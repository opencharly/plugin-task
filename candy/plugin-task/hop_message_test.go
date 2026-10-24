package task

import (
	"context"
	"strings"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/spec"
)

// hopMessageResolver returns a DIFFERENT message per step, so a test can prove the
// hop joins the step messages in PLAN order. The walk is sequential, so a counter is
// the step index.
type hopMessageResolver struct {
	messages []string
	n        int
}

func (r *hopMessageResolver) RunVerb(_ context.Context, op *spec.Op) (spec.CheckResult, bool) {
	if op.Plugin != "command" {
		return spec.CheckResult{}, false
	}
	m := ""
	if r.n < len(r.messages) {
		m = r.messages[r.n]
	}
	r.n++
	return spec.CheckResult{Status: spec.StatusPass, Message: m}, true
}

func (r *hopMessageResolver) RunProvisionAct(_ context.Context, _ *spec.Op, _ string) (spec.CheckResult, bool) {
	return spec.CheckResult{}, false
}

// hopTaskSet builds a taskSet whose task `name` runs the given step texts.
func hopTaskSet(dir, name string, steps ...string) *taskSet {
	plan := make([]spec.Step, 0, len(steps))
	for _, s := range steps {
		plan = append(plan, spec.Step{
			Run: s,
			Op:  spec.Op{Plugin: "command", PluginInput: map[string]any{"command": "true"}},
		})
	}
	return &taskSet{dir: dir, tasks: map[string]spec.Task{
		name: {Description: "the nested task", Plan: plan},
	}}
}

// TestHopTasks_CarriesTheWrappedRunsReport is the plugin-task#33 regression.
//
// The `task:` hop returned the aggregate "ran N task(s), M step(s) failed", so a
// caller that composed a verb through `task:` could see that it PASSED but never
// WHAT the wrapped run reported — the wrapped verb's counts, its per-pin states, its
// producer-first leg order. The pass/fail survived; the coverage did not. The
// umbrella's own `charly.yml` records the loss as filed here rather than paying for
// it by re-composing the verb (which is what produced the contradictory counts in
// opencharly/opencharly#563).
//
// This test drives hopTasks — the REAL hop, the function the verb's call site calls
// — so it fails if the composition regresses. (The plugin-task#12 lesson is exact: a
// test driving a helper BESIDE the call site passed even after the call site
// regressed to the bare aggregate.)
func TestHopTasks_CarriesTheWrappedRunsReport(t *testing.T) {
	const report = "verify: 6 policy pin(s) hold across 431 submodule(s); 431 checkout(s) clean at their gitlink"
	old := verbResolverFor
	verbResolverFor = func(*sdk.Executor) kit.VerbResolver {
		return &hopMessageResolver{messages: []string{report}}
	}
	defer func() { verbResolverFor = old }()

	ts := hopTaskSet(t.TempDir(), "pins", "run git-submodules in the selected mode")

	st, msg := hopTasks(context.Background(), nil, ts, "pins", nil)
	if st != spec.StatusPass {
		t.Fatalf("a healthy nested task must yield PASS, got %s", st)
	}
	if !strings.Contains(msg, report) {
		t.Fatalf("the hop must carry the wrapped run's own report, got:\n%s", msg)
	}
	// The aggregate must not crowd out the evidence: it is the fallback, not the
	// message, when the wrapped run had something to say.
	if strings.Contains(msg, "ran 1 task(s)") {
		t.Fatalf("the aggregate must not displace the wrapped run's report, got:\n%s", msg)
	}
}

// TestHopTasks_CarriesEveryStepMessageInPlanOrder proves the multi-step case: each
// step's own message crosses, in plan order, so a nested task that reported twice
// says both.
func TestHopTasks_CarriesEveryStepMessageInPlanOrder(t *testing.T) {
	old := verbResolverFor
	verbResolverFor = func(*sdk.Executor) kit.VerbResolver {
		return &hopMessageResolver{messages: []string{"first report", "second report"}}
	}
	defer func() { verbResolverFor = old }()

	ts := hopTaskSet(t.TempDir(), "pins", "the first step", "the second step")

	st, msg := hopTasks(context.Background(), nil, ts, "pins", nil)
	if st != spec.StatusPass {
		t.Fatalf("a healthy nested task must yield PASS, got %s", st)
	}
	if !strings.Contains(msg, "first report") || !strings.Contains(msg, "second report") {
		t.Fatalf("every step's message must cross the hop, got:\n%s", msg)
	}
	if strings.Index(msg, "first report") > strings.Index(msg, "second report") {
		t.Fatalf("the step messages must keep the plan order, got:\n%s", msg)
	}
}

// TestHopTasks_NoEvidenceKeepsTheAggregate proves the fallback: a nested task whose
// steps said nothing has no evidence to carry, so the hop keeps the aggregate rather
// than returning an empty message.
func TestHopTasks_NoEvidenceKeepsTheAggregate(t *testing.T) {
	old := verbResolverFor
	verbResolverFor = func(*sdk.Executor) kit.VerbResolver {
		return &hopMessageResolver{messages: []string{""}}
	}
	defer func() { verbResolverFor = old }()

	ts := hopTaskSet(t.TempDir(), "quiet", "a step that says nothing")

	st, msg := hopTasks(context.Background(), nil, ts, "quiet", nil)
	if st != spec.StatusPass {
		t.Fatalf("a healthy nested task must yield PASS, got %s", st)
	}
	if msg != "ran 1 task(s), 0 step(s) failed, 0 skipped" {
		t.Fatalf("a nested task with no evidence must keep the aggregate, got %q", msg)
	}
}

// TestHopTasks_FailurePathUnchanged proves the plugin-task#12 behaviour survives the
// change: a nested FAILURE still names the task, the count, the failing step's path
// and the leaf's own reason — the hop did not trade one loss for another.
func TestHopTasks_FailurePathUnchanged(t *testing.T) {
	old := verbResolverFor
	verbResolverFor = func(*sdk.Executor) kit.VerbResolver {
		return &fakeResolver{status: spec.StatusFail, message: "leaf-reason: boom"}
	}
	defer func() { verbResolverFor = old }()

	ts := hopTaskSet(t.TempDir(), "pins", "bump the pins")

	st, msg := hopTasks(context.Background(), nil, ts, "pins", nil)
	if st != spec.StatusFail {
		t.Fatalf("a failed nested task must yield FAIL, got %s", st)
	}
	for _, want := range []string{`task "pins" failed`, "1 step(s) failed", "[fail] run — bump the pins", "leaf-reason: boom"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in the nested verdict, got:\n%s", want, msg)
		}
	}
}

// TestHopTasks_UnknownTaskIsAFailure proves the hop's own errors still fail loud with
// the reason (the closure error), rather than degrading into a PASS.
func TestHopTasks_UnknownTaskIsAFailure(t *testing.T) {
	ts := &taskSet{dir: t.TempDir(), tasks: map[string]spec.Task{}}

	st, msg := hopTasks(context.Background(), nil, ts, "nope", nil)
	if st != spec.StatusFail {
		t.Fatalf("an unknown task must yield FAIL, got %s", st)
	}
	if !strings.Contains(msg, "nope") {
		t.Fatalf("the failure must name the unknown task, got:\n%s", msg)
	}
}
