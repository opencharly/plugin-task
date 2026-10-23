package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencharly/spec/spec"
)

// curGit runs git with the fixture's sanitized environment, failing the test on error.
// The currency arms need a plain `clone`, which the `-C`-shaped `gitRepo` helper cannot
// express.
func curGit(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// curSuper creates the three-level shape the real umbrella has, in miniature:
//
//	inner   a repo whose main is at i2
//	up      a repo whose main is at c2, and whose tree at c1 records a NESTED GITLINK for
//	        `inner` (the shape charly's tree has for box/*)
//	super   the project under test: submodule `src` = a checkout of `up` at c1 (BEHIND its
//	        main, with its own `inner` checkout also behind), and submodule `d`, which an
//	        EDGE may derive from src's own nested pin.
//
// It returns the superproject dir, its git runner, c1, c2 and i1 — the value c1 records for
// `inner`, which is the twin an edge derives from.
func curSuper(t *testing.T, tmp string) (dir string, run func(...string) string, c1, c2, i1 string) {
	t.Helper()
	inner := filepath.Join(tmp, "inner")
	curGit(t, "init", "-q", "-b", "main", inner)
	writeFile(t, filepath.Join(inner, "g"), "one\n")
	curGit(t, "-C", inner, "add", "-A")
	curGit(t, "-C", inner, "commit", "-qm", "inner one")
	i1 = curGit(t, "-C", inner, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(inner, "g"), "two\n")
	curGit(t, "-C", inner, "add", "-A")
	curGit(t, "-C", inner, "commit", "-qm", "inner two")

	up := filepath.Join(tmp, "up")
	curGit(t, "init", "-q", "-b", "main", up)
	// c1 carries the NESTED GITLINK for `inner`, one commit BEHIND inner's main.
	writeFile(t, filepath.Join(up, ".gitmodules"),
		"[submodule \"inner\"]\n\tpath = inner\n\turl = ../inner\n\tbranch = main\n")
	writeFile(t, filepath.Join(up, "f"), "one\n")
	curGit(t, "-C", up, "add", ".gitmodules", "f")
	cmd := exec.Command("git", "-C", up, "update-index", "--add", "--cacheinfo", "160000,"+i1+",inner")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("update-index inner: %v\n%s", err, out)
	}
	curGit(t, "-C", up, "commit", "-qm", "one, with a nested gitlink")
	c1 = curGit(t, "-C", up, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(up, "f"), "two\n")
	curGit(t, "-C", up, "add", "-A")
	curGit(t, "-C", up, "commit", "-qm", "two")
	c2 = curGit(t, "-C", up, "rev-parse", "HEAD")

	dir = filepath.Join(tmp, "super")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run = gitRepo(t, dir)
	// `src` is a real checkout of `up` sitting one commit BEHIND its main — the state
	// charly's own box/* gitlinks are in today — and it carries its OWN checkout of the
	// nested pin, also behind, because a repo does not hold its submodules' objects.
	curGit(t, "clone", "-q", up, filepath.Join(dir, "src"))
	curGit(t, "-C", filepath.Join(dir, "src"), "checkout", "-q", c1)
	curGit(t, "clone", "-q", inner, filepath.Join(dir, "src", "inner"))
	curGit(t, "-C", filepath.Join(dir, "src", "inner"), "checkout", "-q", i1)
	writeFile(t, filepath.Join(dir, ".gitmodules"),
		"[submodule \"src\"]\n\tpath = src\n\turl = ../up\n\tbranch = main\n"+
			"[submodule \"d\"]\n\tpath = d\n\turl = ../up\n\tbranch = main\n")
	run("add", ".gitmodules")
	return dir, run, c1, c2, i1
}

// curRecord writes a gitlink straight into the superproject's index — no checkout, no
// clone, exactly as the other fixture tests do.
func curRecord(t *testing.T, dir, path, sha string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "update-index", "--add", "--cacheinfo", "160000,"+sha+","+path)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("update-index %s: %v\n%s", path, err, out)
	}
}

