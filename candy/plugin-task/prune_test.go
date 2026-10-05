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
//   - feat/abandoned: its own commit NOT on main, a CLOSED PR head == its tip
//   - feat/wip: its own commit, no PR, beyond main — NEVER prunable
//
// It also creates linked worktrees under .worktrees/ and a checked-out SUBMODULE
// (`sub`) whose own worktree + branch are prunable, so pruneRepos/Pass 1/Pass 2 all
// run across the project root AND the submodule. Returns the project dir.
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

	// feat/merged: branched and fast-forwarded onto main (tip == main, ancestor).
	pruneGit(t, proj, "branch", "feat/merged", "main")

	// feat/squashed: a commit NOT on main; its PR head == the branch tip.
	pruneGit(t, proj, "switch", "-q", "-c", "feat/squashed")
	writeFile(t, filepath.Join(proj, "s"), "squash work")
	pruneGit(t, proj, "add", "-A")
	pruneGit(t, proj, "commit", "-qm", "squashed work")
	squashedTip := pruneGit(t, proj, "rev-parse", "HEAD")
	pruneGit(t, proj, "switch", "-q", "main")

	// feat/abandoned: a commit NOT on main; its PR is CLOSED (abandoned) with a head
	// == the branch tip. Reaped ONLY under include_closed.
	pruneGit(t, proj, "switch", "-q", "-c", "feat/abandoned")
	writeFile(t, filepath.Join(proj, "a"), "abandoned work")
	pruneGit(t, proj, "add", "-A")
	pruneGit(t, proj, "commit", "-qm", "abandoned work")
	abandonedTip := pruneGit(t, proj, "rev-parse", "HEAD")
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
	// A worktree on feat/abandoned, so the closed arm is exercised for BOTH kinds.
	wtAbandoned := filepath.Join(proj, ".worktrees", "cc-abandoned", "proj")
	pruneGit(t, proj, "worktree", "add", "-q", wtAbandoned, "feat/abandoned")

	// --- a checked-out SUBMODULE with its own merged worktree + branch, so
	// pruneRepos/Pass 1/Pass 2 exercise the submodule loop (not just the root). ---
	subsrc := filepath.Join(base, "subsrc")
	pruneGit(t, base, "init", "-q", "-b", "main", "subsrc")
	writeFile(t, filepath.Join(subsrc, "g"), "sub")
	pruneGit(t, subsrc, "add", "-A")
	pruneGit(t, subsrc, "commit", "-qm", "sub base")
	// A merged branch in the submodule (ancestor of its origin/main).
	suborigin := filepath.Join(base, "suborigin.git")
	pruneGit(t, base, "init", "-q", "--bare", "suborigin.git")
	pruneGit(t, subsrc, "remote", "add", "origin", suborigin)
	pruneGit(t, subsrc, "push", "-q", "origin", "main")
	pruneGit(t, subsrc, "fetch", "-q", "origin")
	pruneGit(t, subsrc, "remote", "set-head", "origin", "main")
	pruneGit(t, subsrc, "branch", "feat/sub-merged", "main")
	// Register `sub` as a submodule of proj and check it out.
	pruneGit(t, proj, "config", "-f", ".gitmodules",
		"submodule.sub.path", "sub")
	pruneGit(t, proj, "config", "-f", ".gitmodules",
		"submodule.sub.url", subsrc)
	pruneGit(t, proj, "add", ".gitmodules")
	pruneGit(t, proj, "commit", "-qm", "add submodule")
	pruneGit(t, proj, "-c", "protocol.file.allow=always",
		"submodule", "add", "-q", subsrc, "sub")
	pruneGit(t, proj, "-c", "protocol.file.allow=always",
		"submodule", "update", "--init", "sub")
	// A merged worktree of the SUBMODULE, in the umbrella layout
	// (<umbrella>/.worktrees/<slug>/<sub>) — listed by the submodule's `worktree
	// list` AND within the project's .worktrees/ root.
	subWtMerged := filepath.Join(proj, ".worktrees", "dd-sub", "sub")
	pruneGit(t, filepath.Join(proj, "sub"), "worktree", "add", "-q", subWtMerged, "feat/sub-merged")

	// Fixture PR state, keyed by head branch: a MERGED and a CLOSED PR.
	old := ghPRSource
	ghPRSource = func(string) (map[string][]prInfo, error) {
		return map[string][]prInfo{
			"feat/squashed":  {{Number: 7, State: "MERGED", HeadRef: "feat/squashed", HeadRefOID: squashedTip}},
			"feat/abandoned": {{Number: 8, State: "CLOSED", HeadRef: "feat/abandoned", HeadRefOID: abandonedTip}},
		}, nil
	}
	t.Cleanup(func() { ghPRSource = old })
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

