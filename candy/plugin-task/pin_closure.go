package task

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/opencharly/plugin-task/candy/plugin-task/params"
	"github.com/opencharly/spec/spec"
)

// pin_closure.go — verb:pin-closure: the TRANSITIVE CLOSURE of a pin advance.
//
// A pin advance is a claim about a SET of modules, and the set is DISCOVERED by
// compiling, never declared by a diff. Measured 2026-10-15 (charly#906): advancing
// `spec` alone broke the pinned `sdk` INSIDE THE SDK'S OWN UNEDITED FILE; with that
// pair advanced, the anchor failed on `plugin-vm`, `plugin-settings`, `plugin-clean`
// and `plugin-build` AND on the core's own `BuildEngine:` literal — five modules,
// three of them named by no plan anyone had. Each rung was invisible from the one
// below it, and every one of them was found by LOOKING rather than by pushing.
//
// Four properties, each paid for by a failure that night:
//
//  1. FIX-POINT, never enumerate — compile, collect EVERY failing module, resolve
//     the ones that only need a pin advance, compile again, repeat until the set
//     stops shrinking. A check that asks "do these N modules compile" fails the
//     moment N is unknown; and stopping at the first failure hid three layers
//     (the alias failure masked the sdk, the sdk masked plugin-vm, plugin-vm
//     masked the rest).
//  2. CLOSURE-DISCOVERED — the set is found, never declared.
//  3. BUMP-vs-EDIT CLASSIFIED — for each member, say whether its newest published
//     version already carries the fix (BUMP: advance the pin) or still references
//     the retired symbol (EDIT: somebody owns a cutover). This is the reader's
//     next action, so a gate that only says "does not compile" sends them to
//     patch modules that need a bump.
//  4. NEVER AN EMPTY ANSWER — if the closure cannot be resolved, the Message says
//     what was TRIED. An empty answer meaning "I gave up" is indistinguishable
//     from "nothing is wrong", which is this campaign's recurring defect.
//
// The report rides the verb's Message (runMaintenanceVerbIn's second return),
// because in this repo's own words "a `command:` step's stdout does not survive
// the pass path at all" — a gate that finds the closure and then discards it on
// the pass path is worse than none, because it would look like it had answered.

const closureMaxRungs = 4

// closureFailure is one failing PACKAGE from a `go build ./...` run: the import
// path `go` named, and the `file:line: message` lines beneath it.
type closureFailure struct {
	Pkg   string
	Lines []string
}

// closureVerdict is what a reader has to act on.
type closureVerdict struct {
	Kind     string // "BUMP" or "EDIT"
	Evidence string // the version that proves a BUMP, or the file:line that needs an EDIT
}

// closurePkgLine matches go's per-package failure header, e.g. "# github.com/x/y/z".
var closurePkgLine = regexp.MustCompile(`^# (.+)$`)

// closureErrLine matches go's error lines, e.g. "f.go:12:3: undefined: spec.Foo".
var closureErrLine = regexp.MustCompile(`^(.+?\.go:\d+:\d+): (.*)$`)

// parseClosureFailures returns EVERY failing package in go build output — never
// only the first. A run that reports one red line reports the first rung only.
func parseClosureFailures(out string) []closureFailure {
	var failures []closureFailure
	index := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := closurePkgLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			pkg := strings.TrimSpace(m[1])
			if _, seen := index[pkg]; !seen {
				index[pkg] = len(failures)
				failures = append(failures, closureFailure{Pkg: pkg})
			}
			continue
		}
		m := closureErrLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || len(failures) == 0 {
			continue
		}
		last := &failures[len(failures)-1]
		last.Lines = append(last.Lines, m[1]+": "+m[2])
	}
	return failures
}