// TestGitSubmodules_CurrencyIsNotConsistency pins the clause that makes CURRENCY a
// first-class question rather than a by-product of consistency
// (opencharly/charly#936).
//
// `verify` asserts that a checkout matches the gitlink the INDEX records; it passes on a
// tree whose every pin is behind the ref its repo serves — MEASURED, `verify` was green
// while five of six `distro-*` were behind. This mode answers the other question, from the
// recorded gitlink and the REMOTE alone, names the advance as a LEG, and refuses the
// structural states outright. The upstream is a LOCAL repository path, so the walk is
// exercised live without network access.
func TestGitSubmodules_CurrencyIsNotConsistency(t *testing.T) {
	tmp := t.TempDir()
	dir, run, c1, c2, _ := curSuper(t, tmp)
	curRecord(t, dir, "src", c1)
	curRecord(t, dir, "d", c2) // a second pin, already current: isolates the arm below
	run("commit", "-qm", "src behind its main, d current")

	// Arm 1 — a LEAF behind the ref it names, beside one that is current. The advance is a
	// REACHABLE advance, so the run PASSES and the Message LEADS with the count, naming the
	// advance as a leg. (A gate that failed here would be permanently red on a healthy
	// cross-repo sequence, which is how a gate teaches its readers to ignore it.)
	st, msg := runMaintenanceVerbIn(dir, "git-submodules", map[string]any{"mode": "currency"})
	if st != spec.StatusPass {
		t.Fatalf("a reachable advance is a leg, not a defect; got %s: %s", st, msg)
	}
	for _, want := range []string{
		"1 of 2 pin(s) NOT current",
		"1 behind",
		"src (" + c1[:10] + " -> " + c2[:10] + ")",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the report must lead with what is not current; missing %q in:\n%s", want, msg)
		}
	}
	// The intent is REPORTED, not left silent: this path declares none, so the intent that
	// `bump` actually implements is stated as implicit.
	if !strings.Contains(msg, "implicit: .gitmodules branch=") {
		t.Fatalf("an implicit intent must be stated in the report, got:\n%s", msg)
	}

	// Arm 2 — the same pin at the served ref: current, and it says so.
	curRecord(t, dir, "src", c2)
	st, msg = runMaintenanceVerbIn(dir, "git-submodules", map[string]any{"mode": "currency"})
	if st != spec.StatusPass {
		t.Fatalf("a pin at the served ref must pass: %s: %s", st, msg)
	}
	if !strings.Contains(msg, "0 of 2 pin(s) NOT current [2 cur]") ||
		!strings.Contains(msg, "legs: none") {
		t.Fatalf("a current pin must read as current with no legs, got:\n%s", msg)
	}
}

// TestGitSubmodules_CurrencyReportsBlockedBy proves the state the contract being replaced
// could not express: an EDGE whose recorded value is CORRECT while its SOURCE is behind.
// The run PASSES — the edge is a leg of a producer-first sequence — and the leg order names
// the producer, which is the reason a session no longer has to discover it an hour later.
func TestGitSubmodules_CurrencyReportsBlockedBy(t *testing.T) {
	tmp := t.TempDir()
	dir, run, c1, c2, i1 := curSuper(t, tmp)
	curRecord(t, dir, "src", c1)
	curRecord(t, dir, "d", i1)
	run("commit", "-qm", "src behind its main, d derived from src's own nested pin")

	st, msg := runMaintenanceVerbIn(dir, "git-submodules", map[string]any{
		"mode": "currency", "pinned_from": "src", "pin_map": map[string]any{"d": "inner"},
	})
	if st != spec.StatusPass {
		t.Fatalf("an edge over a behind source is a LEG, not a defect; got %s: %s", st, msg)
	}
	for _, want := range []string{
		"blocked-by src:inner (behind)",
		"1 behind",
		"src (" + c1[:10] + " -> " + c2[:10] + ") -> d (blocked-by src:inner (behind))",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the producer leg must be NAMED and ordered first; missing %q in:\n%s", want, msg)
		}
	}
}