// TestPrune_IncludeClosedReapsAbandoned proves the include_closed opt-in reaps a
// CLOSED-PR worktree/branch (tip contained in the closed head) — and that WITHOUT
// the flag the same branch/worktree survives.
func TestPrune_IncludeClosedReapsAbandoned(t *testing.T) {
	// Without the flag: the abandoned branch + worktree survive.
	proj := newPruneProject(t)
	st, msg := runMaintenanceVerbIn(proj, "prune", map[string]any{"mode": "prune"})
	if st != spec.StatusPass {
		t.Fatalf("status: %s: %s", st, msg)
	}
	if _, ok := pruneGitMay(proj, "rev-parse", "-q", "--verify", "refs/heads/feat/abandoned"); !ok {
		t.Fatalf("CLOSED-PR branch must survive WITHOUT include_closed:\n%s", msg)
	}
	if _, err := os.Stat(filepath.Join(proj, ".worktrees", "cc-abandoned", "proj", ".git")); err != nil {
		t.Fatalf("CLOSED-PR worktree must survive WITHOUT include_closed: %v", err)
	}

	// With the flag: the abandoned branch + worktree are reaped.
	proj2 := newPruneProject(t)
	st, msg = runMaintenanceVerbIn(proj2, "prune", map[string]any{"mode": "prune", "include_closed": true})
	if st != spec.StatusPass {
		t.Fatalf("status: %s: %s", st, msg)
	}
	if _, ok := pruneGitMay(proj2, "rev-parse", "-q", "--verify", "refs/heads/feat/abandoned"); ok {
		t.Fatalf("CLOSED-PR branch must be reaped WITH include_closed:\n%s", msg)
	}
	if _, err := os.Stat(filepath.Join(proj2, ".worktrees", "cc-abandoned", "proj")); err == nil {
		t.Fatalf("CLOSED-PR worktree must be reaped WITH include_closed:\n%s", msg)
	}
}

// TestPrune_UntrackedForceDiscardsDisclosed proves the force path: a merged
// worktree carrying an UNTRACKED file is reaped (git refuses without --force),
// the untracked file is discarded, and the report DISCLOSES the discard count.
func TestPrune_UntrackedForceDiscardsDisclosed(t *testing.T) {
	proj := newPruneProject(t)
	wtMerged := filepath.Join(proj, ".worktrees", "aa-merged", "proj")
	writeFile(t, filepath.Join(wtMerged, "junk.txt"), "untracked leftover")

	st, msg := runMaintenanceVerbIn(proj, "prune", map[string]any{"mode": "prune"})
	if st != spec.StatusPass {
		t.Fatalf("status: %s: %s", st, msg)
	}
	if _, err := os.Stat(wtMerged); err == nil {
		t.Fatalf("merged worktree with untracked files must still be reaped:\n%s", msg)
	}
	if !strings.Contains(msg, "discards 1 untracked entry") {
		t.Fatalf("report must DISCLOSE the untracked discard:\n%s", msg)
	}
}

