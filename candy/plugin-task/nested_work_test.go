package task

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// nestedWorkFixture builds the tree opencharly/plugin-task#54 is about: an umbrella
// that pins `outer` at O1, `outer` that pins its own `inner` at L1, and BOTH levels
// initialized — the shape `git clone --recurse-submodules` produces.
//
// `outer`'s main is at O2, the commit that records `inner` at L2, so the bump ROLLS
// outer and the reconcile then moves the nested checkout from L1 to L2. The nested
// checkout is given L2 locally, the way a real nested remote would serve it.
//
// It also pins a SIBLING, `aaa-leaf`, at L1 — a plain leaf whose origin serves L2 —
// so the umbrella carries TWO submodules and the SCOPE of a refusal is observable:
// the paths are SORTED, so `aaa-leaf` is rolled FIRST and `outer` is refused SECOND.
// That is the shape the product's per-path failure contract (opencharly/plugin-task#13)
// produces, and the shape in which a refusal leaves an earlier path's staged pin
// behind.
//
// It returns the umbrella, the nested checkout's directory, the sibling's directory,
// and the SHAs the assertions speak about.
func nestedWorkFixture(t *testing.T) (umb, innerCo, leafCo, o1, l1, l2 string) {
	t.Helper()
	base, leafBare, _, L1, L2 := bumpOrigins(t)

	outer := filepath.Join(base, "outer-src")
	gitIn(t, base, "init", "-q", "-b", "main", "outer-src")
	writeFile(t, filepath.Join(outer, "o"), "o")
	gitIn(t, outer, "add", "-A")
	gitIn(t, outer, "commit", "-qm", "o")
	gitIn(t, outer, "-c", "protocol.file.allow=always", "submodule", "add", "-q", leafBare, "inner")
	gitIn(t, outer, "config", "-f", ".gitmodules", "submodule.inner.branch", "main")
	gitIn(t, outer, "add", ".gitmodules")
	gitIn(t, outer, "commit", "-qm", "inner=main")
	gitIn(t, outer, "update-index", "--add", "--cacheinfo", "160000,"+L1+",inner")
	gitIn(t, outer, "commit", "-qm", "inner=L1")
	o1 = gitIn(t, outer, "rev-parse", "HEAD")
	// Advance inner to L2 inside outer: that is the commit the bump rolls outer to.
	gitIn(t, outer, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "inner")
	gitIn(t, filepath.Join(outer, "inner"), "fetch", "-q", "origin")
	gitIn(t, filepath.Join(outer, "inner"), "switch", "--quiet", "--detach", L2)
	gitIn(t, outer, "add", "inner")
	gitIn(t, outer, "commit", "-qm", "inner=L2")
	outerBare := filepath.Join(base, "outer.git")
	gitIn(t, base, "clone", "-q", "--bare", outer, outerBare)

	umb = filepath.Join(base, "umb-nested-work")
	gitIn(t, base, "init", "-q", "-b", "main", "umb-nested-work")
	writeFile(t, filepath.Join(umb, "u"), "u")
	gitIn(t, umb, "add", "-A")
	gitIn(t, umb, "commit", "-qm", "u")
	gitIn(t, umb, "-c", "protocol.file.allow=always", "submodule", "add", "-q", outerBare, "outer")
	gitIn(t, umb, "config", "-f", ".gitmodules", "submodule.outer.branch", "main")
	gitIn(t, umb, "add", ".gitmodules")
	gitIn(t, umb, "commit", "-qm", "branch=main")
	gitIn(t, umb, "update-index", "--add", "--cacheinfo", "160000,"+o1+",outer")
	gitIn(t, umb, "commit", "-qm", "stale pin")
	gitIn(t, umb, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "outer")
	outerCo := filepath.Join(umb, "outer")
	gitIn(t, outerCo, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "inner")
	innerCo = filepath.Join(outerCo, "inner")
	gitIn(t, innerCo, "-c", "protocol.file.allow=always", "fetch", "-q", "origin", L2)
	// The SIBLING: a plain leaf pinned at L1, whose origin serves L2. It sorts BEFORE
	// "outer", so the bump ROLLS it first and the refusal of "outer" is then the
	// second thing that happens — the shape in which the refusal's scope is visible.
	gitIn(t, umb, "-c", "protocol.file.allow=always", "submodule", "add", "-q", leafBare, "aaa-leaf")
	gitIn(t, umb, "config", "-f", ".gitmodules", "submodule.aaa-leaf.branch", "main")
	gitIn(t, umb, "add", ".gitmodules")
	gitIn(t, umb, "commit", "-qm", "sibling branch=main")
	gitIn(t, umb, "update-index", "--add", "--cacheinfo", "160000,"+L1+",aaa-leaf")
	gitIn(t, umb, "commit", "-qm", "sibling stale pin")
	gitIn(t, umb, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "aaa-leaf")
	leafCo = filepath.Join(umb, "aaa-leaf")
	return umb, innerCo, leafCo, o1, L1, L2
}

