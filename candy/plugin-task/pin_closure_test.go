package task

// pin_closure_test.go — the acceptance case for verb:pin-closure, taken from the
// ladder that was walked BY HAND on 2026-10-15 (charly#906): advancing `spec` alone
// broke the pinned sdk in the sdk's own unedited file; with that pair advanced the
// build named four plugins AND the core's own literal — five modules, three of them
// named by no plan. The fixtures below are the REAL `go build` output, so the gate
// is tested against a case that happened rather than one that was imagined.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/plugin-task/candy/plugin-task/params"
	"github.com/opencharly/spec/spec"
)

// ladderOutput is verbatim `go build ./...` output from the overlay probe: two
// packages, one with a signature change and no retired symbol, one with both.
const ladderOutput = `# github.com/opencharly/plugin-build/candy/plugin-build
/home/atrawog/go/pkg/mod/github.com/opencharly/plugin-build/candy/plugin-build@v0.2026286.1121/drive.go:75:25: reply.PodmanJobs undefined (type spec.BuildResolveReply has no field or method PodmanJobs)
/home/atrawog/go/pkg/mod/github.com/opencharly/plugin-build/candy/plugin-build@v0.2026286.1121/ensure.go:71:9: req.BuildEngine undefined (type spec.BuildEnsureRequest has no field or method BuildEngine)
# github.com/opencharly/plugin-clean/candy/plugin-clean
/home/atrawog/go/pkg/mod/github.com/opencharly/plugin-clean/candy/plugin-clean@v0.2026271.2313/retention.go:202:29: rt.BuildEngine undefined (type *hostenv.ResolvedRuntime has no field or method BuildEngine)
# github.com/opencharly/plugin-vm/candy/plugin-vm
/home/atrawog/go/pkg/mod/github.com/opencharly/plugin-vm/candy/plugin-vm@v0.2026286.856/vm_box_emit.go:104:63: not enough arguments in call to deploykit.EmitVmBoxAt
`

// TestParseClosureFailures_EveryPackageNotTheFirst pins property 1's second half:
// early exit is what hid three layers in one night, so the parser must return EVERY
// failing package, in order, with their lines.
func TestParseClosureFailures_EveryPackageNotTheFirst(t *testing.T) {
	got := parseClosureFailures(ladderOutput)
	if len(got) != 3 {
		t.Fatalf("got %d failing package(s), want 3 (never stop at the first): %+v", len(got), got)
	}
	wantPkgs := []string{
		"github.com/opencharly/plugin-build/candy/plugin-build",
		"github.com/opencharly/plugin-clean/candy/plugin-clean",
		"github.com/opencharly/plugin-vm/candy/plugin-vm",
	}
	for i, want := range wantPkgs {
		if got[i].Pkg != want {
			t.Errorf("failure %d = %q, want %q", i, got[i].Pkg, want)
		}
	}
	if len(got[0].Lines) != 2 || len(got[1].Lines) != 1 {
		t.Errorf("line capture wrong: %d and %d, want 2 and 1", len(got[0].Lines), len(got[1].Lines))
	}
}