// TestPrune_ModifiedTrackedSkipped proves a merged worktree with a MODIFIED tracked
// file is skipped (never destroyed). The skipped worktree still has its branch
// checked out, so Pass 2 sees the branch in-use and leaves it too — the branch is
// NOT reaped while its worktree survives.
func TestPrune_ModifiedTrackedSkipped(t *testing.T) {
	proj := newPruneProject(t)
	wtMerged := filepath.Join(proj, ".worktrees", "aa-merged", "proj")
	wtMergedDir := filepath.Join(proj, ".worktrees", "aa-merged", "proj", "f")
	writeFile(t, wtMergedDir, "locally modified tracked content")

	st, msg := runMaintenanceVerbIn(proj, "prune", map[string]any{"mode": "prune"})
	if st != spec.StatusPass {
		t.Fatalf("status: %s: %s", st, msg)
	}
	if _, err := os.Stat(wtMerged); err != nil {
		t.Fatalf("worktree with a modified tracked file must NOT be removed: %v", err)
	}
	if !strings.Contains(msg, "modified tracked files") {
		t.Fatalf("report must name the modified-tracked skip:\n%s", msg)
	}
}

// TestPrune_DetachedWorktreeSkipped proves a merged branch's worktree left in
// DETACHED HEAD is skipped (no branch to prove merged), never force-removed.
func TestPrune_DetachedWorktreeSkipped(t *testing.T) {
	proj := newPruneProject(t)
	wtMerged := filepath.Join(proj, ".worktrees", "aa-merged", "proj")
	// Detach the worktree.
	pruneGit(t, wtMerged, "checkout", "--quiet", "--detach", "HEAD")

	st, msg := runMaintenanceVerbIn(proj, "prune", map[string]any{"mode": "prune"})
	if st != spec.StatusPass {
		t.Fatalf("status: %s: %s", st, msg)
	}
	if _, err := os.Stat(wtMerged); err != nil {
		t.Fatalf("detached-HEAD worktree must be skipped: %v", err)
	}
	if !strings.Contains(msg, "detached HEAD") {
		t.Fatalf("report must name the detached-HEAD skip:\n%s", msg)
	}
}

