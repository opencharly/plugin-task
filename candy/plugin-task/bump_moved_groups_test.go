package task

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// The bump report's MOVED counters must cover EVERY group the walk touches — not only
// the index-advance group.
//
// opencharly/plugin-task#53 gave every group in the report a second number: how many
// pins the group TOUCHED and how many of them MOVED. Its coverage reached only ONE of
// the three counters: the fixture had a path with NO checkout, so the walk advanced it
// in the index and `indexMoved` was the only counter that could rise. The other two —
// `rolledMoved` (a path WITH a checkout, rolled onto its branch tip) and `pinnedMoved`
// (a pin_map EDGE) — were never exercised, so a regression in either would reach the
// product silently, and the umbrella's wave PRs quote the report verbatim.
//
// Coverage fails without the change: the pre-change summary carries no moved count at
// all, so neither assertion below can pass on the old tree.

// TestGitSubmodules_BumpCountsRolledSubmodulesThatMoved covers the ROLL group: a path
// WITH a checkout, rolled onto its default branch's tip.
//
// The control is the SAME tree bumped TWICE: the walk writes the pin both times — it
// must record where the pin is, even when the answer is "where it already was" — so
// the touched count is identical and only the MOVED count can fall to zero. That is
// the only comparison that discriminates a write count from a move count.
func TestGitSubmodules_BumpCountsRolledSubmodulesThatMoved(t *testing.T) {
	base, leafBare, doomedBare, L1, L2 := bumpOrigins(t)
	// `zebra` is a REAL submodule of a bare origin that serves `main` at L2, and the
	// umbrella's index records it at L1 — the STALE-PIN shape a wave meets. A submodule
	// created from a plain TempDir has no origin, so its path is skipped; this one has
	// one, so the walk really rolls it.
	umb := newBumpUmbrella(t, base, L1, leafBare, doomedBare, "zebra")
	if got := gitlink(umb, "zebra"); got != L1 {
		t.Fatalf("fixture precondition: the recorded pin must be the STALE L1, got %s (L1=%s L2=%s)", got, L1, L2)
	}

	// BUMP ONE: the checkout sits at the stale pin, so the roll MOVES it.
	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{"mode": "bump"})
	if st != spec.StatusPass {
		t.Fatalf("a bump over a stale materialised pin must PASS, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "rolling 1 leaf submodule(s) to their default-branch HEAD (1 moved)") {
		t.Fatalf("the report must name the roll AND that the roll moved the pin: %q", msg)
	}
	if !strings.Contains(msg, "1 pin(s) moved") {
		t.Fatalf("the report must say how many pins moved: %q", msg)
	}
	if got := gitlink(umb, "zebra"); got != L2 {
		t.Fatalf("the roll did not move the pin: gitlink=%s want L2=%s (L1=%s)", got, L2, L1)
	}

	// BUMP TWO: the SAME tree, whose pin is now current. The walk still writes the pin,
	// so the touched count is unchanged and the MOVED count is zero. A report that
	// counted writes would print the same number twice.
	st, msg = runMaintenanceVerbIn(umb, "git-submodules", map[string]any{"mode": "bump"})
	if st != spec.StatusPass {
		t.Fatalf("the second bump over a current pin must PASS, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "rolling 1 leaf submodule(s) to their default-branch HEAD (0 moved)") {
		t.Fatalf("the write count must not change and the moved count must fall to zero: %q", msg)
	}
	if !strings.Contains(msg, "0 pin(s) moved") {
		t.Fatalf("a bump over a current pin must report ZERO moved pins: %q", msg)
	}
}

// TestGitSubmodules_BumpCountsPinMapEdgeOnceInTheEdgeClause covers the EDGE group, and
// the property the three groups exist to have: they PARTITION the paths the walk
// touched, so a pin is counted ONCE and the moved counters SUM to the moved total.
//
// The fixture is the umbrella's own shape in miniature: a source repo whose tree
// records a NESTED GITLINK, an umbrella that declares the source as a submodule, and an
// UNMATERIALIZED path the umbrella derives from that nested pin. The edge is
// unmaterialized on purpose: that is the branch where the edge used to be counted
// TWICE — once in the index-advance group and once in the pin_map group — and a
// report that counted it twice would claim two moved pins where exactly one moved.
//
// The source rolls too, so the report carries all three counters at once and the SUM
// is observable: 1 rolled + 0 in the index + 1 edge = 2 moved.
func TestGitSubmodules_BumpCountsPinMapEdgeOnceInTheEdgeClause(t *testing.T) {
	base := t.TempDir()

	// `inner` — the leaf the edge points AT.
	inner := filepath.Join(base, "inner")
	gitIn(t, base, "init", "-q", "-b", "main", filepath.Base(inner))
	writeFile(t, filepath.Join(inner, "g"), "one\n")
	gitIn(t, inner, "add", "-A")
	gitIn(t, inner, "commit", "-qm", "inner one")
	i1 := gitIn(t, inner, "rev-parse", "HEAD")

	// `src` — the source of the edge. Its tree records a NESTED GITLINK for `inner`,
	// and its SECOND commit records the same one: a later commit of the source must
	// still carry the twin, or the edge would have nothing to point at.
	src := filepath.Join(base, "src")
	gitIn(t, base, "init", "-q", "-b", "main", filepath.Base(src))
	writeFile(t, filepath.Join(src, "f"), "one\n")
	gitIn(t, src, "add", "f")
	gitIn(t, src, "update-index", "--add", "--cacheinfo", "160000,"+i1+",inner")
	gitIn(t, src, "commit", "-qm", "one, with a nested gitlink")
	s1 := gitIn(t, src, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(src, "f"), "two\n")
	// `git add f`, NOT `git add -A`: the nested gitlink is recorded in the INDEX and
	// has no directory in the work tree, and a blanket add would drop it.
	gitIn(t, src, "add", "f")
	gitIn(t, src, "commit", "-qm", "two")
	s2 := gitIn(t, src, "rev-parse", "HEAD")
	if tree := gitIn(t, src, "ls-tree", "HEAD", "inner"); !strings.Contains(tree, i1) {
		t.Fatalf("fixture precondition: the source's HEAD must record the nested gitlink %s, got %q", i1, tree)
	}
	// A bare origin serving `main` at the source's tip, so the source ROLLS.
	srcBare := filepath.Join(base, "src.git")
	gitIn(t, base, "clone", "-q", "--bare", src, srcBare)

	// The umbrella: it declares `src` as a submodule (with the branch the roll
	// targets) and `edge` as an unmaterialized path the edge derives.
	umb := filepath.Join(base, "umb")
	gitIn(t, base, "init", "-q", "-b", "main", filepath.Base(umb))
	writeFile(t, filepath.Join(umb, "u"), "u")
	gitIn(t, umb, "add", "-A")
	gitIn(t, umb, "commit", "-qm", "u")
	gitIn(t, umb, "-c", "protocol.file.allow=always", "submodule", "add", "-q", srcBare, "src")
	writeFile(t, filepath.Join(umb, ".gitmodules"),
		"[submodule \"src\"]\n\tpath = src\n\turl = "+srcBare+"\n\tbranch = main\n"+
			"[submodule \"edge\"]\n\tpath = edge\n\turl = "+srcBare+"\n\tbranch = main\n")
	gitIn(t, umb, "add", ".gitmodules")
	gitIn(t, umb, "commit", "-qm", "gitmodules")
	// The STALE pins: the source is one commit behind its origin, and the edge
	// records a sha that is not the twin it must end up at.
	gitIn(t, umb, "update-index", "--add", "--cacheinfo", "160000,"+s1+",src")
	stale := gitIn(t, umb, "rev-parse", "HEAD")
	gitIn(t, umb, "update-index", "--add", "--cacheinfo", "160000,"+stale+",edge")
	gitIn(t, umb, "commit", "-qm", "stale pins")
	// The edge has NO checkout: the pin lives in the index (opencharly/plugin-task#37).
	if err := os.MkdirAll(filepath.Join(umb, "edge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(umb, "edge", ".git")); err == nil {
		t.Fatalf("fixture precondition: edge must have no .git")
	}
	// The source's checkout sits at the stale pin, so the roll really moves it.
	gitIn(t, umb, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "src")

	// BUMP ONE: the source rolls (its pin moves) and the edge advances to the twin
	// (its pin moves). Exactly TWO pins moved, and each is counted ONCE.
	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{
		"mode": "bump", "pinned_from": "src", "pin_map": map[string]any{"edge": "inner"},
	})
	if st != spec.StatusPass {
		t.Fatalf("a bump over a stale source and a stale edge must PASS, got %s: %s", st, msg)
	}
	// THE ASSERTION, part 1: the edge is counted in the EDGE clause, and it MOVED.
	if !strings.Contains(msg, "pinned 1 pin_map entr(y|ies) (edge; 1 moved)") {
		t.Fatalf("the edge must be counted ONCE, in the pin_map clause, with its moved count: %q", msg)
	}
	// THE ASSERTION, part 2: the edge is NOT counted in the index-advance clause. The
	// groups partition the paths the walk touched, so the edge — which is written in
	// the index — belongs to the edge group alone.
	if !strings.Contains(msg, "advancing 0 leaf pin(s) in the index (0 moved)") {
		t.Fatalf("the edge must not be counted in the index-advance group: %q", msg)
	}
	// The roll group carries its own moved count: the source's pin moved.
	if !strings.Contains(msg, "rolling 1 leaf submodule(s) to their default-branch HEAD (1 moved)") {
		t.Fatalf("the rolled source must be reported as moved: %q", msg)
	}
	// THE ASSERTION, part 3: the three moved counters SUM to the moved total — 1 + 0 + 1.
	if !strings.Contains(msg, "2 pin(s) moved") {
		t.Fatalf("the moved total must be the SUM of the three groups (1 rolled + 0 in the index + 1 edge): %q", msg)
	}
	if got := gitlink(umb, "edge"); got != i1 {
		t.Fatalf("the edge did not advance to the twin: gitlink=%s want %s", got, i1)
	}
	if got := gitlink(umb, "src"); got != s2 {
		t.Fatalf("the source did not roll: gitlink=%s want %s", got, s2)
	}

	// BUMP TWO: the SAME tree, whose pins are now current. The walk writes every pin
	// again, so the touched counts are unchanged and every MOVED count is zero.
	st, msg = runMaintenanceVerbIn(umb, "git-submodules", map[string]any{
		"mode": "bump", "pinned_from": "src", "pin_map": map[string]any{"edge": "inner"},
	})
	if st != spec.StatusPass {
		t.Fatalf("the second bump over current pins must PASS, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "pinned 1 pin_map entr(y|ies) (edge; 0 moved)") {
		t.Fatalf("the edge is still TOUCHED, so the touched count must not change, and it moved nothing: %q", msg)
	}
	if !strings.Contains(msg, "advancing 0 leaf pin(s) in the index (0 moved)") {
		t.Fatalf("the edge must still be absent from the index-advance group: %q", msg)
	}
	if !strings.Contains(msg, "0 pin(s) moved") {
		t.Fatalf("a bump over current pins must report ZERO moved pins: %q", msg)
	}
}

// The FAILURE report carries the same three moved counters, and they sum too.
//
// The failure report prints the same three groups and the same moved total, in a
// DIFFERENT arrangement, so the counters are proved there as well: the reader of a
// failed wave is the reader who most needs to know what the wave moved before it
// failed.
//
// The fixture must produce a NON-ZERO moved count. A fail report whose counters are
// all zero would be satisfied by a report that printed a hardcoded 0, so it would prove
// PRESENCE and not CORRECTNESS. The stale materialised pin is what makes the roll move
// one pin; the twin that does not exist in the source's tree is what makes the wave
// fail.
func TestGitSubmodules_BumpFailureReportCarriesTheMovedCounters(t *testing.T) {
	base, leafBare, doomedBare, L1, _ := bumpOrigins(t)
	umb := newBumpUmbrella(t, base, L1, leafBare, doomedBare, "zebra")

	// The wave FAILS: the edge's twin does not exist in the source's tree.
	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{
		"mode": "bump", "pinned_from": "zebra", "pin_map": map[string]any{"edge": "does/not/exist"},
	})
	if st != spec.StatusFail {
		t.Fatalf("an unresolvable twin must FAIL, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "1 pin(s) FAILED") {
		t.Fatalf("the failure report must name the failure: %q", msg)
	}
	// The roll still moved a pin before the wave failed, and the report says so.
	rolled, index, edges, total := movedCounts(t, msg)
	if rolled != 1 {
		t.Fatalf("the FAIL report must carry the roll's moved count: got %d, want 1: %q", rolled, msg)
	}
	if index != 0 || edges != 0 {
		t.Fatalf("the FAIL report must carry the other two groups' moved counts: index=%d edges=%d: %q", index, edges, msg)
	}
	if rolled+index+edges != total {
		t.Fatalf("the three moved counters must SUM to the moved total in the FAIL report too: %d + %d + %d != %d in %q",
			rolled, index, edges, total, msg)
	}
	if total != 1 {
		t.Fatalf("exactly one pin moved before the failure, got %d: %q", total, msg)
	}
}

// The three moved counters SUM to the moved total, in the report the verb prints.
//
// This is the property the three groups exist to have, so it is asserted on the REPORT
// and not on the internal counters: a report whose numbers do not add up is a report a
// reader cannot trust, whatever the counters behind it are.
func TestGitSubmodules_BumpMovedCountsSumToTheMovedTotal(t *testing.T) {
	base, leafBare, doomedBare, L1, _ := bumpOrigins(t)
	umb := newBumpUmbrella(t, base, L1, leafBare, doomedBare, "zebra")

	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{"mode": "bump"})
	if st != spec.StatusPass {
		t.Fatalf("the bump must PASS, got %s: %s", st, msg)
	}
	rolled, index, edges, total := movedCounts(t, msg)
	if rolled+index+edges != total {
		t.Fatalf("the three moved counters must SUM to the moved total: %d + %d + %d != %d in %q",
			rolled, index, edges, total, msg)
	}
	if total != 1 {
		t.Fatalf("exactly one pin moved in this fixture, got %d: %q", total, msg)
	}
}

// movedCounts reads the four numbers out of a bump report: the three groups' moved
// counts and the moved total. It FAILS the test when the report does not carry them —
// a report that stopped counting would otherwise pass a sum assertion over zeroes.
//
// Every clause is read in BOTH of its arrangements, because the PASS and the FAIL
// reports word the same group differently: the PASS report says "after ROLLING … and
// ADVANCING …", the FAIL report says "; ROLLED …; ADVANCED …", and the pin_map clause
// prints its names inside the bracket in one ("(edge; 1 moved)") and after it in the
// other ("(1 moved): edge"). One reader, one implementation, both shapes.
func movedCounts(t *testing.T, msg string) (rolled, index, edges, total int) {
	t.Helper()
	rolled = movedCount(t, msg, `roll(?:ing|ed) \d+ leaf submodule\(s\) to their default-branch HEAD \((\d+) moved\)`)
	index = movedCount(t, msg, `advanc(?:ing|ed) \d+ leaf pin\(s\) in the index \((\d+) moved\)`)
	edges = movedCount(t, msg, `pinned \d+ pin_map entr\(y\|ies\) \((?:[^)]*; )?(\d+) moved\)`)
	total = movedCount(t, msg, `(\d+) pin\(s\) moved`)
	return rolled, index, edges, total
}

// movedCount extracts ONE number from a bump report. A missing number is a test
// failure, never a zero: a report that stopped counting would otherwise satisfy a sum
// assertion over zeroes.
func movedCount(t *testing.T, msg, pattern string) int {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(msg)
	if m == nil {
		t.Fatalf("the report does not carry the number %q: %q", pattern, msg)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("the report's number %q is not a number: %v", m[1], err)
	}
	return n
}