// retiredSymbols pulls the identifiers a failure names as missing, newest-last:
// "undefined: spec.KubernetesHostname" → KubernetesHostname,
// "req.BuildEngine undefined" → BuildEngine, "cfg.Engine.Build undefined" → Build.
// The last dotted segment is the one a source scan can see, and choosing it is
// deliberately CONSERVATIVE: a too-common name yields more hits, hence an EDIT,
// and "if you are unsure it is blocking" is the rule the gate exists to serve.
func retiredSymbols(lines []string) []string {
	seen := map[string]bool{}
	var syms []string
	for _, l := range lines {
		msg := l
		if i := strings.Index(l, ": "); i >= 0 {
			msg = l[i+2:]
		}
		// go appends the reason in parentheses: "(type spec.X has no field or
		// method Y)". Strip it GENERICALLY — matching one module's type name would
		// bake that name into this plugin, and the symbol is what precedes it.
		if i := strings.LastIndex(msg, " ("); i >= 0 && strings.HasSuffix(msg, ")") {
			msg = strings.TrimSpace(msg[:i])
		}
		var expr string
		switch {
		case strings.HasPrefix(msg, "undefined: "):
			expr = strings.TrimSpace(strings.TrimPrefix(msg, "undefined: "))
		case strings.HasSuffix(msg, " undefined"):
			expr = strings.TrimSpace(strings.TrimSuffix(msg, " undefined"))
		case strings.Contains(msg, "not enough arguments"), strings.Contains(msg, "too many arguments"):
			continue // a signature change is not a retired symbol: never a BUMP
		default:
			continue
		}
		expr = strings.Trim(expr, "()")
		parts := strings.Split(expr, ".")
		sym := parts[len(parts)-1]
		if sym == "" || seen[sym] {
			continue
		}
		seen[sym] = true
		syms = append(syms, sym)
	}
	return syms
}

// classifyClosure decides BUMP or EDIT for one failing package. newestDir is the
// source dir of the module's NEWEST published version (empty when it could not be
// fetched — then the answer is EDIT, because an unproven bump is a claim nobody
// checked).
func classifyClosure(symbols []string, newestDir string, symbolCount func(dir, sym string) int) closureVerdict {
	if newestDir == "" {
		return closureVerdict{Kind: "EDIT", Evidence: "newest version could not be fetched — not proven fixable by a bump"}
	}
	if len(symbols) == 0 {
		return closureVerdict{Kind: "EDIT", Evidence: "no retired symbol named in the failure — cannot be a bump"}
	}
	for _, sym := range symbols {
		if n := symbolCount(newestDir, sym); n > 0 {
			return closureVerdict{Kind: "EDIT", Evidence: fmt.Sprintf("%s still referenced in the newest version (%d site(s))", sym, n)}
		}
	}
	return closureVerdict{Kind: "BUMP", Evidence: strings.Join(symbols, ",") + " absent from the newest version"}
}

// renderClosureReport is the Message: the pair it resolved at, every rung it took,
// every failing module with its verdict, and an explicit verdict about the advance.
func renderClosureReport(pair string, rungs []string, order []string, verdicts map[string]closureVerdict, edits []string) string {
	var b strings.Builder
	b.WriteString("pin-closure: resolved at " + pair + "\n")
	for _, r := range rungs {
		b.WriteString("  rung: " + r + "\n")
	}
	if len(order) == 0 {
		b.WriteString("closure: EMPTY — every module in the composed set compiles at this pair\n")
		return b.String()
	}
	bumps, editCount := 0, 0
	for _, m := range order {
		v := verdicts[m]
		if v.Kind == "BUMP" {
			bumps++
		} else {
			editCount++
		}
		b.WriteString(fmt.Sprintf("closure: %-8s %-58s %s\n", v.Kind, m, v.Evidence))
	}
	if editCount > 0 {
		b.WriteString(fmt.Sprintf("advance: REFUSED — %d module(s) need an EDIT (their own cutover), %d can be adopted by a pin bump: %s\n",
			editCount, bumps, strings.Join(edits, ", ")))
	} else {
		b.WriteString(fmt.Sprintf("advance: CLEAN at this pair — %d module(s) need only a pin bump\n", bumps))
	}
	return b.String()
}

// newestModuleVersion returns the newest published version of a module path and
// the local dir of that version's sources (empty dir when it cannot be fetched).
func newestModuleVersion(anchorDir, module string) (string, string) {
	out, err := runGo(anchorDir, "", "list", "-m", "-versions", module)
	if err != nil {
		return "", ""
	}
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return "", ""
	}
	version := fields[len(fields)-1]
	if version == module || !strings.HasPrefix(version, "v") {
		return "", "" // no tagged release: nothing to advance to
	}
	dir := moduleCacheDir(anchorDir, module+"@"+version)
	return version, dir
}

