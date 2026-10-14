package task

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// The bump summary must name WHAT CHANGED, and it must be legible when it fails.
//
// Before the fix the summary printed len(done) as "bumped N submodule pin(s)", where `done` accumulated the
// phase-1 ROLLS (every non-pin_map submodule, 422 of 428 on a real umbrella) and never distinguished them
// from the phase-2 PINS (the pin_map entries, <= 6). Measured on the live umbrella: "bumped 422 submodule
// pin(s)" after staging exactly two gitlinks.
//
// This test drives runMaintenanceVerbIn -- the function the gate itself uses -- rather than a helper beside
// the call site, because a test beside the call site passes after the very regression it exists to catch.
// (That mistake is recorded in nested_checkout_test.go's own comment; it is not repeated here.)
//
// Coverage fails without the change: the pre-fix PASS text contains neither "rolling" nor a pin count.
func TestGitSubmodules_BumpSummaryNamesPinsAndRolls(t *testing.T) {
	umb := t.TempDir()
	gitIn(t, umb, "init", "-q", "-b", "main")
	gitIn(t, umb, "config", "user.email", "a@b.c")
	gitIn(t, umb, "config", "user.name", "t")

	// One real submodule, so phase 1 has something to roll.
	sub := t.TempDir()
	gitIn(t, sub, "init", "-q", "-b", "main")
	gitIn(t, sub, "config", "user.email", "a@b.c")
	gitIn(t, sub, "config", "user.name", "t")
	writeFile(t, filepath.Join(sub, "f"), "one")
	gitIn(t, sub, "add", "f")
	gitIn(t, sub, "commit", "-qm", "c1")
	subHead := strings.TrimSpace(gitIn(t, sub, "rev-parse", "HEAD"))

	gitIn(t, umb, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "child")
	gitIn(t, umb, "commit", "-qm", "add child")

	// Advance the submodule so the roll has something to move to.
	writeFile(t, filepath.Join(sub, "f"), "two")
	gitIn(t, sub, "add", "f")
	gitIn(t, sub, "commit", "-qm", "c2")
	if subHead == strings.TrimSpace(gitIn(t, sub, "rev-parse", "HEAD")) {
		t.Fatal("fixture: the submodule did not advance")
	}

	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{"mode": "bump"})
	if st != spec.StatusPass {
		t.Fatalf("bump must PASS on a clean fixture, got %s: %s", st, msg)
	}
	// NO gitlink assertion here, deliberately. Phase 1 rolls each submodule to `origin/<branch>`, and a
	// submodule created from a local TempDir has no origin remote, so `submoduleBranch` returns "" and the
	// path is skipped. The roll behaviour is asserted by nested_checkout_test.go against a fixture that does
	// supply remotes; what THIS test owns is the summary's wording -- the behaviour R7 requires coverage of.
	// (Measured: with the gitlink assertion, this test failed on the fixture, and both message assertions below
	// had already passed.)
	// THE ASSERTION: rolls and pins are reported as distinct quantities, not one "bumped" count.
	if !strings.Contains(msg, "rolling") {
		t.Fatalf("bump summary does not report ROLLS separately; the pre-fix wording named only a bumped count: %q", msg)
	}
	if strings.HasPrefix(msg, "bumped ") {
		t.Fatalf("bump summary still reports rolls as pins bumped: %q", msg)
	}
}