// TestGitSubmodules_CurrencyRefusesStructuralStates pins the three states the walk must
// refuse rather than report: a pin it cannot judge at all, an edge whose two sides
// disagree, and a graph deeper than one derived hop. The depth rule is the whole difference
// from the contract being replaced, which was satisfiable at BOTH ends while both ends were
// stale.
func TestGitSubmodules_CurrencyRefusesStructuralStates(t *testing.T) {
	tmp := t.TempDir()
	dir, run, c1, _, i1 := curSuper(t, tmp)
	curRecord(t, dir, "src", c1)
	curRecord(t, dir, "d", i1)
	run("commit", "-qm", "base")

	// UNPINNED — a path with no intent and no `branch=`: the workflow cannot judge it, and
	// the failure says exactly what to declare rather than treating it as a leaf.
	writeFile(t, filepath.Join(dir, ".gitmodules"),
		"[submodule \"src\"]\n\tpath = src\n\turl = ../up\n\tbranch = main\n"+
			"[submodule \"ghost\"]\n\tpath = ghost\n\turl = ../up\n")
	run("add", ".gitmodules")
	curRecord(t, dir, "ghost", c1)
	st, msg := runMaintenanceVerbIn(dir, "git-submodules", map[string]any{"mode": "currency"})
	if st != spec.StatusFail {
		t.Fatalf("an unjudgeable pin must fail loud, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "ghost: no intent and no `branch=`") || !strings.Contains(msg, "unpinned") {
		t.Fatalf("the failure must name the unpinned path and what to declare, got:\n%s", msg)
	}

	// PIN-MISMATCH — an edge whose two sides disagree. This is the OLD contract's violation,
	// and it survives here as one NAMED state rather than as the whole contract. The source
	// is "." (this superproject, an external checkout): its own HEAD records `src` at c1
	// while the index records c2.
	curRecord(t, dir, "src", strings.Repeat("c", 40))
	run("commit", "-qm", "record c1-era value in HEAD")
	curRecord(t, dir, "src", strings.Repeat("d", 40))
	st, msg = runMaintenanceVerbIn(dir, "git-submodules", map[string]any{
		"mode": "currency", "pinned_from": ".", "pin_map": map[string]any{"src": "src"},
	})
	if st != spec.StatusFail {
		t.Fatalf("an edge whose sides disagree must fail, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "pin-mismatch: src records") {
		t.Fatalf("the mismatch must be named as such, got:\n%s", msg)
	}

	// DEPTH — an edge may not point at an edge: the graph is acyclic BY CONSTRUCTION.
	st, msg = runMaintenanceVerbIn(dir, "git-submodules", map[string]any{
		"mode": "currency", "pinned_from": "src", "pin_map": map[string]any{"src": "inner"},
	})
	if st != spec.StatusFail {
		t.Fatalf("a derived edge as a derived source must be refused, got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "graph depth > 1") {
		t.Fatalf("the depth refusal must name itself, got:\n%s", msg)
	}
}

// TestGitSubmodules_CurrencyJudgesAnExplicitDerivedFrom proves the SECOND edge shape the
// schema documents: `derived-from:<path>` names a source pin OF THIS REPO, so its value is
// that pin's recorded gitlink read HERE and its currency is that pin's own state — not a
// question about `pinned_from`, which such an intent never names.
func TestGitSubmodules_CurrencyJudgesAnExplicitDerivedFrom(t *testing.T) {
	tmp := t.TempDir()
	dir, run, c1, _, _ := curSuper(t, tmp)
	curRecord(t, dir, "src", c1)
	curRecord(t, dir, "d", c1) // d's claim: "my value is src's"
	run("commit", "-qm", "d derives from src in-repo")

	// src is BEHIND its main, so an edge that correctly mirrors a behind source is a LEG.
	st, msg := runMaintenanceVerbIn(dir, "git-submodules", map[string]any{
		"mode": "currency", "intent": map[string]any{"d": "derived-from:src"},
	})
	if st != spec.StatusPass {
		t.Fatalf("an in-repo edge over a behind source is a leg; got %s: %s", st, msg)
	}
	for _, want := range []string{"blocked-by src (behind)", "derived-from:src"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the in-repo edge must name its own source and state; missing %q in:\n%s", want, msg)
		}
	}

	// A source that is NOT a declared pin cannot be inherited from: named, and refused.
	st, msg = runMaintenanceVerbIn(dir, "git-submodules", map[string]any{
		"mode": "currency", "intent": map[string]any{"d": "derived-from:nowhere"},
	})
	if st != spec.StatusFail || !strings.Contains(msg, `derived-from source "nowhere" records no gitlink`) {
		t.Fatalf("an undeclared derived source must be refused BY NAME; got %s: %s", st, msg)
	}
}

// TestGitSubmodules_CurrencyNamesTheSquashAdvance pins the class a first cut got wrong: a pin
// taken before a SQUASH merge is unreachable from the served ref, because the branch advanced
// without descending from its previous head. MEASURED on the real umbrella, where `charly`'s
// own history is squash-merged and the honest state is "advance the pin" — not a divergence,
// and NOT a failure. A hand-pin to an unrelated commit produces the same two SHAs, so the
// report names the class and the remedy that is right for both.
func TestGitSubmodules_CurrencyNamesTheSquashAdvance(t *testing.T) {
	tmp := t.TempDir()
	up := filepath.Join(tmp, "up")
	curGit(t, "init", "-q", "-b", "main", up)
	writeFile(t, filepath.Join(up, "f"), "one\n")
	curGit(t, "-C", up, "add", "-A")
	curGit(t, "-C", up, "commit", "-qm", "before the squash")
	before := curGit(t, "-C", up, "rev-parse", "HEAD")
	// A squash merge: main advances to a commit that does NOT descend from `before`.
	curGit(t, "-C", up, "checkout", "-q", "--orphan", "squashed")
	curGit(t, "-C", up, "commit", "-q", "--allow-empty", "-m", "squashed")
	curGit(t, "-C", up, "branch", "-q", "-f", "main", "squashed")

	dir := filepath.Join(tmp, "super")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := gitRepo(t, dir)
	curGit(t, "clone", "-q", up, filepath.Join(dir, "leaf"))
	curGit(t, "-C", filepath.Join(dir, "leaf"), "checkout", "-q", before)
	writeFile(t, filepath.Join(dir, ".gitmodules"),
		"[submodule \"leaf\"]\n\tpath = leaf\n\turl = ../up\n\tbranch = main\n")
	run("add", ".gitmodules")
	curRecord(t, dir, "leaf", before)
	run("commit", "-qm", "pin taken before the squash")

	st, msg := runMaintenanceVerbIn(dir, "git-submodules", map[string]any{"mode": "currency"})
	if st != spec.StatusPass {
		t.Fatalf("a squash advance is a leg, not a defect; got %s: %s", st, msg)
	}
	for _, want := range []string{"not-on-the-served-line", "squash-merged branch", "1 of 1 pin(s) NOT current"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the class must be named with its remedy; missing %q in:\n%s", want, msg)
		}
	}
}

// TestGitSubmodules_CurrencyRefusesDepthForBOTHEdgeShapes is the arm validator round 2 asked
// for. The depth rule is about the GRAPH, so it must hold for EVERY edge source; round 1
// read `pinned_from` only, which left the EXPLICIT `derived-from:<path>` shape unguarded
// while `schema/task.cue` promised "An edge may NOT point at another edge (depth <= 2): the
// graph is acyclic BY CONSTRUCTION".
//
// BOTH arms FAIL on the pre-change source (`42b76d4`), where the guard was
// `if in.PinnedFrom != "" && len(in.PinMap) > 0` — it fired only when PINNED_FROM was itself
// an edge, so in both arms below it either did not run at all or read a field that was never
// an edge.
func TestGitSubmodules_CurrencyRefusesDepthForBOTHEdgeShapes(t *testing.T) {
	tmp := t.TempDir()
	dir, run, c1, _, _ := curSuper(t, tmp)
	// Three pins, so an edge can name EITHER shape of source: a `pin_map` ENTRY, and a path
	// that DECLARES a `derived-from:` intent.
	writeFile(t, filepath.Join(dir, ".gitmodules"),
		"[submodule \"src\"]\n\tpath = src\n\turl = ../up\n\tbranch = main\n"+
			"[submodule \"d\"]\n\tpath = d\n\turl = ../up\n\tbranch = main\n"+
			"[submodule \"e\"]\n\tpath = e\n\turl = ../up\n\tbranch = main\n")
	run("add", ".gitmodules")
	for _, p := range []string{"src", "d", "e"} {
		curRecord(t, dir, p, c1)
	}
	run("commit", "-qm", "three pins")

	// Both SHAPES are driven as subtests, so ONE failing arm cannot hide the other's verdict.
	for _, arm := range []struct {
		name     string
		in       map[string]any
		shape    string // the source's shape, as the refusal must name it
		refusal  string // the state the refused edge must report
		sourceIn string // the path the refusal must name as the edge's source
	}{
		{
			// SHAPE 1 — an EXPLICIT edge whose source is a `pin_map` ENTRY. This is the shape
			// the verdict named: with `pin_map: {d: inner}` and `intent: {e: "derived-from:d"}`,
			// `e`'s source `d` IS an edge — and `pinned_from` is empty, so the round-1 guard,
			// which read `pinned_from`, never ran.
			name: "explicit edge onto a `pin_map` entry",
			in: map[string]any{
				"mode":    "currency",
				"pin_map": map[string]any{"d": "inner"},
				"intent":  map[string]any{"e": "derived-from:d"},
			},
			shape:    "a `pin_map` entry (d -> inner)",
			refusal:  "blocked-by d (itself an edge)",
			sourceIn: `derives from "d"`,
		},
		{
			// SHAPE 2 — an EXPLICIT edge whose source DECLARES a `derived-from:` intent.
			// `pinned_from` and `pin_map` are BOTH empty, so there is no field the round-1
			// guard read that could see it: the depth-3 chain `d -> e -> src` went through.
			name: "explicit edge onto a declaring edge",
			in: map[string]any{
				"mode":   "currency",
				"intent": map[string]any{"d": "derived-from:e", "e": "derived-from:src"},
			},
			shape:    `declares intent "derived-from:src"`,
			refusal:  "blocked-by e (itself an edge)",
			sourceIn: `derives from "e"`,
		},
	} {
		t.Run(arm.name, func(t *testing.T) {
			st, msg := runMaintenanceVerbIn(dir, "git-submodules", arm.in)
			if st != spec.StatusFail {
				t.Fatalf("an edge over an edge must be refused; got %s: %s", st, msg)
			}
			for _, want := range []string{
				"graph depth > 1",
				"which is ITSELF an edge",
				"an edge may not point at an edge",
				arm.sourceIn,
				arm.shape,
			} {
				if !strings.Contains(msg, want) {
					t.Fatalf("the refusal must name the rule and the source's shape; missing %q in:\n%s", want, msg)
				}
			}
			// The refused pin must SAY why, rather than reporting pass 1's internal
			// placeholder: `pending-edge` is not a contract state, and leaving it in the report
			// would make the refusal's own state depend on the ORDER `paths` is walked in.
			if strings.Contains(msg, "pending-edge") {
				t.Fatalf("a refused edge must not report an internal placeholder as its state:\n%s", msg)
			}
			if !strings.Contains(msg, arm.refusal) {
				t.Fatalf("the refused edge must name its own refusal as its state; missing %q in:\n%s", arm.refusal, msg)
			}
		})
	}
}

// curStale builds the fixture the staleness arms need: a superproject whose `src` pin is
// either behind its upstream (`behind`) or at it (`current`), whose `d` pin is current, and
// whose LAST commit — the one that touched the pins — is dated `ago` in the past.
//
// The date is the fixture's whole point: the age of this repo's own bookkeeping for a pin
// is read from that commit, so the same tree is the healthy regime or the neglect regime
// depending only on when it was last advanced.
func curStale(t *testing.T, tmp, name string, behind bool, ago time.Duration) (dir, c1, c2 string) {
	t.Helper()
	dir, run, c1, c2, _ := curSuper(t, filepath.Join(tmp, name))
	curRecord(t, dir, "d", c2) // current: isolates the arm under test
	if behind {
		curRecord(t, dir, "src", c1)
	} else {
		curRecord(t, dir, "src", c2)
	}
	stamp := time.Now().Add(-ago).UTC().Format(time.RFC3339)
	t.Setenv("GIT_AUTHOR_DATE", stamp)
	t.Setenv("GIT_COMMITTER_DATE", stamp)
	run("commit", "-qm", "record the pins")
	return dir, c1, c2
}

// TestGitSubmodules_CurrencyStalenessBoundsNeglect proves the control that the recurring
// stale twin needed — a bound on NEGLECT, not on churn.
//
// The measurement behind it: a twin whose upstream moves daily is behind its served ref
// roughly 75% of wall-clock time, with continuous behind-windows of 8 and 10 days. A gate
// that failed on the DISTANCE would therefore be red on a healthy tree, which trains every
// reader to ignore it. What actually recurred was charly's own ADVANCE CADENCE: gaps of
// 8 and 10 days with no advance at all, against a healthy maximum of 5 days.
//
// So the bound is on the AGE of this repo's last advance of a pin, and the arms below are
// a one-variable differential: the same tree, the same pin, the same bound — only the date
// of the last advance differs, and the verdict flips with it.
func TestGitSubmodules_CurrencyStalenessBoundsNeglect(t *testing.T) {
	tmp := t.TempDir()
	bound := "168h"

	// Arm 1 — the tree as it is, with the knob UNSET: the verdict is the structural one
	// alone, exactly as before the knob existed. The opt-in is real, and a repo that has
	// not declared a bound sees no change at all.
	oldDir, _, _ := curStale(t, tmp, "old", true, 240*time.Hour)
	st, msg := runMaintenanceVerbIn(oldDir, "git-submodules", map[string]any{"mode": "currency"})
	if st != spec.StatusPass {
		t.Fatalf("without a declared bound the verdict must be unchanged; got %s: %s", st, msg)
	}
	if !strings.Contains(msg, "1 of 2 pin(s) NOT current") || strings.Contains(msg, "STALE") {
		t.Fatalf("an undeclared bound must not mention staleness; got:\n%s", msg)
	}

	// Arm 2 — the same tree WITH the bound: the pin is behind AND this repo has not
	// advanced it for 240h, which is past the 168h bound, so it is STALE and FAILS.
	st, msg = runMaintenanceVerbIn(oldDir, "git-submodules", map[string]any{
		"mode": "currency", "stale_after": bound,
	})
	if st != spec.StatusFail {
		t.Fatalf("a pin neglected for longer than the bound must fail; got %s: %s", st, msg)
	}
	for _, want := range []string{
		"1 STALE pin(s) (neglected for more than 168h)",
		"src: behind, and this repo has not advanced it for 240h",
		"(bound 168h)",
		"FAILED on the STALE pin(s) above",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the stale verdict must name the pin, its age and the bound; missing %q in:\n%s", want, msg)
		}
	}

	// Arm 3 — THE DIFFERENTIAL. The same tree, the same behind pin, the same bound, and the
	// ONLY difference is when this repo last advanced it: an advance a minute ago is
	// CHURN-IN-PROGRESS, not neglect, so the run PASSES. A control that failed here would
	// be red on a healthy tree, which is the failure this knob exists to avoid.
	freshDir, _, _ := curStale(t, tmp, "fresh", true, time.Minute)
	st, msg = runMaintenanceVerbIn(freshDir, "git-submodules", map[string]any{
		"mode": "currency", "stale_after": bound,
	})
	if st != spec.StatusPass {
		t.Fatalf("a pin advanced a minute ago is churn, not neglect; got %s: %s", st, msg)
	}
	if strings.Contains(msg, "STALE") {
		t.Fatalf("a recently advanced pin must not be stale; got:\n%s", msg)
	}
	if !strings.Contains(msg, "1 of 2 pin(s) NOT current") {
		t.Fatalf("the not-current count must still be reported; got:\n%s", msg)
	}

	// Arm 4 — the IDLE-UPSTREAM trap: a pin that is CURRENT but was advanced a long time
	// ago. An upstream that has not moved is not neglected, and the bound must not fire on
	// it — otherwise every quiet repo in the org goes red on its own silence.
	idleDir, _, _ := curStale(t, tmp, "idle", false, 240*time.Hour)
	st, msg = runMaintenanceVerbIn(idleDir, "git-submodules", map[string]any{
		"mode": "currency", "stale_after": bound,
	})
	if st != spec.StatusPass {
		t.Fatalf("a current pin is never stale, however old its last advance; got %s: %s", st, msg)
	}
	if strings.Contains(msg, "STALE") {
		t.Fatalf("a current pin must not be reported stale; got:\n%s", msg)
	}

	// Arm 5 — a malformed bound is a loud failure, never a silently disabled control.
	st, msg = runMaintenanceVerbIn(oldDir, "git-submodules", map[string]any{
		"mode": "currency", "stale_after": "7d",
	})
	if st != spec.StatusFail || !strings.Contains(msg, "not a duration") {
		t.Fatalf("a malformed bound must fail loud; got %s: %s", st, msg)
	}
}