// runGo runs the go tool in dir, with the overlay in force when workFile is set.
func runGo(dir, workFile string, args ...string) (string, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	env := append(os.Environ(), "GOTMPDIR="+goTmpDir())
	if workFile != "" {
		// Workspace mode FORBIDS `-mod` outright ("go: -mod may only be set to readonly or vendor
		// when in workspace mode"), so an ambient GOFLAGS carrying it fails this build with a flag
		// error the operator never set against this verb — measured: this repo's own gate exports
		// `GOFLAGS=-mod=mod` for its charly build, and the fixture run inherited it. The closure
		// defines its own composed module set, so `-mod` is meaningless here: drop it rather than
		// letting the caller's build flags decide whether this verb works.
		env = append(env, "GOWORK="+workFile, "GOFLAGS="+goFlagsWithoutMod(os.Getenv("GOFLAGS")))
	}
	cmd.Env = env
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// goFlagsWithoutMod drops every `-mod` flag from a GOFLAGS value. It exists because the closure
// build runs in WORKSPACE mode, where the Go toolchain rejects `-mod` outright, so inheriting one
// from the caller's environment would fail the verb with an error about a flag it never set.
func goFlagsWithoutMod(flags string) string {
	var kept []string
	for _, f := range strings.Fields(flags) {
		if f == "-mod" || strings.HasPrefix(f, "-mod=") {
			continue
		}
		kept = append(kept, f)
	}
	return strings.Join(kept, " ")
}

func goTmpDir() string {
	if dir := os.Getenv("GOTMPDIR"); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cache", "go-tmp")
	}
	return os.TempDir()
}

// moduleCacheDir resolves a downloaded module version's source dir, or "".
func moduleCacheDir(anchorDir, ref string) string {
	out, err := runGo(anchorDir, "", "mod", "download", "-json", ref)
	if err != nil {
		return ""
	}
	const key = `"Dir": `
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, key) {
			return strings.Trim(strings.TrimPrefix(line, key), `",`)
		}
	}
	return ""
}

// runPinClosure is the live loop: resolve the pair, compose the overlay, build the
// whole closure, classify every failure, adopt the bumps, and repeat until the set
// stops shrinking — then report, in the Message, what a reader must do next.
func runPinClosure(projDir string, in params.PinClosureInput) (spec.Status, string) {
	anchor := in.Anchor
	if anchor == "" {
		anchor = filepath.Join(projDir, "charly")
	}
	if _, err := os.Stat(filepath.Join(anchor, "go.mod")); err != nil {
		return spec.StatusFail, fmt.Sprintf(
			"pin-closure: no anchor module at %s (tried: anchor input, then <project>/charly); set `anchor:` to the module whose go.mod names the pins — %v",
			anchor, err)
	}
	workDir, err := os.MkdirTemp("", "charly-pin-closure-")
	if err != nil {
		return spec.StatusFail, fmt.Sprintf("pin-closure: temp overlay dir: %v", err)
	}
	defer os.RemoveAll(workDir)
	pair := resolvedPair(anchor, in.ModulePrefix)
	// The workspace names the anchor MODULE dir itself: naming its parent makes go
	// reject the workspace ("module not found"), which fails the build with no "# pkg"
	// line at all — the shape of failure that produced a false EMPTY report once.
	members := []string{anchor}
	members = append(members, in.Compose...)
	replaces := map[string]string{}
	workFile := writeOverlay(workDir, members, replaces)

	rungs := []string{}
	order := []string{}
	verdicts := map[string]closureVerdict{}
	edits := []string{}
	for rung := 1; rung <= closureMaxRungs; rung++ {
		out, buildErr := runGo(anchor, workFile, "build", "./...")
		failures := parseClosureFailures(out)
		if buildErr != nil && len(failures) == 0 {
			// The build failed for a reason this parser cannot attribute to a module
			// (a workspace or resolution error, for instance). Say WHAT WAS TRIED and
			// what the tool said: an empty answer that means "I gave up" is
			// indistinguishable from "nothing is wrong", which is this gate's whole
			// reason to exist.
			return spec.StatusFail, fmt.Sprintf(
				"pin-closure: the closure could not be resolved at %s\ntried: go build ./... with GOWORK=%s over %d composed module(s) (%s)\ngo said: %s",
				pair, workFile, len(members), strings.Join(members, ", "), firstLines(out, 6))
		}
		if len(failures) == 0 {
			rungs = append(rungs, fmt.Sprintf("%d: the composed set compiles", rung))
			return spec.StatusPass, renderClosureReport(pair, rungs, order, verdicts, edits)
		}
		rungs = append(rungs, fmt.Sprintf("%d: %d failing package(s)", rung, len(failures)))
		adopted := 0
		for _, f := range failures {
			module := closureModule(f.Pkg)
			if _, seen := verdicts[module]; seen {
				continue
			}
			// Classify against the module the FAILING PACKAGE belongs to, not the
			// repo root: these repos tag both the repo and the subdir module, and
			// the repo-root tag can already be fixed while the module tag a
			// consumer actually pins still carries the retired symbol. Classifying
			// against the wrong one yields a FALSE BUMP — the dangerous direction,
			// because it tells a reader to advance a pin where a cutover is owed.
			version, dir := newestModuleVersion(anchor, f.Pkg)
			v := classifyClosure(retiredSymbols(f.Lines), dir, grepSymbol)
			if v.Kind == "BUMP" {
				v.Evidence = version + " — " + v.Evidence
				replaces[module] = version // adopted: the workspace pins the newest version
				adopted++
			} else {
				if len(f.Lines) > 0 {
					v.Evidence = f.Lines[0] // the file:line its owner needs
				}
				edits = append(edits, module)
			}
			verdicts[module] = v
			order = append(order, module)
		}
		if adopted == 0 {
			rungs = append(rungs, fmt.Sprintf("%d: no member can be advanced by a pin bump — fixed point", rung))
			return spec.StatusFail, renderClosureReport(pair, rungs, order, verdicts, edits)
		}
		workFile = writeOverlay(workDir, members, replaces)
	}
	return spec.StatusFail, renderClosureReport(pair, rungs, order, verdicts, edits)
}

