package task

import (
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// The FAILURE report must LEAD, because the runner truncates long messages.
//
// #477's live run printed 422 submodule names and then failed with rc=1 and no visible reason: the
// "; %d FAILED: %s" clause was appended AFTER the name list and the runner cut it off. The verb's own comment
// promises the opposite -- "every failure is reported together at the end, so the run's own output names all of
// them" -- and an ordering that puts 422 names first makes that promise unkeepable.
//
// This test drives runMaintenanceVerbIn -- the function the gate itself uses -- so it fails if the summary
// regresses to names-first. Coverage fails without the change: the pre-fix StatusFail string began
// "bumped %d submodule pin(s): <names>; %d FAILED: ...", i.e. the names led and the count of failures did not.
func TestGitSubmodules_BumpSummaryLeadsWithFailures(t *testing.T) {
	umb := t.TempDir()
	gitIn(t, umb, "init", "-q", "-b", "main")
	gitIn(t, umb, "config", "user.email", "a@b.c")
	gitIn(t, umb, "config", "user.name", "t")

	// A real submodule for phase 1 to roll.
	sub := t.TempDir()
	gitIn(t, sub, "init", "-q", "-b", "main")
	gitIn(t, sub, "config", "user.email", "a@b.c")
	gitIn(t, sub, "config", "user.name", "t")
	writeFile(t, sub+"/f", "one")
	gitIn(t, sub, "add", "f")
	gitIn(t, sub, "commit", "-qm", "c1")
	gitIn(t, umb, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "child")
	gitIn(t, umb, "commit", "-qm", "add child")

	// A pin_map whose target has NO gitlink in the pinned_from: `pinSubmodule` cannot resolve it, so the verb
	// must record a failure and return StatusFail. This reaches the failing branch without needing a contended
	// lock, which a fixture cannot produce.
	st, msg := runMaintenanceVerbIn(umb, "git-submodules", map[string]any{
		"mode":        "bump",
		"pinned_from": "child",
		"pin_map":     map[string]any{"child": "does/not/exist"},
	})
	if st != spec.StatusFail {
		t.Fatalf("a pin_map entry with no gitlink must FAIL, got %s: %s", st, msg)
	}
	// THE ASSERTION, and it is about ORDER: the failure count is the first thing in the message.
	if !strings.HasPrefix(msg, "1 pin(s) FAILED") {
		t.Fatalf("the failure report does not LEAD; a long name list in front of it is what the runner truncates: %q", msg)
	}
	// And the reason is present, not merely a count.
	// The cause must be present, whichever branch recorded it: an unresolvable gitlink ("no gitlink") or a
	// pinned_from that did not roll (the EDGE refusal). Both are real causes; the assertion is that the
	// message carries ONE of them rather than only a count.
	if !strings.Contains(msg, "refused to pin") && !strings.Contains(msg, "no gitlink") {
		t.Fatalf("the failure report leads but names no cause: %q", msg)
	}
}
