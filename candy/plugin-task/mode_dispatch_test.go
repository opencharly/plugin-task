package task

// mode_dispatch_test.go pins the catalog invariant for THIS plugin's five `mode`
// word lists: the words the schema advertises at a verb's `mode` field are exactly the
// words that verb's own Go code handles.
//
// Without it a word can sit in the catalog as a phantom: the schema validates the
// authored `mode:` and the verb then answers `unknown mode` — or, worse, silently
// falls through its `if mode == …` chain to whatever the last branch does. The
// `check`/`sync` pair is advertised at THREE defs (#FileParityInput,
// #SpliceRegionInput, #ModulePinsInput) while the other two tables have one consumer
// each, so the invariant is asserted per FIELD, not per table.
//
// Both readers read the real sources: the served schema through the CUE API
// (schema_served_test.go), and the verb's own function body out of the Go file that
// implements it. Neither restates a word.

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

// modeConsumer binds a served `mode` field to the Go function that dispatches on it.
type modeConsumer struct {
	file string
	fn   string
	def  string
}

var modeConsumers = []modeConsumer{
	{"verbs.go", "runGitSubmodules", "#GitSubmodulesInput"},
	{"verbs.go", "runFileParity", "#FileParityInput"},
	{"verbs.go", "runSpliceRegion", "#SpliceRegionInput"},
	{"verbs.go", "runModulePins", "#ModulePinsInput"},
	{"prune.go", "runPrune", "#PruneInput"},
}

// funcSource returns the body of one top-level Go function, up to the next top-level
// `func `. Scoping matters: verbs.go holds several functions, and a scan over the whole
// file would mix their mode words.
func funcSource(t *testing.T, path, name string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(b)
	start := regexp.MustCompile(`(?m)^func ` + name + `\(`).FindStringIndex(src)
	if start == nil {
		t.Fatalf("%s: no func %s", path, name)
	}
	rest := src[start[0]:]
	if end := regexp.MustCompile(`(?m)^func `).FindStringIndex(rest[1:]); end != nil {
		rest = rest[:end[0]+1]
	}
	return rest
}

// switchArmWords returns the `case "<word>":` words of the first `switch <sel> {` in
// body that sit at brace depth 1 relative to that switch. Depth-scoping is what keeps
// runGitSubmodules' NESTED `switch pinnedFromState {` arms (not-rolled, unmaterialized)
// out of the mode word set.
func switchArmWords(t *testing.T, body, sel string) []string {
	t.Helper()
	idx := regexp.MustCompile(`(?m)^\s*switch ` + regexp.QuoteMeta(sel) + ` \{`).FindStringIndex(body)
	if idx == nil {
		return nil
	}
	tail := body[idx[0]:]
	depth, done := 0, false
	seen := map[string]bool{}
	caseRe := regexp.MustCompile(`^case "([a-z][a-z0-9-]*)":`)
	lineRe := regexp.MustCompile(`[^\n]*\n?`)
	for !done && len(tail) > 0 {
		line := lineRe.FindString(tail)
		tail = tail[len(line):]
		trimmed := regexp.MustCompile(`^[ \t]*`).ReplaceAllString(line, "")
		if depth == 1 {
			if m := caseRe.FindStringSubmatch(trimmed); m != nil {
				seen[m[1]] = true
			}
		}
		for _, c := range line {
			switch c {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					done = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for w := range seen {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

// dispatchWords returns every mode word a verb's own code handles: the arms of its
// `switch in.Mode`, the words it compares `mode` against in an `if`, and the word it
// substitutes when `mode` was omitted (its Go-level default).
func dispatchWords(t *testing.T, body string) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, w := range switchArmWords(t, body, "in.Mode") {
		seen[w] = true
	}
	for _, w := range switchArmWords(t, body, "mode") {
		seen[w] = true
	}
	for _, m := range regexp.MustCompile(`(?m)^.*\b(?:mode|in\.Mode)\s*(?:==|!=)\s*"([a-z][a-z0-9-]*)".*$`).FindAllStringSubmatch(body, -1) {
		seen[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`(?m)^\s*mode = "([a-z][a-z0-9-]*)"\s*$`).FindAllStringSubmatch(body, -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for w := range seen {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

// TestModeEnumMatchesItsDispatch asserts, for every `mode` field, that the words the
// served schema advertises there and the words that field's own verb handles are the
// SAME SET — in both directions, per field.
func TestModeEnumMatchesItsDispatch(t *testing.T) {
	v := servedSchema(t)
	for _, c := range modeConsumers {
		advertised := schemaFieldWordList(t, v, c.def, "mode")
		if len(advertised) == 0 {
			t.Errorf("%s.mode advertises no words — the invariant would pass over nothing", c.def)
			continue
		}
		handled := dispatchWords(t, funcSource(t, c.file, c.fn))
		if len(handled) == 0 {
			t.Errorf("%s: no mode words found in %s — the reader is broken, not the code", c.fn, c.file)
			continue
		}
		for _, w := range advertised {
			found := false
			for _, h := range handled {
				if h == w {
					found = true
				}
			}
			if !found {
				t.Errorf("%s.mode advertises %q, which %s does NOT handle — the word is a phantom and the authored step fails only at dispatch", c.def, w, c.fn)
			}
		}
		for _, h := range handled {
			found := false
			for _, w := range advertised {
				if w == h {
					found = true
				}
			}
			if !found {
				t.Errorf("%s handles mode %q, which %s.mode does NOT accept — the arm is unreachable", c.fn, h, c.def)
			}
		}
	}
}

// TestGoModeDefaultsAreAdvertised pins the other half of dispatch on the four verbs
// whose `mode` is OPTIONAL: the word the Go code substitutes for an omitted mode is
// the behaviour an author gets by writing nothing, so it must be a word the schema
// accepts — otherwise every authored body that relies on the default dispatches on a
// word the catalog does not carry.
func TestGoModeDefaultsAreAdvertised(t *testing.T) {
	v := servedSchema(t)
	defRe := regexp.MustCompile(`(?m)^\s*mode = "([a-z][a-z0-9-]*)"\s*$`)
	for _, c := range modeConsumers {
		body := funcSource(t, c.file, c.fn)
		defaults := defRe.FindAllStringSubmatch(body, -1)
		advertised := schemaFieldWordList(t, v, c.def, "mode")
		for _, d := range defaults {
			found := false
			for _, w := range advertised {
				if w == d[1] {
					found = true
				}
			}
			if !found {
				t.Errorf("%s defaults an omitted mode to %q, which %s.mode does NOT accept", c.fn, d[1], c.def)
			}
		}
	}
}
