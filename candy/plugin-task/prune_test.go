package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// prune_test.go — the verb:prune unit suite. It replaces the ghPRSource seam with
// a fixture so no network/auth is needed, and builds real git repos + linked
// worktrees so the ancestry + containment logic runs against real objects.

// pruneGit runs git in dir with a hermetic env, failing the test on error.
func pruneGit(t *testing.T, dir string, args ...string) string {
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

// pruneGitMay runs git, returning output + ok (no fatal) — for expected-failure probes.
func pruneGitMay(dir string, args ...string) (string, bool) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

// newPruneProject builds a project repo with origin/main on branch `main`, plus
// three branches:
//   - feat/merged: committed onto main (ancestor of origin/main) — prunable
//   - feat/squashed: its own commit NOT on main, but a MERGED PR head == its tip
//     (the squash-merge case git ancestry cannot see) — prunable via gh
//   - feat/wip: its own commit, no PR, beyond main — NEVER prunable
//
// It also creates two linked worktrees under .worktrees/: one on feat/merged
// (prunable), one on feat/wip (kept). Returns the project dir.
func newPruneProject(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	pruneGit(t, base, "init", "-q", "-b", "main", "proj")
	writeFile(t, filepath.Join(proj, "f"), "base")
	pruneGit(t, proj, "add", "-A")
	pruneGit(t, proj, "commit", "-qm", "base")
	// A bare "origin" so origin/main resolves.
	origin := filepath.Join(base, "origin.git")
	pruneGit(t, base, "init", "-q", "--bare", "origin.git")
	pruneGit(t, proj, "remote", "add", "origin", origin)
	pruneGit(t, proj, "push", "-q", "origin", "main")
	pruneGit(t, proj, "fetch", "-q", "origin")
	// origin/HEAD -> origin/main
	pruneGit(t, proj, "remote", "set-head", "origin", "main")

	mergedTip := pruneGit(t, proj, "rev-parse", "main")

	// feat/merged: branched and fast-forwarded onto main (tip == main, ancestor).
	pruneGit(t, proj, "branch", "feat/merged", "main")

	// feat/squashed: a commit NOT on main; its PR head == the branch tip.
	pruneGit(t, proj, "switch", "-q", "-c", "feat/squashed")
	writeFile(t, filepath.Join(proj, "s"), "squash work")
	pruneGit(t, proj, "add", "-A")
	pruneGit(t, proj, "commit", "-qm", "squashed work")
	squashedTip := pruneGit(t, proj, "rev-parse", "HEAD")
	pruneGit(t, proj, "switch", "-q", "main")

	// feat/wip: a commit NOT on main, no PR — must never be pruned.
	pruneGit(t, proj, "switch", "-q", "-c", "feat/wip")
	writeFile(t, filepath.Join(proj, "w"), "wip")
	pruneGit(t, proj, "add", "-A")
	pruneGit(t, proj, "commit", "-qm", "wip work")
	pruneGit(t, proj, "switch", "-q", "main")

	// Linked worktrees under .worktrees/<slug>/proj/.
	wtMerged := filepath.Join(proj, ".worktrees", "aa-merged", "proj")
	pruneGit(t, proj, "worktree", "add", "-q", wtMerged, "feat/merged")
	wtWip := filepath.Join(proj, ".worktrees", "bb-wip", "proj")
	pruneGit(t, proj, "worktree", "add", "-q", wtWip, "feat/wip")

	// Fixture PR state, keyed by head branch.
	old := ghPRSource
	ghPRSource = func(string) (map[string][]prInfo, error) {
		return map[string][]prInfo{
			"feat/squashed": {{Number: 7, State: "MERGED", HeadRef: "feat/squashed", HeadRefOID: squashedTip}},
		}, nil
	}
	t.Cleanup(func() { ghPRSource = old })
	_ = mergedTip
	return proj
}

// TestPrune_ReportPrunesOnlyMerged proves the report lists exactly the ancestry-
// merged worktree/branch + the gh-squash-merged branch, and NEVER the wip set.
func TestPrune_ReportPrunesOnlyMerged(t *testing.T) {
	proj := newPruneProject(t)
	st, msg := runMaintenanceVerbIn(proj, "prune", map[string]any{"mode": "report"})
	if st != spec.StatusPass {
		t.Fatalf("report status: %s: %s", st, msg)
	}
	// feat/squashed must be pruned via the gh MERGED head.
	if !strings.Contains(msg, "feat/squashed") {
		t.Fatalf("squash-merged branch must be reported prunable:\n%s", msg)
	}
	// feat/wip must NOT appear as a prunable target.
	for _, line := range strings.Split(msg, "\n") {
		if strings.Contains(line, "feat/wip") && !strings.Contains(line, "skipped") {
			t.Fatalf("unmerged branch feat/wip must never be prunable:\n%s", line)
		}
	}
}

// TestPrune_PruneRemovesMergedKeepsWip proves the live mode removes the merged
// worktree + branches and leaves feat/wip (and its worktree) intact.
func TestPrune_PruneRemovesMergedKeepsWip(t *testing.T) {
	proj := newPruneProject(t)
	st, msg := runMaintenanceVerbIn(proj, "prune", map[string]any{"mode": "prune"})
	if st != spec.StatusPass {
		t.Fatalf("prune status: %s: %s", st, msg)
	}
	// feat/wip branch + its worktree must survive.
	if _, ok := pruneGitMay(proj, "rev-parse", "-q", "--verify", "refs/heads/feat/wip"); !ok {
		t.Fatalf("feat/wip was deleted — unmerged work lost:\n%s", msg)
	}
	if _, ok := pruneGitMay(proj, "rev-parse", "-q", "--verify", "refs/heads/feat/merged"); ok {
		t.Fatalf("feat/merged should have been deleted:\n%s", msg)
	}
	if _, ok := pruneGitMay(proj, "rev-parse", "-q", "--verify", "refs/heads/feat/squashed"); ok {
		t.Fatalf("feat/squashed should have been deleted:\n%s", msg)
	}
	// The merged worktree dir must be gone; the wip worktree must remain.
	if _, err := os.Stat(filepath.Join(proj, ".worktrees", "aa-merged", "proj")); err == nil {
		t.Fatalf("merged worktree not removed:\n%s", msg)
	}
	if _, err := os.Stat(filepath.Join(proj, ".worktrees", "bb-wip", "proj", ".git")); err != nil {
		t.Fatalf("wip worktree must remain: %v", err)
	}
}

// TestPrune_LocalOnlyMissesSquash proves local_only prunes strictly LESS: the
// squash-merged branch (no ancestry) is NOT pruned without the gh layer.
func TestPrune_LocalOnlyMissesSquash(t *testing.T) {
	proj := newPruneProject(t)
	st, msg := runMaintenanceVerbIn(proj, "prune", map[string]any{"mode": "prune", "local_only": true})
	if st != spec.StatusPass {
		t.Fatalf("status: %s: %s", st, msg)
	}
	if _, ok := pruneGitMay(proj, "rev-parse", "-q", "--verify", "refs/heads/feat/squashed"); !ok {
		t.Fatalf("local_only must NOT prune the squash-merged branch:\n%s", msg)
	}
	// The ancestry-merged branch is still pruned (no network needed).
	if _, ok := pruneGitMay(proj, "rev-parse", "-q", "--verify", "refs/heads/feat/merged"); ok {
		t.Fatalf("ancestry-merged branch should be pruned even local_only:\n%s", msg)
	}
}

// TestPrune_GhUnavailableIsConservative proves a GitHub failure prunes STRICTLY
// LESS (never more): the squash-merged branch survives when gh is unavailable.
func TestPrune_GhUnavailableIsConservative(t *testing.T) {
	proj := newPruneProject(t)
	old := ghPRSource
	ghPRSource = func(string) (map[string][]prInfo, error) {
		return nil, os.ErrNotExist // simulates no gh / no auth / offline
	}
	t.Cleanup(func() { ghPRSource = old })
	st, msg := runMaintenanceVerbIn(proj, "prune", map[string]any{"mode": "prune"})
	if st != spec.StatusPass {
		t.Fatalf("status: %s: %s", st, msg)
	}
	if _, ok := pruneGitMay(proj, "rev-parse", "-q", "--verify", "refs/heads/feat/squashed"); !ok {
		t.Fatalf("gh-unavailable must not prune the squash-merged branch:\n%s", msg)
	}
}

// TestPrune_BeyondMergedHeadKept proves a branch whose PR is MERGED but which
// carries commits BEYOND the merged head (unmerged local work) is NEVER pruned.
func TestPrune_BeyondMergedHeadKept(t *testing.T) {
	proj := newPruneProject(t)
	// feat/squashed now gains a NEW commit beyond the merged head recorded in the fixture.
	pruneGit(t, proj, "switch", "-q", "feat/squashed")
	writeFile(t, filepath.Join(proj, "s2"), "post-merge local work")
	pruneGit(t, proj, "add", "-A")
	pruneGit(t, proj, "commit", "-qm", "local-only work beyond the merged head")
	pruneGit(t, proj, "switch", "-q", "main")

	st, msg := runMaintenanceVerbIn(proj, "prune", map[string]any{"mode": "prune"})
	if st != spec.StatusPass {
		t.Fatalf("status: %s: %s", st, msg)
	}
	if _, ok := pruneGitMay(proj, "rev-parse", "-q", "--verify", "refs/heads/feat/squashed"); !ok {
		t.Fatalf("branch with commits BEYOND a merged head must never be pruned:\n%s", msg)
	}
}

// TestPrune_UnknownModeFails proves an invalid mode is a loud failure.
func TestPrune_UnknownModeFails(t *testing.T) {
	proj := newPruneProject(t)
	st, _ := runMaintenanceVerbIn(proj, "prune", map[string]any{"mode": "nope"})
	if st != spec.StatusFail {
		t.Fatalf("unknown mode must fail, got %s", st)
	}
}

// TestNormalizeGitHubSlug covers the remote-URL -> owner/repo translation.
func TestNormalizeGitHubSlug(t *testing.T) {
	cases := map[string]string{
		"https://github.com/opencharly/plugin-task.git": "opencharly/plugin-task",
		"https://github.com/opencharly/plugin-task":     "opencharly/plugin-task",
		"git@github.com:opencharly/plugin-task.git":     "opencharly/plugin-task",
		"https://example.invalid/foo/bar.git":           "",
		"":                                              "",
	}
	for in, want := range cases {
		if got := normalizeGitHubSlug(in); got != want {
			t.Errorf("normalizeGitHubSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
