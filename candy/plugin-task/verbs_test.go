package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// --- git-submodules ---

// gitRepo initializes a git repo at dir with a single commit, returning a helper to
// run git in it.
func gitRepo(t *testing.T, dir string) func(args ...string) string {
	t.Helper()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	return run
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGitSubmodules_Status proves status enumerates a declared submodule.
func TestGitSubmodules_Status(t *testing.T) {
	dir := t.TempDir()
	run := gitRepo(t, dir)
	writeFile(t, filepath.Join(dir, ".gitmodules"),
		"[submodule \"foo\"]\n\tpath = foo\n\turl = https://example.invalid/foo.git\n\tbranch = main\n")
	writeFile(t, filepath.Join(dir, "README"), "x")
	run("add", "-A")
	run("commit", "-qm", "init")

	st, msg := runMaintenanceVerbIn(dir, "git-submodules", map[string]any{"mode": "status"})
	if st != spec.StatusPass {
		t.Fatalf("status: %s: %s", st, msg)
	}
	if !strings.Contains(msg, "foo") {
		t.Fatalf("status must list foo: %q", msg)
	}
}

// TestGitSubmodules_VerifyPolicyB proves the policy-B comparison passes when the
// umbrella gitlink equals the pinned repo's twin, and fails when the twin is
// MISSING (never a vacuous empty==empty pass). It uses a real gitlink written
// through `git update-index --cacheinfo` (no nested submodule clone needed).
func TestGitSubmodules_VerifyPolicyB(t *testing.T) {
	dir := t.TempDir()
	run := gitRepo(t, dir)
	sha := strings.Repeat("a", 40)
	writeG := func(path, s string) {
		cmd := exec.Command("git", "-C", dir, "update-index", "--add", "--cacheinfo",
			"160000,"+s+","+path)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("update-index %s: %v\n%s", path, err, out)
		}
	}
	writeFile(t, filepath.Join(dir, ".gitmodules"),
		"[submodule \"target\"]\n\tpath = target\n\turl = u\n\tbranch = main\n")
	// Stage .gitmodules, then write the gitlink LAST: a plain `git add -A` after
	// update-index would stage the gitlink's "deletion" (there is no worktree dir).
	run("add", ".gitmodules")
	// The umbrella carries a gitlink at `target`; the pinned checkout (this same
	// repo, ".") carries the SAME gitlink at `target` so policy B holds.
	writeG("target", sha)
	run("commit", "-qm", "parent")

	// PASS: pin_map target -> target resolves to the identical gitlink in pinned ".".
	st, msg := runMaintenanceVerbIn(dir, "git-submodules", map[string]any{
		"mode": "verify", "pinned_from": ".", "pin_map": map[string]any{"target": "target"},
	})
	if st != spec.StatusPass {
		t.Fatalf("matching gitlinks must pass policy B, got %s: %s", st, msg)
	}

	// FAIL: a pinned path with NO gitlink must fail loudly (never vacuous).
	st, msg = runMaintenanceVerbIn(dir, "git-submodules", map[string]any{
		"mode": "verify", "pinned_from": ".", "pin_map": map[string]any{"target": "absent/twin"},
	})
	if st != spec.StatusFail {
		t.Fatalf("a missing twin gitlink must fail, got %s: %s", st, msg)
	}
}

// TestGitSubmodules_UnknownMode proves an unknown mode fails loudly.
func TestGitSubmodules_UnknownMode(t *testing.T) {
	dir := t.TempDir()
	gitRepo(t, dir)
	writeFile(t, filepath.Join(dir, ".gitmodules"), "")
	st, _ := runMaintenanceVerbIn(dir, "git-submodules", map[string]any{"mode": "nope"})
	if st != spec.StatusFail {
		t.Fatalf("unknown mode must fail, got %s", st)
	}
}

// --- file-parity ---

func TestFileParity_CheckDriftAndSync(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a"), "same")
	writeFile(t, filepath.Join(dir, "b"), "same")
	writeFile(t, filepath.Join(dir, "c"), "one")
	writeFile(t, filepath.Join(dir, "d"), "two")
	pairs := []any{map[string]any{"left": "a", "right": "b"}, map[string]any{"left": "c", "right": "d"}}

	st, msg := runMaintenanceVerbIn(dir, "file-parity", map[string]any{"mode": "check", "pairs": pairs})
	if st != spec.StatusFail {
		t.Fatalf("drift must fail, got %s: %s", st, msg)
	}

	// sync copies left -> right; a subsequent check passes.
	st, msg = runMaintenanceVerbIn(dir, "file-parity", map[string]any{"mode": "sync", "pairs": pairs})
	if st != spec.StatusPass {
		t.Fatalf("sync: %s: %s", st, msg)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "d")); string(got) != "one" {
		t.Fatalf("sync did not copy c -> d: %q", got)
	}
	st, _ = runMaintenanceVerbIn(dir, "file-parity", map[string]any{"mode": "check", "pairs": pairs})
	if st != spec.StatusPass {
		t.Fatalf("post-sync check must pass, got %s", st)
	}
}

func TestFileParity_NoPairsFails(t *testing.T) {
	dir := t.TempDir()
	st, _ := runMaintenanceVerbIn(dir, "file-parity", map[string]any{})
	if st != spec.StatusFail {
		t.Fatalf("no pairs must fail, got %s", st)
	}
}

// --- splice-region ---