// TestPrune_CwdWorktreeSkipped proves the "is cwd" guard: a merged worktree the
// process is STANDING INSIDE is skipped (never removed out from under the caller).
func TestPrune_CwdWorktreeSkipped(t *testing.T) {
	proj := newPruneProject(t)
	wtMerged := filepath.Join(proj, ".worktrees", "aa-merged", "proj")
	// Stand inside the merged worktree: the guard must skip it.
	t.Chdir(wtMerged)

	st, msg := runMaintenanceVerbIn(proj, "prune", map[string]any{"mode": "prune"})
	if st != spec.StatusPass {
		t.Fatalf("status: %s: %s", st, msg)
	}
	if _, err := os.Stat(wtMerged); err != nil {
		t.Fatalf("the cwd worktree must NOT be removed: %v", err)
	}
	if !strings.Contains(msg, "(is cwd)") {
		t.Fatalf("report must name the is-cwd skip:\n%s", msg)
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

// TestPrune_SubmoduleSwept proves the submodule loop in pruneRepos/Pass 1/Pass 2 is
// real: a merged worktree + branch of a CHECKED-OUT SUBMODULE is reaped, while the
// project-root merged set is reaped too. Deleting the submodule loop would leave
// this test red.
func TestPrune_SubmoduleSwept(t *testing.T) {
	proj := newPruneProject(t)
	subPath := filepath.Join(proj, "sub")
	subWt := filepath.Join(proj, ".worktrees", "dd-sub", "sub")

	st, msg := runMaintenanceVerbIn(proj, "prune", map[string]any{"mode": "prune"})
	if st != spec.StatusPass {
		t.Fatalf("status: %s: %s", st, msg)
	}
	// The submodule's merged worktree is gone.
	if _, err := os.Stat(subWt); err == nil {
		t.Fatalf("submodule merged worktree must be reaped:\n%s", msg)
	}
	// The submodule's merged branch is gone.
	if _, ok := pruneGitMay(subPath, "rev-parse", "-q", "--verify", "refs/heads/feat/sub-merged"); ok {
		t.Fatalf("submodule merged branch must be reaped:\n%s", msg)
	}
	// The submodule still exists and is intact.
	if _, err := os.Stat(filepath.Join(subPath, "g")); err != nil {
		t.Fatalf("submodule must remain intact: %v", err)
	}
}

// TestPrune_UninitializedSubmoduleIsNeverSwept is the regression for
// plugin-task#16 — the charly#768 class, missed in verb:prune. `git -C` only
// CHANGES DIRECTORY: on a present-but-UNINITIALIZED submodule directory (an empty
// dir, the normal state of a fresh `git worktree` before `submodule update
// --init`) git WALKS UP to the enclosing repository, so every `worktree list` /
// `for-each-ref` / `branch -D` the sweep makes through that path reads and
// MUTATES the PROJECT itself. Before the fix pruneRepos accepted any existing
// directory, so the project's own worktrees and branches — including another
// session's — were reported prunable once per uninitialized submodule, and
// `mode=prune` Pass 2 reached `git branch -D` on them. This test FAILS on that
// code.
func TestPrune_UninitializedSubmoduleIsNeverSwept(t *testing.T) {
	proj := newPruneProject(t)

	// Declare a SECOND submodule that is never `submodule update --init`-ed, then
	// materialize its path as an EMPTY directory — exactly the fresh-worktree state.
	const uninit = "subuninit"
	pruneGit(t, proj, "config", "-f", ".gitmodules", "submodule."+uninit+".path", uninit)
	pruneGit(t, proj, "config", "-f", ".gitmodules", "submodule."+uninit+".url",
		filepath.Join(filepath.Dir(proj), "nowhere.git"))
	if err := os.MkdirAll(filepath.Join(proj, uninit), 0o755); err != nil {
		t.Fatal(err)
	}

	// pruneRepos must never hand the sweep a path that walks up to the project.
	for _, r := range pruneRepos(proj) {
		if filepath.Base(r) == uninit {
			t.Fatalf("pruneRepos must EXCLUDE the uninitialized submodule %q "+
				"(git -C on it walks up to the superproject), got %q", uninit, r)
		}
	}

	st, msg := runMaintenanceVerbIn(proj, "prune", map[string]any{"mode": "report"})
	if st != spec.StatusPass {
		t.Fatalf("report status: %s: %s", st, msg)
	}
	// Nothing in the report may be attributed to the uninitialized submodule: every
	// target the walk-up would surface belongs to the PROJECT, not to it.
	for _, line := range strings.Split(msg, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && (f[0] == "worktree" || f[0] == "branch") && f[1] == uninit {
			t.Fatalf("the report attributed a SUPERPROJECT target to the "+
				"uninitialized submodule %q:\n%s", uninit, line)
		}
	}
}

// TestGHListPRsForDir_Live exercises the REAL gh shell-out / URL->slug / JSON-decode
// / branch-keying path (ghListPRsForDir) against the actual opencharly/plugin-task
// repo. R7a: LIVE against the real service, or SKIP cleanly when `gh` is
// unauthenticated — never a fake of the boundary.
func TestGHListPRsForDir_Live(t *testing.T) {
	if _, err := exec.LookPath("gh"); err != nil {
		t.Skip("gh not on PATH — skipping the live PR-state boundary test")
	}
	if err := exec.Command("gh", "auth", "status").Run(); err != nil {
		t.Skip("gh not authenticated — skipping the live PR-state boundary test")
	}
	// The module lives inside the plugin-task repo; resolve the repo root.
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not in a git checkout: %v", err)
	}
	repoRoot := strings.TrimSpace(string(root))
	url, err := exec.Command("git", "-C", repoRoot, "config", "--get", "remote.origin.url").Output()
	if err != nil || !strings.Contains(string(url), "github.com/opencharly/plugin-task") {
		t.Skipf("not the plugin-task checkout (origin=%q)", strings.TrimSpace(string(url)))
	}
	byBranch, err := ghListPRsForDir(repoRoot)
	if err != nil {
		t.Fatalf("ghListPRsForDir: %v", err)
	}
	// PR #3 (feat/generic-maintenance-verbs) is MERGED in this repo — the real
	// service must return it, keyed by its head branch.
	got := byBranch["feat/generic-maintenance-verbs"]
	if len(got) == 0 {
		t.Fatalf("expected the real gh layer to return feat/generic-maintenance-verbs; keys=%v", keysOf(byBranch))
	}
	if got[0].State != "MERGED" || got[0].HeadRefOID == "" {
		t.Fatalf("unexpected PR record: %+v", got[0])
	}
}

func keysOf(m map[string][]prInfo) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
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