// TestGitSubmodules_BumpRefusesToDropNestedWork is opencharly/plugin-task#54's gate.
//
// THE DEFECT: the reconcile moves the nested checkouts with `git submodule update
// --recursive`, and that update takes a nested checkout off the commit it is at — even
// when that commit carries work NOTHING ELSE REFERENCES — with exit 0 and nothing
// reported. The operator's commit survives only in the reflog. MEASURED against git
// 2.56: the same update refuses a DIRTY nested checkout loudly, so the loss is not
// git's doing but the update's blind spot.
//
// THE ASSERTION: the bump REFUSES, it NAMES the nested checkout, and it leaves THE
// REFUSED SUBMODULE exactly as it was — the nested checkout still holds the operator's
// commit, the superproject's pin was NOT staged, and the submodule's own checkout was
// not switched. A bump that refuses is the only outcome that loses nothing.
//
// The scope is PER-SUBMODULE, and deliberately so: the bump collects per-path failures
// and continues (opencharly/plugin-task#13), so a sibling that sorts first still rolls.
// The sibling's roll is asserted by
// TestGitSubmodules_BumpRefusalIsScopedToTheRefusedSubmodule, and this fixture carries
// that sibling so the scope is visible here too.
//
// Before the fix this FAILS: the bump PASSES and the nested checkout is moved off the
// operator's commit, with nothing reported.
func TestGitSubmodules_BumpRefusesToDropNestedWork(t *testing.T) {
	umb, innerCo, _, o1, _, _ := nestedWorkFixture(t)

	// The operator's work: a commit in the nested checkout that NO ref references —
	// the shape the update drops.
	writeFile(t, filepath.Join(innerCo, "work"), "work")
	gitIn(t, innerCo, "add", "-A")
	gitIn(t, innerCo, "commit", "-qm", "local work")
	work := gitIn(t, innerCo, "rev-parse", "HEAD")
	if out := gitIn(t, innerCo, "for-each-ref", "--contains", work, "--format=%(refname)"); out != "" {
		t.Fatalf("fixture: the local commit must be referenced by no ref, got %q", out)
	}

	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{"mode": "bump"})
	if st != spec.StatusFail {
		t.Fatalf("the bump must REFUSE a nested checkout whose commit the update would drop, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "outer/inner") {
		t.Fatalf("the refusal must NAME the nested checkout, got: %s", msg)
	}
	if got := gitIn(t, innerCo, "rev-parse", "HEAD"); got != work {
		t.Fatalf("the nested checkout must still hold the operator's commit %s, got %s", work, got)
	}
	// The tree is untouched: the pin was not staged and the submodule was not switched.
	if got := gitlink(umb, "outer"); got != o1 {
		t.Fatalf("the refusal must leave the superproject's pin at %s, got %s", o1, got)
	}
	if got := gitIn(t, filepath.Join(umb, "outer"), "rev-parse", "HEAD"); got != o1 {
		t.Fatalf("the refusal must leave the submodule's checkout at %s, got %s", o1, got)
	}
}

