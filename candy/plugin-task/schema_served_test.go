package task

// schema_served_test.go is the schema-LOAD gate, and the §10.1 S3 gate.
//
// `charly box validate` does NOT compile a served schema: a dangling reference
// inside a served def is green at plugin load and only surfaces when the host
// validates an authored `git-submodules:` / `file-parity:` / `prune:` body against
// it (opencharly/spec#301). So this file composes the plugin's OWN Describe reply and
// compiles the body it serves, with the same CUE library the runtime links — the
// served body, not the file on disk, is the surface the host splices onto its base.
//
// It pins the S3 half of D-PAT-3 as well: every closed set of words is a vocabulary
// table, its word list is DERIVED from that table, and the enum an author meets is
// `or()` of the derived list — so a word cannot be added to the table and forgotten
// by the list, nor accepted by the enum while the table it claims to derive from
// lacks it.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	pb "github.com/opencharly/spec/proto"
)

// vocabHarness LIFTS the hidden vocabulary tables and their derived word lists into one
// public definition. An `_`-prefixed label is hidden by CUE and cannot be looked up from
// outside the compilation unit, so the served body alone exposes only the enums; this
// fragment adds no semantics, it re-exports the tables the schema already carries so the
// test can read them instead of restating their words.
//
// The lists are read through `words.<name>` — a REQUIRED field of this definition —
// rather than at the defs' own fields, four of which are optional (and a plain
// LookupPath refuses an optional field).
const vocabHarness = `
#TestVocabulary: {
	tables: {
		submodules_mode: _submodulesMode
		check_sync:      _checkSync
		prune_mode:      _pruneMode
	}
	words: {
		submodules_mode: _submodulesModeWord
		check_sync:      _checkSyncWord
		prune_mode:      _pruneModeWord
	}
}
`

// vocabTables names every vocabulary table the schema carries, as in vocabHarness.
// THREE tables cover FIVE `mode` fields: `_checkSync` is the one two-word set the
// `check`/`sync` verbs share, so it has ONE home and three consumers.
var vocabTables = []string{"submodules_mode", "check_sync", "prune_mode"}

// vocabProbe pairs a table with ONE authored input that uses a word from it at a
// specific FIELD, so the enum is exercised through the same def the host validates
// against.
type vocabProbe struct {
	table string // the key vocabHarness gives this table
	label string // the table's own label in the served body
	field string // "#Def.field" — where an author meets the table's words
	use   func(word string) string
}

// vocabProbes covers every field wired to a vocabulary table. A new field belongs here.
var vocabProbes = []vocabProbe{
	{"submodules_mode", "_submodulesMode", "#GitSubmodulesInput.mode", func(w string) string {
		return fmt.Sprintf(`{#GitSubmodulesInput: {mode: %q}}`, w)
	}},
	{"check_sync", "_checkSync", "#FileParityInput.mode", func(w string) string {
		return fmt.Sprintf(`{#FileParityInput: {mode: %q, pairs: [{left: "a", right: "b"}]}}`, w)
	}},
	{"check_sync", "_checkSync", "#SpliceRegionInput.mode", func(w string) string {
		return fmt.Sprintf(`{#SpliceRegionInput: {mode: %q, target: "t", fragment: "f", begin: "B", end: "E"}}`, w)
	}},
	{"check_sync", "_checkSync", "#ModulePinsInput.mode", func(w string) string {
		return fmt.Sprintf(`{#ModulePinsInput: {mode: %q, source_go_mod: "g", glob: "*", keys: ["k"]}}`, w)
	}},
	{"prune_mode", "_pruneMode", "#PruneInput.mode", func(w string) string {
		return fmt.Sprintf(`{#PruneInput: {mode: %q}}`, w)
	}},
}

