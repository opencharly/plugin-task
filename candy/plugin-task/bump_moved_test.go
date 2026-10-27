package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// The bump report must count the pins that MOVED, not the pins it WROTE.
//
// The defect (opencharly/plugin-task#53): `advancePinInIndex` records a pin in the
// superproject's index with `git update-index --add --cacheinfo` and returns PASS on
// exit 0, and the caller counted every PASS. So the report's "advancing 406 pin(s) in
// the index" counted INDEX WRITES, and it read as a count of pins that moved. Measured
// on the live umbrella: 406 written, 4 moved — a report that a body repeats verbatim
// would be false by 402.
//
// The control is the SAME tree bumped TWICE, which is the only comparison that
// discriminates: the walk writes the pin both times (it must record where the pin is,
// even when the answer is "where it already was"), so the WRITE count is identical
// and only the MOVE count can differ.
//
// Coverage fails without the change: the pre-change summary carries no moved count at
// all, so neither assertion below can pass on the old tree.
func TestGitSubmodules_BumpCountsMovedPinsNotIndexWrites(t *testing.T) {
	base := t.TempDir()

	// A leaf repo with ONE commit, so the remote serves exactly one tip.
	leaf := filepath.Join(base, "leaf")
	gitIn(t, base, "init", "-q", "-b", "main", filepath.Base(leaf))
	writeFile(t, filepath.Join(leaf, "f"), "L1")
	gitIn(t, leaf, "add", "-A")
	gitIn(t, leaf, "commit", "-qm", "L1")
	leafTip := gitIn(t, leaf, "rev-parse", "HEAD")
	leafBare := filepath.Join(base, "leaf.git")
	gitIn(t, base, "clone", "-q", "--bare", leaf, leafBare)

	// An umbrella that DECLARES `target` and has NO checkout for it: the pin lives
	// in the index (opencharly/plugin-task#37), so the bump advances it there.
	umb := filepath.Join(base, "umb")
	gitIn(t, base, "init", "-q", "-b", "main", filepath.Base(umb))
	writeFile(t, filepath.Join(umb, "u"), "u")
	gitIn(t, umb, "add", "-A")
	gitIn(t, umb, "commit", "-qm", "u")
	writeFile(t, filepath.Join(umb, ".gitmodules"),
		"[submodule \"target\"]\n\tpath = target\n\turl = "+leafBare+"\n\tbranch = main\n")
	gitIn(t, umb, "add", ".gitmodules")
	gitIn(t, umb, "commit", "-qm", "gitmodules")
	// The recorded pin is ONE COMMIT BEHIND: the index holds a sha the remote does
	// not serve, so the bump's write moves it.
	head := gitIn(t, umb, "rev-parse", "HEAD")
	gitIn(t, umb, "update-index", "--add", "--cacheinfo", "160000,"+head+",target")
	gitIn(t, umb, "commit", "-qm", "stale gitlink")
	// The directory exists and is EMPTY (no .git): the declared-but-unmaterialized state.
	if err := os.MkdirAll(filepath.Join(umb, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(umb, "target", ".git")); err == nil {
		t.Fatalf("fixture precondition: target must have no .git")
	}

	// BUMP ONE: the pin is behind, so the write MOVES it.
	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{"mode": "bump"})
	if st != spec.StatusPass {
		t.Fatalf("bump over an unmaterialized path must PASS, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "advancing 1 leaf pin(s) in the index (1 moved)") {
		t.Fatalf("the report must name the pin it advanced AND that the pin moved: %q", msg)
	}
	if !strings.Contains(msg, "1 pin(s) moved") {
		t.Fatalf("the report must say how many pins moved: %q", msg)
	}
	if got := gitlink(umb, "target"); got != leafTip {
		t.Fatalf("the pin did not advance to the remote's tip: %s != %s", got, leafTip)
	}

	// BUMP TWO: the SAME tree, whose pin is now current. The walk still WRITES the
	// pin — it must record where the pin is — so the touched count is unchanged and
	// the MOVED count is zero. This is the discrimination: a report that counted
	// writes would print the same number twice.
	st, msg = runMaintenanceVerbIn(umb, "git-submodules", map[string]any{"mode": "bump"})
	if st != spec.StatusPass {
		t.Fatalf("the second bump over a current pin must PASS, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "advancing 1 leaf pin(s) in the index (0 moved)") {
		t.Fatalf("the write count must not change and the moved count must fall to zero: %q", msg)
	}
	if !strings.Contains(msg, "0 pin(s) moved") {
		t.Fatalf("a bump over a current pin must report ZERO moved pins: %q", msg)
	}
}
