package task

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// TestGitSubmodules_FFRefusesNonAncestor is the fast-forward gate.
//
// MEASURED HAZARD: from a detached HEAD carrying a local commit, `git switch --detach
// <unrelated tip>` exits 0 and leaves that commit in ZERO refs (the reflog alone). So ff
// must REFUSE a move whose target does not descend from the checkout's current HEAD,
// leaving the submodule EXACTLY as it was — checkout not switched, gitlink not staged,
// nested checkouts not reconciled.
//
// BOTH ff phases are covered, because both call pinSubmodule: arm 1 is the leaf roll
// (a path not in pin_map), arm 2 is a pin_map EDGE whose twin is BEHIND the edge
// checkout's HEAD (the producer must advance first, never a rewind).
func TestGitSubmodules_FFRefusesNonAncestor(t *testing.T) {
	// Arm 1 — the LEAF roll.
	base, leafBare, doomedBare, L1, L2 := ffOrigins(t)
	umb := newFFUmbrella(t, base, L1, leafBare, doomedBare, "zebra")
	co := filepath.Join(umb, "zebra")
	// A local commit AT the branch tip — the head that no ff may move.
	gitIn(t, co, "switch", "--quiet", "--detach", L2)
	writeFile(t, filepath.Join(co, "work"), "work")
	gitIn(t, co, "add", "-A")
	gitIn(t, co, "commit", "-qm", "local work")
	head := gitIn(t, co, "rev-parse", "HEAD")
	if head == L2 {
		t.Fatalf("fixture: the checkout must carry a commit BEYOND the tip")
	}

	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{"mode": "ff"})
	if st != spec.StatusFail {
		t.Fatalf("a non-fast-forward move must FAIL, got %s: %s", st, msg)
	}
	for _, want := range []string{
		"refused to move zebra",
		head,
		L2,
		"a fast-forward only advances",
		"push, branch or stash the work at HEAD, or advance the producer first",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal must name the path, both commits and the direction; missing %q in:\n%s", want, msg)
		}
	}
	// The refusal left the submodule EXACTLY as it was.
	if got := gitIn(t, co, "rev-parse", "HEAD"); got != head {
		t.Fatalf("the refused checkout moved: HEAD=%s want %s", got, head)
	}
	if got := gitlink(umb, "zebra"); got != L1 {
		t.Fatalf("the refused pin was staged: gitlink=%s want the unchanged %s", got, L1)
	}

	// Arm 2 — the pin_map EDGE. `src`'s tree records its twin `inner` at L2b; the edge
	// checkout carries a commit BEYOND that, so pinning it would rewind it.
	base2, leafBare2, srcBare2, L1b, L2b := ffOriginsEdge(t)
	umb2 := filepath.Join(base2, "umb-edge")
	gitIn(t, base2, "init", "-q", "-b", "main", "umb-edge")
	writeFile(t, filepath.Join(umb2, "u"), "u")
	gitIn(t, umb2, "add", "-A")
	gitIn(t, umb2, "commit", "-qm", "u")
	gitIn(t, umb2, "-c", "protocol.file.allow=always", "submodule", "add", "-q", srcBare2, "src")
	gitIn(t, umb2, "config", "-f", ".gitmodules", "submodule.src.branch", "main")
	gitIn(t, umb2, "-c", "protocol.file.allow=always", "submodule", "add", "-q", leafBare2, "edge")
	gitIn(t, umb2, "config", "-f", ".gitmodules", "submodule.edge.branch", "main")
	gitIn(t, umb2, "add", ".gitmodules")
	gitIn(t, umb2, "commit", "-qm", "branches")
	edgeCo := filepath.Join(umb2, "edge")
	gitIn(t, edgeCo, "switch", "--quiet", "--detach", L2b)
	writeFile(t, filepath.Join(edgeCo, "work"), "work")
	gitIn(t, edgeCo, "add", "-A")
	gitIn(t, edgeCo, "commit", "-qm", "local work")
	edgeHead := gitIn(t, edgeCo, "rev-parse", "HEAD")
	gitIn(t, umb2, "update-index", "--add", "--cacheinfo", "160000,"+L1b+",edge")
	gitIn(t, umb2, "commit", "-qm", "edge behind its twin")

	st, msg = runMaintenanceVerbIn(umb2, "git-submodules", map[string]any{
		"mode": "ff", "pinned_from": "src", "pin_map": map[string]any{"edge": "inner"},
	})
	if st != spec.StatusFail {
		t.Fatalf("an edge whose twin is behind its HEAD must FAIL, got %s: %s", st, msg)
	}
	for _, want := range []string{
		"refused to move edge",
		"a fast-forward only advances",
		"advance the producer first",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the edge refusal must carry the same message; missing %q in:\n%s", want, msg)
		}
	}
	if got := gitIn(t, edgeCo, "rev-parse", "HEAD"); got != edgeHead {
		t.Fatalf("the refused edge checkout moved: HEAD=%s want %s", got, edgeHead)
	}
	if got := gitlink(umb2, "edge"); got != L1b {
		t.Fatalf("the refused edge pin was staged: gitlink=%s want the unchanged %s", got, L1b)
	}
}

// ffOriginsEdge builds the edge fixture arm 2 needs: a bare `src.git` whose `main` records
// a NESTED gitlink for `inner` (a leaf bare origin serving L1b then L2b). It returns the
// base dir, both values and the two leaf tips.
func ffOriginsEdge(t *testing.T) (base, leafBare, srcBare, L1b, L2b string) {
	t.Helper()
	base, leafBare, _, L1b, L2b = ffOrigins(t)
	src := filepath.Join(base, "src-origin")
	gitIn(t, base, "init", "-q", "-b", "main", "src-origin")
	writeFile(t, filepath.Join(src, "s"), "s")
	gitIn(t, src, "add", "-A")
	gitIn(t, src, "commit", "-qm", "s")
	writeFile(t, filepath.Join(src, ".gitmodules"),
		"[submodule \"inner\"]\n\tpath = inner\n\turl = "+leafBare+"\n\tbranch = main\n")
	gitIn(t, src, "add", ".gitmodules")
	gitIn(t, src, "commit", "-qm", "gitmodules")
	gitIn(t, src, "update-index", "--add", "--cacheinfo", "160000,"+L2b+",inner")
	gitIn(t, src, "commit", "-qm", "inner=L2")
	srcBare = filepath.Join(base, "src.git")
	gitIn(t, base, "clone", "-q", "--bare", src, srcBare)
	return base, leafBare, srcBare, L1b, L2b
}