// TestRetiredSymbols pins the extraction, including the case that must NOT look
// like a retired symbol: a signature change can never be fixed by a pin bump, so
// it must not produce a symbol that a source scan could mistakenly clear.
func TestRetiredSymbols(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"undefined with package qualifier", []string{"f.go:1:2: undefined: spec.KubernetesHostname"}, []string{"KubernetesHostname"}},
		{"trailing undefined", []string{"f.go:1:2: req.BuildEngine undefined (type spec.BuildEnsureRequest has no field or method BuildEngine)"}, []string{"BuildEngine"}},
		{"trailing undefined, dotted", []string{"f.go:1:2: cfg.Engine.Build undefined (type hostenv.EngineConfig has no field or method Build)"}, []string{"Build"}},
		{"signature change names no symbol", []string{"f.go:1:2: not enough arguments in call to deploykit.EmitVmBoxAt"}, nil},
	}
	for _, c := range cases {
		got := retiredSymbols(c.in)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// TestClassifyClosure_BumpVersusEdit is the property that decides the reader's next
// action. A member whose NEWEST published version no longer references the retired
// symbol is a BUMP (advance the pin); one that still does is an EDIT (its own
// cutover); and one that could not be fetched is an EDIT because an unproven bump is
// a claim nobody checked.
func TestClassifyClosure_BumpVersusEdit(t *testing.T) {
	absent := func(_, _ string) int { return 0 }
	present := func(_, _ string) int { return 3 }

	if v := classifyClosure([]string{"BuildEngine"}, "/newest", absent); v.Kind != "BUMP" {
		t.Errorf("newest no longer references it: got %s (%s), want BUMP", v.Kind, v.Evidence)
	}
	if v := classifyClosure([]string{"BuildEngine"}, "/newest", present); v.Kind != "EDIT" {
		t.Errorf("newest still references it: got %s, want EDIT", v.Kind)
	}
	if v := classifyClosure([]string{"BuildEngine"}, "", absent); v.Kind != "EDIT" {
		t.Errorf("unfetched newest: got %s, want EDIT (an unproven bump is a claim)", v.Kind)
	}
	if v := classifyClosure(nil, "/newest", absent); v.Kind != "EDIT" {
		t.Errorf("no symbol named (a signature change): got %s, want EDIT", v.Kind)
	}
}

// TestRenderClosureReport_RefusesWhileAnEditRemains pins property 4: the Message
// states the pair it resolved at, every rung, every member with its verdict, and it
// REFUSES the advance while any EDIT remains — the channel that survives the pass
// path must never answer "clean" over a set that does not compile.
func TestRenderClosureReport_RefusesWhileAnEditRemains(t *testing.T) {
	pair := "github.com/opencharly/spec@v0.2026288.554 github.com/opencharly/sdk@v0.2026287.2235"
	verdicts := map[string]closureVerdict{
		"github.com/opencharly/plugin-vm/candy/plugin-vm":       {Kind: "BUMP", Evidence: "v0.2026287.2320 — BuildEngine absent from the newest version"},
		"github.com/opencharly/plugin-clean/candy/plugin-clean": {Kind: "EDIT", Evidence: "retention.go:202:29: rt.BuildEngine undefined"},
	}
	msg := renderClosureReport(pair, []string{"1: 3 failing package(s)", "2: 1 failing package(s)"}, []string{
		"github.com/opencharly/plugin-vm/candy/plugin-vm",
		"github.com/opencharly/plugin-clean/candy/plugin-clean",
	}, verdicts, []string{"github.com/opencharly/plugin-clean/candy/plugin-clean"})

	if !strings.Contains(msg, "github.com/opencharly/spec@v0.2026288.554") {
		t.Error("the Message must state the resolved pair, so a reader can tell a real answer from a stale one")
	}
	if !strings.Contains(msg, "1: 3 failing package(s)") || !strings.Contains(msg, "2: 1 failing package(s)") {
		t.Error("the Message must say which rung of the loop it is on")
	}
	if !strings.Contains(msg, "BUMP") || !strings.Contains(msg, "EDIT") {
		t.Error("every member must be classified BUMP or EDIT — that is the reader's next action")
	}
	if !strings.Contains(msg, "REFUSED") {
		t.Error("an advance with an EDIT remaining must be REFUSED, never reported clean")
	}
}

// TestRenderClosureReport_NeverEmpty pins the other half of property 4: an empty
// closure is stated as such, never as silence — "I gave up" and "nothing is wrong"
// must be distinguishable.
func TestRenderClosureReport_NeverEmpty(t *testing.T) {
	msg := renderClosureReport("github.com/opencharly/spec@v0.2026288.554", []string{"1: the composed set compiles"}, nil, nil, nil)
	if !strings.Contains(msg, "EMPTY") || !strings.Contains(msg, "resolved at") {
		t.Errorf("an empty closure must be stated, not silent: %q", msg)
	}
}

// TestResolvedPair_UnresolvedSaysWhatItTried pins the third clause of property 4.
func TestResolvedPair_UnresolvedSaysWhatItTried(t *testing.T) {
	msg := resolvedPair("/nonexistent-anchor-dir", "")
	if !strings.Contains(msg, "unresolved") || !strings.Contains(msg, "/nonexistent-anchor-dir/go.mod") {
		t.Errorf("an unresolvable pair must name what was tried: %q", msg)
	}
}

// TestClosureModule maps a failing package to the module a consumer would advance.
func TestClosureModule(t *testing.T) {
	got := closureModule("github.com/opencharly/plugin-clean/candy/plugin-clean")
	if got != "github.com/opencharly/plugin-clean" {
		t.Errorf("got %q, want the repo root a consumer advances", got)
	}
}

// TestGoFlagsWithoutMod pins the workspace-mode guard: a caller's `-mod` flag must never reach the
// closure build, because the Go toolchain rejects it outright in workspace mode (measured — this
// repo's own gate exports GOFLAGS=-mod=mod for its charly build, and the fixture run inherited it
// and failed the verb). Every OTHER flag is preserved: the sanitizer removes one flag, not a
// user's flags.
func TestGoFlagsWithoutMod(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"-mod=mod", ""},
		{"-mod=readonly", ""},
		// GOFLAGS entries are `-flag=value` (or a bare bool flag): `-mod vendor` is NOT a
		// representable GOFLAGS value, so the sanitizer is not asked to parse it.
		{"-mod -trimpath", "-trimpath"},
		{"-mod=mod -trimpath", "-trimpath"},
		{"-trimpath -mod=mod -tags x", "-trimpath -tags x"},
		{"-trimpath", "-trimpath"},
	} {
		if got := goFlagsWithoutMod(c.in); got != c.want {
			t.Errorf("goFlagsWithoutMod(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRunPinClosure_IteratesToAFixedPointAfterABump is the proof the BUMP arm runs — the arm the
// live gate's two fixtures never reach, because both end at rung 1 with nothing adopted. It builds a
// REAL three-module fixture on disk, so the loop is not simulated:
//
//	anchor (this project) requires example.com/org/repo/candy/dep, replaced at v0.1.0 with a source tree whose own
//	package DOES NOT COMPILE ("undefined: retired"). The newest version's tree compiles.
//
// Rung 1 must classify the failure as BUMP (the symbol is absent from the newest source), ADOPT it
// (write the replace into the workspace, keyed by the SAME module path the lookup used), re-compose
// the overlay, and take a SECOND rung that compiles. A bump that is never adopted — the defect the
// review named — fails this test at rung 1.
func TestRunPinClosure_IteratesToAFixedPointAfterABump(t *testing.T) {
	root := t.TempDir()
	writeFile := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	writeFile("anchor/go.mod", "module example.com/anchor\n\ngo 1.24\n\nrequire example.com/org/repo/candy/dep v0.1.0\n\nreplace example.com/org/repo/candy/dep => ../depold\n")
	writeFile("anchor/main.go", "package main\n\nimport \"example.com/org/repo/candy/dep\"\n\nfunc main() { dep.Fine() }\n")
	// The pinned version's own package does not compile — the shape a retired symbol leaves behind.
	writeFile("depold/go.mod", "module example.com/org/repo/candy/dep\n\ngo 1.24\n")
	writeFile("depold/dep.go", "package dep\n\nfunc Fine() { retired() }\n")
	// The newest published version carries the fix.
	writeFile("depnew/go.mod", "module example.com/org/repo/candy/dep\n\ngo 1.24\n")
	writeFile("depnew/dep.go", "package dep\n\nfunc Fine() {}\n")

	anchor := filepath.Join(root, "anchor")
	depNew := filepath.Join(root, "depnew")

	orig := newestModuleVersionFn
	newestModuleVersionFn = func(anchorDir, module string) (string, string) {
		if module != "example.com/org/repo/candy/dep" {
			t.Fatalf("the version lookup must be keyed on the MODULE the failing package belongs to, got %q", module)
		}
		return "v0.2.0", depNew
	}
	defer func() { newestModuleVersionFn = orig }()

	status, msg := runPinClosure(root, params.PinClosureInput{Anchor: anchor})
	if status != spec.StatusPass {
		t.Fatalf("runPinClosure = %s, want PASS after the bump is adopted\n%s", status, msg)
	}
	for _, want := range []string{
		"closure: BUMP",                  // the classification
		"example.com/org/repo/candy/dep", // the module that was adopted, by its REAL path
		"2: the composed set compiles",   // the SECOND rung: the iteration happened
		"1: 1 failing package(s)",        // rung 1 saw the breakage
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("report must contain %q — it is the evidence the loop adopted and iterated\n%s", want, msg)
		}
	}
}

// TestWorkspaceGoDerivesTheFloorFromTheAnchor pins the `go` line of the generated workspace: the
// anchor's own directive, never a literal, and a conservative floor only when it cannot be read.
func TestWorkspaceGoDerivesTheFloorFromTheAnchor(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if got := workspaceGo(dir); got != "1.24" {
		t.Errorf("workspaceGo = %q, want the anchor's own directive 1.24", got)
	}
	if got := workspaceGo(filepath.Join(dir, "missing")); got != "1.21" {
		t.Errorf("workspaceGo(no go.mod) = %q, want the conservative floor 1.21", got)
	}
}

// TestWriteOverlay_AdoptsADirOrAVersion pins both replace forms: a downloaded dir is used AS IS (no
// version form, so the next build does not re-resolve it), a version is written in the module form.
func TestWriteOverlay_AdoptsADirOrAVersion(t *testing.T) {
	overlay := writeOverlay(t.TempDir(), "1.24", []string{"/src/anchor"}, map[string]string{
		"example.com/by-dir":     "/cache/example.com/by-dir@v0.2.0",
		"example.com/by-version": "v0.2.0",
	})
	data, err := os.ReadFile(overlay)
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"go 1.24",
		"example.com/by-dir => /cache/example.com/by-dir@v0.2.0",
		"example.com/by-version => example.com/by-version v0.2.0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("overlay must contain %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "go 1.26.4") {
		t.Errorf("the workspace must not carry the retired hardcoded floor\n%s", got)
	}
}