func TestSpliceRegion_SyncThenCurrent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "frag"), "before\nBEGIN\nnew body\nEND\nafter\n")
	writeFile(t, filepath.Join(dir, "tgt"), "head\nBEGIN\nold body\nEND\ntail\n")

	st, msg := runMaintenanceVerbIn(dir, "splice-region", map[string]any{
		"target": "tgt", "fragment": "frag", "begin": "BEGIN", "end": "END",
	})
	if st != spec.StatusPass {
		t.Fatalf("splice: %s: %s", st, msg)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "tgt"))
	want := "head\nBEGIN\nnew body\nEND\ntail\n"
	if string(got) != want {
		t.Fatalf("splice result:\n%q\nwant\n%q", got, want)
	}
	// A second sync is a no-op (already current).
	st, msg = runMaintenanceVerbIn(dir, "splice-region", map[string]any{
		"target": "tgt", "fragment": "frag", "begin": "BEGIN", "end": "END",
	})
	if st != spec.StatusPass || !strings.Contains(msg, "already current") {
		t.Fatalf("idempotent splice: %s: %s", st, msg)
	}
	// check mode on a stale target fails.
	writeFile(t, filepath.Join(dir, "tgt"), "head\nBEGIN\nstale\nEND\ntail\n")
	st, _ = runMaintenanceVerbIn(dir, "splice-region", map[string]any{
		"mode": "check", "target": "tgt", "fragment": "frag", "begin": "BEGIN", "end": "END",
	})
	if st != spec.StatusFail {
		t.Fatalf("stale check must fail, got %s", st)
	}
}

func TestSpliceRegion_MissingRegionFails(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "frag"), "no markers\n")
	writeFile(t, filepath.Join(dir, "tgt"), "BEGIN\nx\nEND\n")
	st, msg := runMaintenanceVerbIn(dir, "splice-region", map[string]any{
		"target": "tgt", "fragment": "frag", "begin": "BEGIN", "end": "END",
	})
	if st != spec.StatusFail {
		t.Fatalf("fragment without markers must fail, got %s: %s", st, msg)
	}
}

// --- module-pins ---

func TestModulePins_CheckAndSync(t *testing.T) {
	dir := t.TempDir()
	// A local stub module the fixture modules actually IMPORT (so `go mod tidy`
	// keeps the adopted require — the real-world case where the lockstep modules
	// import sdk/spec).
	writeFile(t, filepath.Join(dir, "dep", "go.mod"), "module example.com/dep\n\ngo 1.26\n")
	writeFile(t, filepath.Join(dir, "dep", "dep.go"), "package dep\n\nfunc F() {}\n")
	writeFile(t, filepath.Join(dir, "core", "go.mod"),
		"module core\n\ngo 1.26\n\nrequire github.com/opencharly/sdk v1.2.3\n")
	writeFile(t, filepath.Join(dir, "mods", "m1", "go.mod"),
		"module m1\n\ngo 1.26\n\nrequire github.com/opencharly/sdk v1.0.0\n\nreplace github.com/opencharly/sdk => ../../dep\n")
	writeFile(t, filepath.Join(dir, "mods", "m1", "m1.go"),
		"package m1\n\nimport _ \"github.com/opencharly/sdk\"\n")

	// check reports the stale pin.
	st, msg := runMaintenanceVerbIn(dir, "module-pins", map[string]any{
		"mode": "check", "source_go_mod": "core/go.mod", "glob": "mods/*",
		"keys": []any{"github.com/opencharly/sdk"},
	})
	if st != spec.StatusFail || !strings.Contains(msg, "stale") {
		t.Fatalf("stale pin must fail, got %s: %s", st, msg)
	}

	// sync adopts the source pin and tidies (the module imports the dep, so tidy
	// keeps it).
	st, msg = runMaintenanceVerbIn(dir, "module-pins", map[string]any{
		"source_go_mod": "core/go.mod", "glob": "mods/*",
		"keys": []any{"github.com/opencharly/sdk"},
	})
	if st != spec.StatusPass {
		t.Fatalf("sync: %s: %s", st, msg)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "mods", "m1", "go.mod"))
	if !strings.Contains(string(got), "github.com/opencharly/sdk v1.2.3") {
		t.Fatalf("sync did not adopt the pin:\n%s", got)
	}
}

func TestModulePins_MissingSourceKeyFails(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "core", "go.mod"), "module core\n\ngo 1.26\n")
	st, _ := runMaintenanceVerbIn(dir, "module-pins", map[string]any{
		"source_go_mod": "core/go.mod", "glob": "mods/*",
		"keys": []any{"github.com/opencharly/sdk"},
	})
	if st != spec.StatusFail {
		t.Fatalf("missing source pin must fail, got %s", st)
	}
}

// TestRequireVersion proves the go.mod require reader.
func TestRequireVersion(t *testing.T) {
	gm := "module x\n\ngo 1.26\n\nrequire (\n\tgithub.com/opencharly/sdk v1.2.3\n\tgithub.com/opencharly/spec v4.5.6\n)\n"
	if v := requireVersion(gm, "github.com/opencharly/sdk"); v != "v1.2.3" {
		t.Fatalf("sdk = %q", v)
	}
	if v := requireVersion(gm, "github.com/opencharly/spec"); v != "v4.5.6" {
		t.Fatalf("spec = %q", v)
	}
	if v := requireVersion(gm, "github.com/absent"); v != "" {
		t.Fatalf("absent = %q, want empty", v)
	}
}
