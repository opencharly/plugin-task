package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// TestGitSubmodules_VerifyNamesTheMismatchDirection pins the clause that makes the gate's
// failure message TRUE (opencharly/opencharly#461).
//
// `verify` compares a value it reads from THIS repo's index (the recorded gitlink) against a
// value that belongs to ANOTHER repo (the submodule checkout's HEAD), and those two are
// maintained by different verbs: `charly task sync` materialises an advance and stages it,
// while the sanctioned branch flow (`git switch -c <b> origin/main`, `git reset`,
// `git stash pop`, an aborted commit) rewrites the index and silently un-stages it. The
// pre-fix code reported every mismatch as `HEAD <h> != gitlink <g>` — the same string a
// genuinely broken pin produces — so a session whose pins were CORRECT read the gate as
// telling it its tree was inconsistent, and the one command it needed (`git add`) was never
// named. Every arm below reaches that state and asserts the direction and the remedy; all
// three FAIL on the pre-fix code, which emitted no class at all.
func TestGitSubmodules_VerifyNamesTheMismatchDirection(t *testing.T) {
	dir := t.TempDir()
	run := gitRepo(t, dir)

	// A real nested checkout at `sub`, two commits deep, so there is a PARENT to record in the
	// index while the checkout sits on the CHILD — the un-staged-advance shape exactly.
	subDir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subRun := gitRepo(t, subDir)
	writeFile(t, filepath.Join(subDir, "README"), "one\n")
	subRun("add", "-A")
	subRun("commit", "-qm", "one")
	parent := subRun("rev-parse", "HEAD")
	writeFile(t, filepath.Join(subDir, "README"), "two\n")
	subRun("add", "-A")
	subRun("commit", "-qm", "two")
	child := subRun("rev-parse", "HEAD")

	writeFile(t, filepath.Join(dir, ".gitmodules"),
		"[submodule \"sub\"]\n\tpath = sub\n\turl = u\n\tbranch = main\n")
	run("add", ".gitmodules")

	// Record a gitlink directly (no network, no nested clone), the same way the checkout-audit
	// test does — `--cacheinfo` needs no worktree for the path.
	recordGitlink := func(sha string) {
		t.Helper()
		cmd := exec.Command("git", "-C", dir, "update-index", "--add", "--cacheinfo", "160000,"+sha+",sub")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("update-index %s: %v\n%s", sha, err, out)
		}
	}

	// Arm 1 — the checkout is AHEAD of the index: `charly task sync`'s own advance, after its
	// staging was rewritten. The gate must still FAIL (committing now records the OLD pin) and
	// must say which way AND what to run.
	recordGitlink(parent)
	run("commit", "-qm", "record the parent while the checkout is on the child")
	st, msg := runMaintenanceVerbIn(dir, "git-submodules", map[string]any{"mode": "verify"})
	if st != spec.StatusFail {
		t.Fatalf("an index behind its checkout must fail — a commit would record the OLD pin, got %s: %s", st, msg)
	}
	for _, want := range []string{
		"unstaged advance", "git add sub", child, parent, "1 failure(s) (1 unstaged advance)",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the failure must name the direction and the remedy; missing %q in:\n%s", want, msg)
		}
	}
	// The directions are mutually exclusive, and a wrong one is worse than none: naming a
	// correct advance as a divergence is the defect itself, re-created in prose.
	if strings.Contains(msg, "off its recorded gitlink") || strings.Contains(msg, "checkout behind the index") {
		t.Fatalf("an un-staged advance must not be reported as another class:\n%s", msg)
	}

	// Arm 2 — the checkout is BEHIND the index: the gitlink was recorded without its checkout
	// (the classic "checked out a newer umbrella commit, forgot git submodule update"). The
	// opposite remedy, so it must be named as such rather than as the arm-1 class.
	recordGitlink(child)
	subRun("checkout", "-q", parent)
	st, msg = runMaintenanceVerbIn(dir, "git-submodules", map[string]any{"mode": "verify"})
	if st != spec.StatusFail {
		t.Fatalf("a checkout behind the index must fail, got %s: %s", st, msg)
	}
	for _, want := range []string{"checkout behind the index", "git submodule update sub", child, parent} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the reverse direction must be named with its own remedy; missing %q in:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "unstaged advance") {
		t.Fatalf("a checkout behind the index must not be reported as an un-staged advance:\n%s", msg)
	}

	// Arm 3 — OFF ITS RECORDED GITLINK: a PRESENT but unrelated commit is recorded. The object
	// exists locally, so this is a real divergence rather than an unreadable pin, and the report
	// must neither claim one of the two directions nor invent a cause it cannot see.
	subRun("checkout", "-q", "--orphan", "other")
	subRun("commit", "-q", "--allow-empty", "-m", "unrelated root")
	other := subRun("rev-parse", "HEAD")
	subRun("checkout", "-q", child)
	recordGitlink(other)
	st, msg = runMaintenanceVerbIn(dir, "git-submodules", map[string]any{"mode": "verify"})
	if st != spec.StatusFail {
		t.Fatalf("a checkout off its recorded gitlink must fail, got %s: %s", st, msg)
	}
	for _, want := range []string{"off its recorded gitlink", "fetch origin", other, child} {
		if !strings.Contains(msg, want) {
			t.Fatalf("a divergence must be named as one, with a remedy; missing %q in:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "unstaged advance") || strings.Contains(msg, "checkout behind the index") {
		t.Fatalf("an unrelated pin must not be reported as a direction it does not have:\n%s", msg)
	}
}
