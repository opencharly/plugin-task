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

// TestGitSubmodules_StatusUninitializedMarker proves the status arm added for
// charly#768: a present-but-UNINITIALIZED submodule must be reported as
// `- (uninitialized)`, NOT via git's walk-up to the superproject. This test FAILS on
// the pre-fix code, which ran `git -C <empty-dir> rev-parse --short HEAD` — for an
// empty dir that walks up and prints the UMBRELLA's own HEAD as the submodule's pin.
func TestGitSubmodules_StatusUninitializedMarker(t *testing.T) {
	dir := t.TempDir()
	run := gitRepo(t, dir)
	writeFile(t, filepath.Join(dir, ".gitmodules"),
		"[submodule \"target\"]\n\tpath = target\n\turl = https://example.invalid/target.git\n\tbranch = main\n")
	writeFile(t, filepath.Join(dir, "README"), "x")
	run("add", "-A")
	run("commit", "-qm", "init")
	// Record the gitlink for `target`, then leave its DIRECTORY present but EMPTY
	// (no `.git`) — the exact post-`git worktree add` state.
	run("update-index", "--add", "--cacheinfo", "160000,"+run("rev-parse", "HEAD")+",target")
	run("commit", "-qm", "gitlink")
	umbShort := run("rev-parse", "--short", "HEAD")
	if err := os.MkdirAll(filepath.Join(dir, "target"), 0o755); err != nil {
		t.Fatal(err)
	}

	st, msg := runMaintenanceVerbIn(dir, "git-submodules", map[string]any{"mode": "status"})
	if st != spec.StatusPass {
		t.Fatalf("status: %s: %s", st, msg)
	}
	if !strings.Contains(msg, "- (uninitialized)") {
		t.Fatalf("an uninitialized submodule must be marked '- (uninitialized)', got:\n%s", msg)
	}
	if strings.Contains(msg, umbShort) {
		t.Fatalf("status printed the UMBRELLA's HEAD (%s) as a submodule pin — the walk-up regression:\n%s", umbShort, msg)
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

// TestGitSubmodules_VerifyCheckoutAudit pins the clause the doc comment promised
// but the code never performed (plugin-task#10): for every `.gitmodules` path the
// checkout must be CLEAN and its HEAD must BE the recorded gitlink, read
// walk-up-safely through submoduleAt (charly#768); an uninitialized path is
// CLASSIFIED (named in the Message), never silently skipped and never counted as a
// clean checkout — but its PIN is still audited, because the index gitlink read
// needs no checkout, so a declared path with NO recorded gitlink FAILS rather than
// passing as "uninitialized (pin audited)". This test FAILS on the pre-fix code,
// whose `verify` arm compared only pin_map gitlinks and never read a submodule's
// worktree.
func TestGitSubmodules_VerifyCheckoutAudit(t *testing.T) {
	dir := t.TempDir()
	run := gitRepo(t, dir)

	// A real nested checkout at `sub`, sitting at its own commit.
	subDir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subRun := gitRepo(t, subDir)
	writeFile(t, filepath.Join(subDir, "README"), "hello\n")
	subRun("add", "-A")
	subRun("commit", "-qm", "sub init")
	subSHA := subRun("rev-parse", "HEAD")

	writeFile(t, filepath.Join(dir, ".gitmodules"),
		"[submodule \"sub\"]\n\tpath = sub\n\turl = u\n\tbranch = main\n"+
			"[submodule \"ghostmod\"]\n\tpath = ghostmod\n\turl = u\n\tbranch = main\n")
	run("add", ".gitmodules")
	// Record both gitlinks directly (no network / no nested clone): `sub` HAS a
	// checkout, `ghostmod` does NOT (its directory is never created — the empty-dir
	// state a fresh `git worktree add` leaves).
	writeGitlink := func(path, sha string) {
		t.Helper()
		cmd := exec.Command("git", "-C", dir, "update-index", "--add", "--cacheinfo", "160000,"+sha+","+path)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("update-index %s: %v\n%s", path, err, out)
		}
	}
	writeGitlink("sub", subSHA)
	writeGitlink("ghostmod", subSHA)
	run("commit", "-qm", "gitlinks")

	// PASS: `sub` is clean and at its gitlink; `ghostmod` is uninitialized and must
	// be CLASSIFIED in the Message (the channel that survives the pass path).
	st, msg := runMaintenanceVerbIn(dir, "git-submodules", map[string]any{"mode": "verify"})
	if st != spec.StatusPass {
		t.Fatalf("a clean checkout at its gitlink must pass: %s: %s", st, msg)
	}
	if !strings.Contains(msg, "1 checkout(s) clean at their gitlink") {
		t.Fatalf("the Message must report checkout coverage, got: %q", msg)
	}
	if !strings.Contains(msg, "uninitialized") || !strings.Contains(msg, "ghostmod") {
		t.Fatalf("an uninitialized path must be classified (named) in the Message, got: %q", msg)
	}

	// FAIL: a DIRTY checkout must fail, naming the path.
	writeFile(t, filepath.Join(subDir, "README"), "dirty\n")
	st, msg = runMaintenanceVerbIn(dir, "git-submodules", map[string]any{"mode": "verify"})
	if st != spec.StatusFail {
		t.Fatalf("a dirty checkout must fail, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "sub: dirty working tree") {
		t.Fatalf("the failure must name the dirty path, got: %q", msg)
	}

	// FAIL: a checkout whose HEAD drifted off its gitlink must fail, naming both.
	subRun("checkout", "--", "README")
	subRun("commit", "-q", "--allow-empty", "-m", "drift")
	drift := subRun("rev-parse", "HEAD")
	st, msg = runMaintenanceVerbIn(dir, "git-submodules", map[string]any{"mode": "verify"})
	if st != spec.StatusFail {
		t.Fatalf("a checkout off its gitlink must fail, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "sub: HEAD "+drift+" != gitlink "+subSHA) {
		t.Fatalf("the failure must name the drift, got: %q", msg)
	}

	// FAIL: a path DECLARED in `.gitmodules` with NO recorded gitlink must fail —
	// including when it has no checkout. `git ls-files -s` is an index read that
	// needs no worktree, so "uninitialized" must never become a way for an
	// unaudited pin to pass. `sub` is put back at its gitlink first, so this is the
	// only failure in the run.
	subRun("checkout", "-q", subSHA)
	writeFile(t, filepath.Join(dir, ".gitmodules"),
		"[submodule \"sub\"]\n\tpath = sub\n\turl = u\n\tbranch = main\n"+
			"[submodule \"ghostmod\"]\n\tpath = ghostmod\n\turl = u\n\tbranch = main\n"+
			"[submodule \"nopin\"]\n\tpath = nopin\n\turl = u\n\tbranch = main\n")
	run("add", ".gitmodules")
	run("commit", "-qm", "declare nopin with no gitlink")
	st, msg = runMaintenanceVerbIn(dir, "git-submodules", map[string]any{"mode": "verify"})
	if st != spec.StatusFail {
		t.Fatalf("a declared path with no recorded gitlink must fail, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "nopin: no gitlink recorded in the index") {
		t.Fatalf("the failure must name the unrecorded path, got: %q", msg)
	}
	if strings.Contains(msg, "uninitialized") {
		t.Fatalf("a path with no recorded gitlink must never be reported as an audited pin, got: %q", msg)
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

// TestRequireVersion proves the go.mod require reader handles BOTH the block form
// and the single-line `require M vX` form (the form the live gate's fixtures use).
func TestRequireVersion(t *testing.T) {
	block := "module x\n\ngo 1.26\n\nrequire (\n\tgithub.com/opencharly/sdk v1.2.3\n\tgithub.com/opencharly/spec v4.5.6\n)\n"
	if v := requireVersion(block, "github.com/opencharly/sdk"); v != "v1.2.3" {
		t.Fatalf("block sdk = %q", v)
	}
	if v := requireVersion(block, "github.com/opencharly/spec"); v != "v4.5.6" {
		t.Fatalf("block spec = %q", v)
	}
	if v := requireVersion(block, "github.com/absent"); v != "" {
		t.Fatalf("block absent = %q, want empty", v)
	}
	single := "module x\n\ngo 1.26\n\nrequire github.com/opencharly/sdk v1.2.3\n"
	if v := requireVersion(single, "github.com/opencharly/sdk"); v != "v1.2.3" {
		t.Fatalf("single-line sdk = %q", v)
	}
	if v := requireVersion(single, "github.com/absent"); v != "" {
		t.Fatalf("single-line absent = %q, want empty", v)
	}
}

// TestGitSubmodules_BumpOrder pins the LOAD-BEARING bump order with REAL submodules:
// the pinned_from repo (`src`) is itself a ROLLING submodule, STALE in the umbrella,
// and its `inner` gitlink ADVANCES during the bump. The pin_map entry (`target`) must
// then be pinned from src's NEW gitlink. The reverse order pins target from src's
// OLD gitlink then advances src, leaving policy B violated.
func TestGitSubmodules_BumpOrder(t *testing.T) {
	base := t.TempDir()
	mustGit := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	initRepo := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		mustGit(dir, "init", "-q", "-b", "main")
	}
	commitFile := func(dir, name, content string) {
		t.Helper()
		writeFile(t, filepath.Join(dir, name), content)
		mustGit(dir, "add", name)
		mustGit(dir, "commit", "-qm", name)
	}
	stageGitlink := func(dir, path, sha, msg string) {
		t.Helper()
		cmd := exec.Command("git", "-C", dir, "update-index", "--add", "--cacheinfo", "160000,"+sha+","+path)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("update-index in %s: %v\n%s", dir, err, out)
		}
		mustGit(dir, "commit", "-qm", msg)
	}

	// leaf: two REAL commits L1, L2 (target is switched between them).
	leaf := filepath.Join(base, "leaf")
	initRepo(leaf)
	commitFile(leaf, "leaf.txt", "L1")
	L1 := mustGit(leaf, "rev-parse", "HEAD")
	commitFile(leaf, "leaf.txt", "L2")
	L2 := mustGit(leaf, "rev-parse", "HEAD")

	// src-origin: its `inner` gitlink names L1 then L2 (synthetic gitlinks — only ever
	// READ via ls-tree, never switched).
	srcOrigin := filepath.Join(base, "src-origin")
	initRepo(srcOrigin)
	writeFile(t, filepath.Join(srcOrigin, ".gitmodules"),
		"[submodule \"inner\"]\n\tpath = inner\n\turl = "+leaf+"\n\tbranch = main\n")
	mustGit(srcOrigin, "add", ".gitmodules")
	mustGit(srcOrigin, "commit", "-qm", "gitmodules")
	stageGitlink(srcOrigin, "inner", L1, "inner=L1")
	A := mustGit(srcOrigin, "rev-parse", "HEAD")
	stageGitlink(srcOrigin, "inner", L2, "inner=L2")
	B := mustGit(srcOrigin, "rev-parse", "HEAD")
	srcBare := filepath.Join(base, "src.git")
	mustGit(base, "clone", "-q", "--bare", srcOrigin, srcBare)
	// src checkout at B (the post-roll state bump will reach).
	src := filepath.Join(base, "src")
	mustGit(base, "clone", "-q", srcBare, src)
	mustGit(src, "checkout", "-q", B)

	// umbrella: real submodules src (STALE at A) and target (real, STALE at L1).
	umb := filepath.Join(base, "umb")
	initRepo(umb)
	commitFile(umb, "u.txt", "u")
	mustGit(umb, "-c", "protocol.file.allow=always", "submodule", "add", "-q", srcBare, "src")
	mustGit(umb, "-c", "protocol.file.allow=always", "submodule", "add", "-q", leaf, "target")
	mustGit(umb, "config", "-f", ".gitmodules", "submodule.src.branch", "main")
	mustGit(umb, "config", "-f", ".gitmodules", "submodule.target.branch", "main")
	mustGit(umb, "add", ".gitmodules")
	mustGit(umb, "commit", "-qm", "branch=main")
	mustGit(umb, "update-index", "--add", "--cacheinfo", "160000,"+A+",src")
	mustGit(umb, "update-index", "--add", "--cacheinfo", "160000,"+L1+",target")
	mustGit(umb, "commit", "-qm", "stale pins")
	mustGit(umb, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "src", "target")

	in := map[string]any{
		"mode": "bump", "pinned_from": "src", "pin_map": map[string]any{"target": "inner"},
	}
	st, msg := runMaintenanceVerbIn(umb, "git-submodules", in)
	if st != spec.StatusPass {
		t.Fatalf("bump: %s: %s", st, msg)
	}
	if got := gitlink(umb, "src"); got != B {
		t.Fatalf("src did not roll: got %s want %s", got, B)
	}
	got := gitlink(umb, "target")
	if got != L2 {
		t.Fatalf("target pinned to %s, want L2=%s (L1=%s means it read stale src)", got, L2, L1)
	}
}

// TestGitSubmodules_BumpUninitializedFailsLoud proves charly#768's fix: a submodule
// directory that is present-but-UNINITIALIZED (no `.git`) must make `bump` FAIL LOUD
// and must NOT be operated on through git's walk-up to the superproject. Before the
// fix, `git -C <empty-submodule-dir> switch --detach` switched the UMBRELLA.
func TestGitSubmodules_BumpUninitializedFailsLoud(t *testing.T) {
	base := t.TempDir()

	// A leaf submodule SOURCE with two commits (so there is a real ref to switch to).
	leaf := filepath.Join(base, "leaf")
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	mustGitMay := func(dir string, args ...string) (string, bool) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err == nil
	}
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	run(leaf, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(leaf, "f"), "L1")
	run(leaf, "add", "-A")
	run(leaf, "commit", "-qm", "L1")
	leafBare := filepath.Join(base, "leaf.git")
	run(base, "clone", "-q", "--bare", leaf, leafBare)

	// umb declares the submodule `target` in .gitmodules but NEVER initializes it:
	// the directory exists and is EMPTY (the exact post-`git worktree add` state).
	umb := filepath.Join(base, "umb")
	if err := os.MkdirAll(umb, 0o755); err != nil {
		t.Fatal(err)
	}
	run(umb, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(umb, "u"), "u")
	run(umb, "add", "-A")
	run(umb, "commit", "-qm", "u")
	writeFile(t, filepath.Join(umb, ".gitmodules"),
		"[submodule \"target\"]\n\tpath = target\n\turl = "+leafBare+"\n\tbranch = main\n")
	run(umb, "add", ".gitmodules")
	run(umb, "commit", "-qm", "gitmodules")
	// Stage a bogus gitlink for `target` and create the EMPTY dir (no .git).
	head := run(umb, "rev-parse", "HEAD")
	if out, ok := mustGitMay(umb, "update-index", "--add", "--cacheinfo", "160000,"+head+",target"); !ok {
		t.Fatalf("update-index: %s", out)
	}
	run(umb, "commit", "-qm", "staged gitlink")
	if err := os.MkdirAll(filepath.Join(umb, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(umb, "target", ".git")); err == nil {
		t.Fatalf("fixture precondition: target must have no .git")
	}
	// Give the UMBRELLA an `origin` with an `origin/main` ref, so that pre-fix the
	// `git -C target switch --detach origin/main` walk-up would GENUINELY switch the
	// umbrella (the real charly#768 regression) rather than erroring out.
	umbOrigin := filepath.Join(base, "umb-origin.git")
	run(base, "clone", "-q", "--bare", umb, umbOrigin)
	run(umb, "remote", "add", "origin", umbOrigin)
	run(umb, "fetch", "-q", "origin")
	run(umb, "branch", "--set-upstream-to=origin/main", "main")

	// Record the UMBRELLA's own HEAD+branch BEFORE the bump, to prove it is untouched.
	umbHeadBefore := run(umb, "rev-parse", "HEAD")
	umbBranchBefore := run(umb, "rev-parse", "--abbrev-ref", "HEAD")

	// bump over the WHOLE module set (no pin_map) — `target` rolls to origin/main.
	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{"mode": "bump"})
	if st != spec.StatusFail {
		t.Fatalf("bump over an uninitialized submodule must FAIL LOUD, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "not an initialized checkout") {
		t.Fatalf("failure must name the uninitialized checkout, got: %s", msg)
	}

	// THE REGRESSION ASSERTION: the umbrella must be EXACTLY where it was — same
	// HEAD, same branch. Pre-fix, `git -C target switch --detach origin/main` walked
	// up and detached the umbrella.
	if got := run(umb, "rev-parse", "HEAD"); got != umbHeadBefore {
		t.Fatalf("umbrella HEAD moved: %s -> %s", umbHeadBefore, got)
	}
	if got := run(umb, "rev-parse", "--abbrev-ref", "HEAD"); got != umbBranchBefore {
		t.Fatalf("umbrella branch changed: %q -> %q (the walk-up detach regression)", umbBranchBefore, got)
	}
}

func TestGitSubmodules_BumpReportsUninitializedOnce(t *testing.T) {
	base := t.TempDir()

	// A leaf submodule SOURCE with two commits (so there is a real ref to switch to).
	leaf := filepath.Join(base, "leaf")
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	mustGitMay := func(dir string, args ...string) (string, bool) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err == nil
	}
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	run(leaf, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(leaf, "f"), "L1")
	run(leaf, "add", "-A")
	run(leaf, "commit", "-qm", "L1")
	leafBare := filepath.Join(base, "leaf.git")
	run(base, "clone", "-q", "--bare", leaf, leafBare)

	// umb declares the submodule `target` in .gitmodules but NEVER initializes it:
	// the directory exists and is EMPTY (the exact post-`git worktree add` state).
	umb := filepath.Join(base, "umb")
	if err := os.MkdirAll(umb, 0o755); err != nil {
		t.Fatal(err)
	}
	run(umb, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(umb, "u"), "u")
	run(umb, "add", "-A")
	run(umb, "commit", "-qm", "u")
	writeFile(t, filepath.Join(umb, ".gitmodules"),
		"[submodule \"target\"]\n\tpath = target\n\turl = "+leafBare+"\n\tbranch = main\n")
	run(umb, "add", ".gitmodules")
	run(umb, "commit", "-qm", "gitmodules")
	// Stage a bogus gitlink for `target` and create the EMPTY dir (no .git).
	head := run(umb, "rev-parse", "HEAD")
	if out, ok := mustGitMay(umb, "update-index", "--add", "--cacheinfo", "160000,"+head+",target"); !ok {
		t.Fatalf("update-index: %s", out)
	}
	run(umb, "commit", "-qm", "staged gitlink")
	if err := os.MkdirAll(filepath.Join(umb, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(umb, "target", ".git")); err == nil {
		t.Fatalf("fixture precondition: target must have no .git")
	}
	// Give the UMBRELLA an `origin` with an `origin/main` ref, so that pre-fix the
	// `git -C target switch --detach origin/main` walk-up would GENUINELY switch the
	// umbrella (the real charly#768 regression) rather than erroring out.
	umbOrigin := filepath.Join(base, "umb-origin.git")
	run(base, "clone", "-q", "--bare", umb, umbOrigin)
	run(umb, "remote", "add", "origin", umbOrigin)
	run(umb, "fetch", "-q", "origin")
	run(umb, "branch", "--set-upstream-to=origin/main", "main")

	// Record the UMBRELLA's own HEAD+branch BEFORE the bump, to prove it is untouched.
	umbHeadBefore := run(umb, "rev-parse", "HEAD")
	umbBranchBefore := run(umb, "rev-parse", "--abbrev-ref", "HEAD")

	// bump over the WHOLE module set (no pin_map) — `target` rolls to origin/main.
	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{"mode": "bump"})
	if st != spec.StatusFail {
		t.Fatalf("bump over an uninitialized submodule must FAIL LOUD, got %s: %s", st, msg)
	}
	// THE COLLAPSE, asserted so this test FAILS on the pre-change tree: there the message was
	// "bumped ...; 1 FAILED: submodule %q is not an initialized checkout" - a per-path failure list.
	// The phrase below ("N submodule(s) not an initialized checkout (name)") is new, and the FAILED
	// list is gone, so neither assertion can pass before this change.
	if !strings.Contains(msg, "1 submodule(s) not an initialized checkout") {
		t.Fatalf("the count must be reported once, in the new form; got: %s", msg)
	}
	if !strings.Contains(msg, "(target)") {
		t.Fatalf("the report must NAME the uninitialized paths; got: %s", msg)
	}
	if strings.Contains(msg, "FAILED") {
		t.Fatalf("the per-path failure list must be gone (one report, not N failures); got: %s", msg)
	}
	if n := strings.Count(msg, "not an initialized checkout"); n != 1 {
		t.Fatalf("the condition must be reported exactly ONCE, got %d occurrences: %s", n, msg)
	}

	// THE REGRESSION ASSERTION: the umbrella must be EXACTLY where it was — same
	// HEAD, same branch. Pre-fix, `git -C target switch --detach origin/main` walked
	// up and detached the umbrella.
	if got := run(umb, "rev-parse", "HEAD"); got != umbHeadBefore {
		t.Fatalf("umbrella HEAD moved: %s -> %s", umbHeadBefore, got)
	}
	if got := run(umb, "rev-parse", "--abbrev-ref", "HEAD"); got != umbBranchBefore {
		t.Fatalf("umbrella branch changed: %q -> %q (the walk-up detach regression)", umbBranchBefore, got)
	}
	// The gating: the SAME tree in `status` mode must still succeed and still carry its marker -
	// the injection is scoped to bump, and status never consults `skipped`.
	stStatus, msgStatus := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{"mode": "status"})
	if stStatus == spec.StatusFail {
		t.Fatalf("status must not fail on an uninitialized gitlink; got: %s", msgStatus)
	}
	if !strings.Contains(msgStatus, "uninitialized") {
		t.Fatalf("status must still mark the uninitialized gitlink; got: %s", msgStatus)
	}
}

// TestSubmoduleAt proves the guard's contract directly: an initialized submodule
// yields a walk-up-safe --git-dir/--work-tree pair; a bare/uninitialized dir errors.
func TestSubmoduleAt(t *testing.T) {
	base := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	sub := filepath.Join(base, "sub")
	run(base, "init", "-q", "-b", "main", "sub")
	writeFile(t, filepath.Join(sub, "s"), "s")
	run(sub, "add", "-A")
	run(sub, "commit", "-qm", "s")
	proj := filepath.Join(base, "proj")
	run(base, "init", "-q", "-b", "main", "proj")
	writeFile(t, filepath.Join(proj, "p"), "p")
	run(proj, "add", "-A")
	run(proj, "commit", "-qm", "p")
	run(proj, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")

	// Initialized: resolves + yields an absolute git-dir pointing at the submodule.
	abs, gitArgs, err := submoduleAt(proj, "sub")
	if err != nil {
		t.Fatalf("initialized submodule must resolve: %v", err)
	}
	if abs != filepath.Join(proj, "sub") {
		t.Fatalf("abs = %q", abs)
	}
	if !strings.Contains(gitArgs, filepath.Join(proj, "sub", ".git")) {
		t.Fatalf("gitArgs must target the submodule's .git: %q", gitArgs)
	}

	// Uninitialized: errors and never yields a walk-up-prone form.
	if err := os.MkdirAll(filepath.Join(proj, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := submoduleAt(proj, "empty"); err == nil {
		t.Fatalf("an uninitialized submodule dir must error")
	}
}

// gitIn runs git in an ALREADY-INITIALIZED dir (a clone target, a bare repo) with a
// hermetic environment — the fixture runner for repos `gitRepo` must not re-init.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// bumpOrigins builds the two REAL bare origins the #13 fixtures use: `leaf` (L1 then
// L2 — the roll target) and `doomed` (the origin a test REMOVES, so a submodule's
// `git fetch origin` fails for real, with no mock and no sleep).
func bumpOrigins(t *testing.T) (base, leafBare, doomedBare, L1, L2 string) {
	t.Helper()
	base = t.TempDir()
	leaf := filepath.Join(base, "leaf")
	gitIn(t, base, "init", "-q", "-b", "main", "leaf")
	writeFile(t, filepath.Join(leaf, "f"), "L1")
	gitIn(t, leaf, "add", "-A")
	gitIn(t, leaf, "commit", "-qm", "L1")
	L1 = gitIn(t, leaf, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(leaf, "f"), "L2")
	gitIn(t, leaf, "add", "-A")
	gitIn(t, leaf, "commit", "-qm", "L2")
	L2 = gitIn(t, leaf, "rev-parse", "HEAD")
	leafBare = filepath.Join(base, "leaf.git")
	gitIn(t, base, "clone", "-q", "--bare", leaf, leafBare)

	doomed := filepath.Join(base, "doomed-src")
	gitIn(t, base, "init", "-q", "-b", "main", "doomed-src")
	writeFile(t, filepath.Join(doomed, "d"), "d")
	gitIn(t, doomed, "add", "-A")
	gitIn(t, doomed, "commit", "-qm", "d")
	doomedBare = filepath.Join(base, "doomed.git")
	gitIn(t, base, "clone", "-q", "--bare", doomed, doomedBare)
	return base, leafBare, doomedBare, L1, L2
}

// newBumpUmbrella builds a REAL umbrella declaring the named submodules (each with
// branch=main), pins every submodule that is not `doomed`/`src` to pinSHA, and
// initializes them all.
func newBumpUmbrella(t *testing.T, base, pinSHA, leafBare, doomedBare string, names ...string) string {
	t.Helper()
	umb := filepath.Join(base, "umb-"+strings.Join(names, "-"))
	gitIn(t, base, "init", "-q", "-b", "main", filepath.Base(umb))
	writeFile(t, filepath.Join(umb, "u"), "u")
	gitIn(t, umb, "add", "-A")
	gitIn(t, umb, "commit", "-qm", "u")
	for _, n := range names {
		src := leafBare
		if n == "doomed" || n == "src" {
			src = doomedBare
		}
		gitIn(t, umb, "-c", "protocol.file.allow=always", "submodule", "add", "-q", src, n)
		gitIn(t, umb, "config", "-f", ".gitmodules", "submodule."+n+".branch", "main")
	}
	gitIn(t, umb, "add", ".gitmodules")
	gitIn(t, umb, "commit", "-qm", "branch=main")
	if pinSHA != "" {
		for _, n := range names {
			if n == "doomed" || n == "src" {
				continue
			}
			gitIn(t, umb, "update-index", "--add", "--cacheinfo", "160000,"+pinSHA+","+n)
		}
		gitIn(t, umb, "commit", "-qm", "stale pins")
	}
	gitIn(t, umb, "-c", "protocol.file.allow=always", "submodule", "update", "--init")
	return umb
}

// TestGitSubmodules_BumpContinuesPastFailure proves plugin-task#13's fix: ONE path's
// per-invocation failure costs that ONE pin, not every pin sorted after it. Before the
// fix the walk RETURNED at the first failure (phase 1 of `bump`), so a single contended
// checkout — a momentarily held superproject `.git/index.lock` failing that path's
// `git add`, the mechanism reproduced in the issue — stranded the whole remainder.
func TestGitSubmodules_BumpContinuesPastFailure(t *testing.T) {
	base, leafBare, doomedBare, L1, L2 := bumpOrigins(t)
	// Path order is SORTED (submodulePaths), so `doomed` is visited FIRST and `zebra`
	// sorts AFTER it — exactly the ordering that used to strand the rest.
	umb := newBumpUmbrella(t, base, L1, leafBare, doomedBare, "doomed", "zebra")
	// The failing path's failure is set up AFTER initialization: its origin is GONE,
	// so `git fetch origin` in that submodule fails for real.
	if err := os.RemoveAll(doomedBare); err != nil {
		t.Fatal(err)
	}

	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{"mode": "bump"})
	if st != spec.StatusFail {
		t.Fatalf("bump with one failing path must FAIL, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "doomed") {
		t.Fatalf("the aggregated failure must NAME the failing path, got: %s", msg)
	}
	// THE ASSERTION: `zebra` sorts AFTER the failing `doomed` and still rolled to L2.
	if got := gitlink(umb, "zebra"); got != L2 {
		t.Fatalf("zebra was stranded by doomed's failure: gitlink=%s want L2=%s (L1=%s)", got, L2, L1)
	}
}

// TestGitSubmodules_BumpRefusesStalePinnedFrom proves the LOAD-BEARING order guard
// survives the continue-on-failure change: when the pinned_from repo does NOT roll, its
// gitlinks are the OLD ones, and pinning a pin_map entry from them is the policy-B
// violation that order exists to prevent. Phase 2 must REFUSE and say so — never
// silently pin from stale data.
func TestGitSubmodules_BumpRefusesStalePinnedFrom(t *testing.T) {
	base, leafBare, doomedBare, L1, _ := bumpOrigins(t)
	umb := newBumpUmbrella(t, base, L1, leafBare, doomedBare, "src", "target")
	if err := os.RemoveAll(doomedBare); err != nil {
		t.Fatal(err)
	}

	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{
		"mode": "bump", "pinned_from": "src", "pin_map": map[string]any{"target": "inner"},
	})
	if st != spec.StatusFail {
		t.Fatalf("a bump whose pinned_from did not roll must FAIL, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "did not roll") {
		t.Fatalf("the refusal must say the pinned_from did not roll, got: %s", msg)
	}
	// THE ASSERTION: target is UNTOUCHED — it was never pinned from stale gitlinks.
	if got := gitlink(umb, "target"); got != L1 {
		t.Fatalf("target pinned from a STALE pinned_from: gitlink=%s want the unchanged L1=%s", got, L1)
	}
}

// TestGitSubmodules_BumpExternalPinnedFrom is the boundary guard for the refusal above:
// a pinned_from that is NOT one of this project's own submodules — the `../src` shape
// scripts/gate-task.sh's pins-bump fixture exercises, and the shape the umbrella's own
// `pins` task uses when the twin lives outside the tree — is NEVER rolled by this walk,
// so it must not be judged "did not roll". Its gitlinks are READ and pinned from, exactly
// as before. This test fails if the staleness refusal is widened to external checkouts,
// which would make every roll of that shape a hard failure.
func TestGitSubmodules_BumpExternalPinnedFrom(t *testing.T) {
	base, leafBare, doomedBare, L1, L2 := bumpOrigins(t)
	// The umbrella declares ONLY `target`; the pinned_from lives BESIDE it.
	umb := newBumpUmbrella(t, base, L1, leafBare, doomedBare, "target")
	// `srcrepo` is a sibling checkout, NOT a submodule of the umbrella. It pins `inner`
	// at L2 — the value `target` must adopt.
	srcDir := filepath.Join(base, "srcrepo")
	gitIn(t, base, "init", "-q", "-b", "main", "srcrepo")
	writeFile(t, filepath.Join(srcDir, "s"), "s")
	gitIn(t, srcDir, "add", "-A")
	gitIn(t, srcDir, "commit", "-qm", "s")
	gitIn(t, srcDir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", leafBare, "inner")
	gitIn(t, srcDir, "update-index", "--add", "--cacheinfo", "160000,"+L2+",inner")
	gitIn(t, srcDir, "commit", "-qm", "inner=L2")

	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{
		"mode": "bump", "pinned_from": "../srcrepo", "pin_map": map[string]any{"target": "inner"},
	})
	if st != spec.StatusPass {
		t.Fatalf("an EXTERNAL pinned_from must still pin its pin_map entries, got %s: %s", st, msg)
	}
	if got := gitlink(umb, "target"); got != L2 {
		t.Fatalf("target not pinned from the external pinned_from: gitlink=%s want L2=%s (L1=%s)", got, L2, L1)
	}
}