// closureModule maps a failing package to the module a consumer would advance:
// the package's own module path when it resolves, else its repo root (3 segments).
func closureModule(pkg string) string {
	parts := strings.Split(pkg, "/")
	if len(parts) > 3 {
		return strings.Join(parts[:3], "/")
	}
	return pkg
}

// grepSymbol counts the references to sym in a downloaded module's Go sources.
func grepSymbol(dir, sym string) int {
	if dir == "" || sym == "" {
		return 0
	}
	out, _ := exec.Command("grep", "-rl", "--include=*.go", sym, dir).Output()
	n := 0
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" && !strings.HasSuffix(l, "_test.go") {
			n++
		}
	}
	return n
}

// resolvedPair states the pins the anchor resolves at — a reader must be able to
// tell a real answer from a stale one. prefix keeps this verb domain-neutral: the
// CALLER names the org whose pins matter (empty = every require line).
func resolvedPair(anchor, prefix string) string {
	data, err := os.ReadFile(filepath.Join(anchor, "go.mod"))
	if err != nil {
		return "<unresolved: " + anchor + "/go.mod unreadable>"
	}
	var pins []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if prefix != "" && !strings.HasPrefix(line, prefix) {
			continue
		}
		if strings.HasPrefix(line, "//") || strings.HasPrefix(line, "module ") || strings.HasPrefix(line, "go ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.Contains(fields[0], "/") && strings.HasPrefix(fields[1], "v") {
			pins = append(pins, fields[0]+"@"+fields[1])
		}
	}
	sort.Strings(pins)
	if len(pins) == 0 {
		return "<unresolved: no pins matching " + prefix + " in " + anchor + "/go.mod>"
	}
	return strings.Join(pins, " ")
}

// writeOverlay writes the temp go.work naming the composed members (the anchor plus
// whatever the caller or the loop has adopted).
func writeOverlay(workDir string, members []string, replaces map[string]string) string {
	var b strings.Builder
	b.WriteString("go 1.26.4\n\nuse (\n")
	for _, m := range members {
		b.WriteString("\t" + m + "\n")
	}
	b.WriteString(")\n")
	if len(replaces) > 0 {
		var mods []string
		for m := range replaces {
			mods = append(mods, m)
		}
		sort.Strings(mods)
		b.WriteString("\nreplace (\n")
		for _, m := range mods {
			b.WriteString("\t" + m + " => " + m + " " + replaces[m] + "\n")
		}
		b.WriteString(")\n")
	}
	path := filepath.Join(workDir, "go.work")
	_ = os.WriteFile(path, []byte(b.String()), 0o644)
	return path
}

// firstLines bounds a tool's own words for the Message: enough to see the cause,
// never enough to swamp the report.
func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = append(lines[:n], fmt.Sprintf("… (%d more line(s))", len(lines)-n))
	}
	return strings.Join(lines, " | ")
}