// servedSchema returns the plugin's served CUE body COMPILED, with the test's table
// re-export appended. NewMeta's Describe already compiles the concatenated body
// (sdk.BuildCapabilities), so a failure here means the schema the host receives does
// not stand on its own.
func servedSchema(t *testing.T) cue.Value {
	t.Helper()
	caps, err := NewMeta().Describe(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	body := caps.GetSchemaCue()
	if body == "" {
		t.Fatal("Describe served an EMPTY CUE body")
	}
	v := cuecontext.New().CompileString(body + vocabHarness)
	if err := v.Err(); err != nil {
		t.Fatalf("served schema does not compile: %v", err)
	}
	return v
}

// schemaFieldWordList returns the word list of an optionally-declared field: a plain
// LookupPath refuses an optional field, so the struct's arcs are walked with
// cue.Optional(true).
func schemaFieldWordList(t *testing.T, v cue.Value, def, field string) []string {
	t.Helper()
	path := fmt.Sprintf("%s.%s", def, field)
	sv := v.LookupPath(cue.ParsePath(def))
	if err := sv.Err(); err != nil {
		t.Fatalf("%s does not resolve in the served schema: %v", def, err)
	}
	it, err := sv.Fields(cue.Optional(true))
	if err != nil {
		t.Fatalf("%s: enumerate fields: %v", def, err)
	}
	for it.Next() {
		if it.Selector().Unquoted() == field {
			seen := map[string]bool{}
			collectWords(t, path, it.Value(), 0, seen)
			words := make([]string, 0, len(seen))
			for w := range seen {
				words = append(words, w)
			}
			sort.Strings(words)
			return words
		}
	}
	t.Fatalf("%s has no field %q", def, field)
	return nil
}

func collectWords(t *testing.T, path string, val cue.Value, depth int, seen map[string]bool) {
	t.Helper()
	if err := val.Err(); err != nil {
		t.Fatalf("%s does not resolve in the served schema: %v", path, err)
	}
	if depth > 16 {
		t.Fatalf("%s: the disjunction nests deeper than 16 levels", path)
	}
	if val.IsConcrete() && val.IncompleteKind() == cue.StringKind {
		w, err := val.String()
		if err != nil {
			t.Fatalf("%s: a concrete string does not stringify: %v", path, err)
		}
		seen[w] = true
		return
	}
	_, alts := val.Expr()
	if len(alts) == 0 {
		t.Fatalf("%s has no alternatives — it is not a word list", path)
	}
	for _, alt := range alts {
		collectWords(t, path, alt, depth+1, seen)
	}
}

// TestServedSchemaResolvesItsDefs — every definition the plugin's contract names must
// still resolve in the served body.
func TestServedSchemaResolvesItsDefs(t *testing.T) {
	v := servedSchema(t)
	for _, path := range []string{
		"#TaskInput", "#GitSubmodulesInput", "#FileParityInput", "#FileParityPair",
		"#SpliceRegionInput", "#ModulePinsInput", "#PinClosureInput", "#PruneInput",
	} {
		if err := v.LookupPath(cue.ParsePath(path)).Err(); err != nil {
			t.Errorf("served schema: %s does not resolve: %v", path, err)
		}
	}
}

// TestVocabularyTablesNameTheirOwner pins the §3.1 ownership tag: each table carries
// `@vocab(<the plugin's own word>)`, so a table copied out of another plugin's schema
// cannot pass as this one's.
//
// The assertion is made on the served TEXT because a `@vocab` is a compile-time
// annotation: it does not survive a reference, so it cannot be read through the
// harness's `tables.<name>` lift. It is the attribute and ONLY the attribute that is
// read as text here — no word set is ever counted from the text.
func TestVocabularyTablesNameTheirOwner(t *testing.T) {
	caps, err := NewMeta().Describe(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	body := caps.GetSchemaCue()
	tags := regexp.MustCompile(`@vocab\(([^)]*)\)`).FindAllStringSubmatch(body, -1)
	if len(tags) != len(vocabTables) {
		t.Fatalf("served body carries %d @vocab tags for %d vocabulary tables — every table is tagged exactly once", len(tags), len(vocabTables))
	}
	for _, tag := range tags {
		if tag[1] != "task" {
			t.Errorf("a vocabulary table is tagged @vocab(%s), want @vocab(task) — the table claims an owner that is not this plugin", tag[1])
		}
	}
}

// TestS3VocabularyTablesDriveTheirEnums proves, for every vocabulary table, that (a)
// the derived word list IS the table's row set, and (b) each word is accepted where an
// author uses it while a non-word is refused. (b) is what a table that is merely
// DECLARED — never wired into the enum — fails.
func TestS3VocabularyTablesDriveTheirEnums(t *testing.T) {
	v := servedSchema(t)
	ctx := v.Context()
	var lifted struct {
		Tables map[string]map[string]any
		Words  map[string][]string
	}
	if err := v.LookupPath(cue.ParsePath("#TestVocabulary")).Decode(&lifted); err != nil {
		t.Fatalf("lift the vocabulary tables: %v", err)
	}
	// (a) once per TABLE: the derived list is the table's row set.
	for _, name := range vocabTables {
		rows, ok := lifted.Tables[name]
		if !ok {
			t.Errorf("no vocabulary table %q in the served schema", name)
			continue
		}
		if len(rows) == 0 {
			t.Errorf("vocabulary table %q has no rows", name)
			continue
		}
		words, ok := lifted.Words[name]
		if !ok {
			t.Errorf("no derived word list for %q in the served schema", name)
			continue
		}
		keys := make([]string, 0, len(rows))
		for w := range rows {
			keys = append(keys, w)
		}
		sort.Strings(keys)
		sort.Strings(words)
		if fmt.Sprint(keys) != fmt.Sprint(words) {
			t.Errorf("%q's derived list %v is not its table's row set %v", name, words, keys)
		}
	}
	// (b) once per FIELD: the enum the AUTHOR meets there is the table's row set too —
	// not merely a set that happens to accept every row today. A field written as a
	// literal list equal to its table passes (a) and fails here.
	for _, p := range vocabProbes {
		rows, ok := lifted.Tables[p.table]
		if !ok {
			continue
		}
		keys := make([]string, 0, len(rows))
		for w := range rows {
			keys = append(keys, w)
		}
		sort.Strings(keys)
		dfn, fld, ok := strings.Cut(p.field, ".")
		if !ok {
			t.Fatalf("%q is not a \"#Def.field\" path", p.field)
		}
		if got := schemaFieldWordList(t, v, dfn, fld); fmt.Sprint(got) != fmt.Sprint(keys) {
			t.Errorf("%s accepts %v but its table %q holds %v — the field is not wired to that table", p.field, got, p.table, keys)
		}
	}
	// (c) once per FIELD: every table word is accepted where an author uses it, and a
	// non-word is refused — which is what a table never wired into its enum fails.
	for _, p := range vocabProbes {
		rows, ok := lifted.Tables[p.table]
		if !ok {
			continue
		}
		for w := range rows {
			if err := v.Unify(ctx.CompileString(p.use(w))).Err(); err != nil {
				t.Errorf("%s refuses the table word %q an author may write: %v", p.field, w, err)
			}
		}
		if err := v.Unify(ctx.CompileString(p.use("zzz-not-a-word"))).Err(); err == nil {
			t.Errorf("%s accepts a non-word — the enum is not the table", p.field)
		}
	}
}

// appendTableRow returns the served body with one word APPENDED to the named table's
// declaration. The tables are written on one line each — `_name: {a: {}, b: {}} @vocab(x)`
// — so a row can be inserted without parsing CUE, and the inserted key is QUOTED so a
// hyphen is a legal label. A table that stops being one line fails LOUDLY here rather than
// mutating the wrong text.
func appendTableRow(t *testing.T, body, table, word string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^(` + regexp.QuoteMeta(table) + `): \{(.*)\} @vocab\(`)
	loc := re.FindStringSubmatchIndex(body)
	if loc == nil {
		t.Fatalf("could not locate the one-line declaration of %s in the served body — this probe edits the TABLE, so a reformatted table must be taught to it", table)
	}
	return body[:loc[4]] + body[loc[4]:loc[5]] + `, "` + word + `": {}` + body[loc[5]:]
}

// TestATableWordReachesItsField proves the DERIVATION and not merely the equality of two
// sets: a word appended to a table must appear in the enum the field accepts.
//
// This is the direction (a) and (b) cannot see when the shapes are equal on both sides of
// a mutation-free comparison. A field written as a literal list whose words happen to
// equal its table passes every equality check — and it is exactly the form that drifts the
// moment the table gains a row, because the literal was never wired to anything.
func TestATableWordReachesItsField(t *testing.T) {
	caps, err := NewMeta().Describe(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	body := caps.GetSchemaCue()
	ctx := cuecontext.New()
	const extra = "zzz-gained-word"
	for _, p := range vocabProbes {
		v := ctx.CompileString(appendTableRow(t, body, p.label, extra))
		if err := v.Err(); err != nil {
			t.Fatalf("%s: the body with a row added to %s does not compile: %v", p.table, p.label, err)
		}
		if err := v.Unify(ctx.CompileString(p.use(extra))).Err(); err != nil {
			t.Errorf("%s does not accept %q after %s gained that row — the field is NOT derived from the table, it only happens to agree with it today: %v", p.field, extra, p.label, err)
		}
	}
}