// TestGitSubmodules_BumpKeepsNestedWorkThatABranchHolds is the DISCRIMINATING half of
// the gate: the same work, on a BRANCH, is not lost by the update — the branch still
// holds the commit after the checkout moved — so the bump must go through.
//
// Without this half the gate would also pass a verb that refuses every nested
// checkout, which is not a fix but a freeze (R7: a test that cannot fail is invalid).
func TestGitSubmodules_BumpKeepsNestedWorkThatABranchHolds(t *testing.T) {
	umb, innerCo, _, _, _, L2 := nestedWorkFixture(t)

	writeFile(t, filepath.Join(innerCo, "work"), "work")
	gitIn(t, innerCo, "add", "-A")
	gitIn(t, innerCo, "commit", "-qm", "local work")
	work := gitIn(t, innerCo, "rev-parse", "HEAD")
	gitIn(t, innerCo, "branch", "keep")

	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{"mode": "bump"})
	if st != spec.StatusPass {
		t.Fatalf("the bump must go through when a branch holds the local commit, got %s: %s", st, msg)
	}
	// The work survived the move.
	if got := gitIn(t, innerCo, "rev-parse", "keep"); got != work {
		t.Fatalf("the branch must still hold the operator's commit %s, got %s", work, got)
	}
	// And the nested checkout followed the pin the new commit records.
	if got := gitIn(t, innerCo, "rev-parse", "HEAD"); got != L2 {
		t.Fatalf("the nested checkout must be at the recorded pin %s, got %s", L2, got)
	}
}

// TestGitSubmodules_BumpRefusalIsScopedToTheRefusedSubmodule proves the SCOPE of the
// refusal, which the #54 gate alone did not show.
//
// THE QUESTION: when the bump refuses a nested checkout that carries work, what is left
// untouched? The answer the shipped surfaces gave was "the tree" — which is not what the
// product does. The bump collects PER-PATH failures and continues
// (opencharly/plugin-task#13: "ONE path's per-invocation failure costs that ONE pin, not
// every pin sorted after it", proven by TestGitSubmodules_BumpContinuesPastFailure), so
// a refusal stops ITS OWN submodule and nothing else.
//
// THE FIXTURE: the umbrella carries two submodules and the paths are SORTED, so
// `aaa-leaf` is rolled FIRST and `outer` — the one whose nested checkout carries the
// operator's work — is refused SECOND. The sibling's staged pin is the evidence that
// the refusal is not a whole-bump abort; the refused submodule's untouched state is the
// evidence that the refusal is not a no-op.
//
// Before the #54 fix this FAILS: the bump PASSES, the nested checkout is moved off the
// operator's commit, and nothing is reported.
func TestGitSubmodules_BumpRefusalIsScopedToTheRefusedSubmodule(t *testing.T) {
	umb, innerCo, leafCo, o1, _, L2 := nestedWorkFixture(t)

	// The operator's work: a commit in the nested checkout that NO ref references —
	// the shape the update drops.
	writeFile(t, filepath.Join(innerCo, "work"), "work")
	gitIn(t, innerCo, "add", "-A")
	gitIn(t, innerCo, "commit", "-qm", "local work")
	work := gitIn(t, innerCo, "rev-parse", "HEAD")
	if out := gitIn(t, innerCo, "for-each-ref", "--contains", work, "--format=%(refname)"); out != "" {
		t.Fatalf("fixture: the local commit must be referenced by no ref, got %q", out)
	}

	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{"mode": "bump"})
	t.Logf("bump: %s: %s", st, msg)
	if st != spec.StatusFail {
		t.Fatalf("the bump must REFUSE the nested checkout whose commit the update would drop, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "outer/inner") {
		t.Fatalf("the refusal must NAME the nested checkout, got: %s", msg)
	}
	// The REFUSED SUBMODULE is untouched.
	if got := gitIn(t, innerCo, "rev-parse", "HEAD"); got != work {
		t.Fatalf("the nested checkout must still hold the operator's commit %s, got %s", work, got)
	}
	if got := gitlink(umb, "outer"); got != o1 {
		t.Fatalf("the refused submodule's pin must stay at %s, got %s", o1, got)
	}
	if got := gitIn(t, filepath.Join(umb, "outer"), "rev-parse", "HEAD"); got != o1 {
		t.Fatalf("the refused submodule's own checkout must stay at %s, got %s", o1, got)
	}
	// The SIBLING rolled: the refusal is scoped to its own submodule, not the wave.
	if got := gitlink(umb, "aaa-leaf"); got != L2 {
		t.Fatalf("the sibling must still have rolled to %s — a refusal is per-submodule, got %s", L2, got)
	}
	if got := gitIn(t, leafCo, "rev-parse", "HEAD"); got != L2 {
		t.Fatalf("the sibling's checkout must be at %s, got %s", L2, got)
	}
}
