package task

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// TestGitSubmodules_BumpReconcilesNestedCheckouts is plugin-task#11's gate: advancing a
// submodule's gitlink moves the PIN, and it must also move that submodule's OWN
// INITIALIZED nested checkouts onto the gitlinks the new commit records. Without that
// reconciliation the shared tree goes dirty (` M <nested>`) and `git-submodules verify`
// fails twice over — the umbrella's own checkout audit and its nested half — on a change
// the bump itself just made.
//
// The fixture is the tree the README's `git clone --recurse-submodules` produces: the
// umbrella pins `outer` at O1 (an umbrella commit that is not an ancestor of O2, so the
// walk treats O1 as not-rolled and the pin really moves), and BOTH levels are initialized
// (outer at O1, its nested `inner` at L1). `bump` rolls `outer` to O2, whose `inner`
// gitlink is L2; the assertion is that `inner`'s checkout followed. Before the fix this
// FAILS with ` M inner` in outer's status.
func TestGitSubmodules_BumpReconcilesNestedCheckouts(t *testing.T) {
	base, leafBare, _, L1, L2 := bumpOrigins(t)

	// outer: a repo whose OWN submodule `inner` is leafBare, with O1 pinning inner=L1 and
	// O2 pinning inner=L2.
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
	o1 := strings.TrimSpace(gitIn(t, outer, "rev-parse", "HEAD"))
	// Advance inner to L2 inside outer, and record that as O2 (outer's main HEAD).
	gitIn(t, outer, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "inner")
	innerSrc := filepath.Join(outer, "inner")
	gitIn(t, innerSrc, "fetch", "-q", "origin")
	gitIn(t, innerSrc, "switch", "--quiet", "--detach", L2)
	gitIn(t, outer, "add", "inner")
	gitIn(t, outer, "commit", "-qm", "inner=L2")
	o2 := strings.TrimSpace(gitIn(t, outer, "rev-parse", "HEAD"))
	outerBare := filepath.Join(base, "outer.git")
	gitIn(t, base, "clone", "-q", "--bare", outer, outerBare)

	// The umbrella declares ONLY `outer`, pinned at O1, and initializes BOTH levels.
	umb := filepath.Join(base, "umb-nested")
	gitIn(t, base, "init", "-q", "-b", "main", "umb-nested")
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
	// The NESTED checkout too — the clone shape this issue is about.
	outerCo := filepath.Join(umb, "outer")
	gitIn(t, outerCo, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "inner")
	if got := gitlink(umb, "outer"); got != o1 {
		t.Fatalf("fixture: umbrella must pin outer at O1=%s, got %s", o1, got)
	}
	innerCo := filepath.Join(outerCo, "inner")
	if out := strings.TrimSpace(gitIn(t, innerCo, "rev-parse", "HEAD")); out != L1 {
		t.Fatalf("fixture: nested inner must start at L1=%s, got %s", L1, out)
	}
	// The nested checkout needs L2 locally: the verb's reconcile moves ALREADY-INITIALIZED
	// nested checkouts and never initializes/fetches a nested remote on its own, so the
	// fixture supplies the object the way a real nested remote would (the protocol override
	// is a FIXTURE concern — a local-path origin — and is deliberately not baked into the
	// shipped verb).
	gitIn(t, innerCo, "-c", "protocol.file.allow=always", "fetch", "-q", "origin", L2)

	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{"mode": "bump"})
	if st != spec.StatusPass {
		t.Fatalf("bump must PASS, got %s: %s", st, msg)
	}
	if got := gitlink(umb, "outer"); got != o2 {
		t.Fatalf("bump did not move outer's pin: gitlink=%s want O2=%s (O1=%s)", got, o2, o1)
	}
	// THE ASSERTION: the nested checkout followed the pin, so outer's own tree is clean
	// where it was ` M inner` before the fix.
	if out := strings.TrimSpace(gitIn(t, outerCo, "status", "--porcelain")); out != "" {
		t.Fatalf("bump left outer's nested checkouts stale (plugin-task#11): `git -C outer status --porcelain` = %q; inner HEAD=%s want L2=%s",
			out, strings.TrimSpace(gitIn(t, filepath.Join(outerCo, "inner"), "rev-parse", "HEAD")), L2)
	}
	if got := strings.TrimSpace(gitIn(t, filepath.Join(outerCo, "inner"), "rev-parse", "HEAD")); got != L2 {
		t.Fatalf("nested inner checkout = %s, want L2=%s (the gitlink its new commit records)", got, L2)
	}
}
