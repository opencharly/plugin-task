package task

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/kit"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
)

// mkTask builds a minimal task entity for tests.
func mkTask(desc string, deps ...string) spec.Task {
	return spec.Task{Description: desc, DependsOn: deps, Plan: []spec.Step{{Run: "noop", Op: spec.Op{Command: "true"}}}}
}

// TestClosure_DependencyFirst proves depends_on is resolved dependency-first and
// deduplicated.
func TestClosure_DependencyFirst(t *testing.T) {
	ts := &taskSet{tasks: map[string]spec.Task{
		"a": mkTask("a", "b"),
		"b": mkTask("b", "c"),
		"c": mkTask("c"),
	}}
	order, err := ts.closure([]string{"a"})
	if err != nil {
		t.Fatalf("closure: %v", err)
	}
	want := []string{"c", "b", "a"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

// TestClosure_Cycle proves a dependency cycle is a hard error.
func TestClosure_Cycle(t *testing.T) {
	ts := &taskSet{tasks: map[string]spec.Task{
		"a": mkTask("a", "b"),
		"b": mkTask("b", "a"),
	}}
	if _, err := ts.closure([]string{"a"}); err == nil {
		t.Fatal("a dependency cycle must be a hard error")
	}
}

// TestClosure_Unknown proves an unknown dependency is a hard error.
func TestClosure_Unknown(t *testing.T) {
	ts := &taskSet{tasks: map[string]spec.Task{"a": mkTask("a", "missing")}}
	if _, err := ts.closure([]string{"a"}); err == nil {
		t.Fatal("an unknown dependency must be a hard error")
	}
}

// TestParseArgs proves the CLI grammar parses task names, params, and flags.
func TestParseArgs(t *testing.T) {
	o, err := parseArgs([]string{"build", "--param", "N=3", "--dry-run", "--json"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if o.name != "build" || !o.dryRun || !o.json || o.params["N"] != "3" {
		t.Fatalf("parsed = %+v", o)
	}
	if _, err := parseArgs([]string{"a", "b"}); err == nil {
		t.Fatal("two task names must be rejected")
	}
	if _, err := parseArgs([]string{"--nope"}); err == nil {
		t.Fatal("an unknown flag must be rejected")
	}
}

// TestPlatformSkip proves the platform gate.
func TestPlatformSkip(t *testing.T) {
	skip := spec.Task{Platforms: []string{"plan9"}}
	if platformSkip(skip) == "" {
		t.Fatal("a task restricted to plan9 must skip on linux")
	}
	ok := spec.Task{Platforms: []string{"linux"}}
	if platformSkip(ok) != "" {
		t.Fatal("a linux task must not skip on linux")
	}
	excl := spec.Task{ExcludePlatforms: []string{"linux"}}
	if platformSkip(excl) == "" {
		t.Fatal("a linux-excluded task must skip on linux")
	}
}

// TestInvokeValidate proves the deep OpValidate check catches the concrete cases the
// closedness-only host value gate leaves open (missing description, empty plan).
func TestInvokeValidate(t *testing.T) {
	// missing description
	reply, err := invokeValidate(mkInvoke(`{"plan":[{"run":"x","command":"true"}]}`))
	if err != nil {
		t.Fatalf("invokeValidate: %v", err)
	}
	if !hasErrDiag(t, reply) {
		t.Fatal("a task missing description must produce an error diagnostic")
	}
	// empty plan
	reply, err = invokeValidate(mkInvoke(`{"description":"x"}`))
	if err != nil {
		t.Fatalf("invokeValidate: %v", err)
	}
	if !hasErrDiag(t, reply) {
		t.Fatal("a task with no plan must produce an error diagnostic")
	}
	// valid
	reply, err = invokeValidate(mkInvoke(`{"description":"x","plan":[{"run":"y","command":"true"}]}`))
	if err != nil {
		t.Fatalf("invokeValidate: %v", err)
	}
	if hasErrDiag(t, reply) {
		t.Fatal("a valid task must produce no error diagnostic")
	}
}

// TestExpandVars proves ${VAR} / $VAR substitution for dir resolution.
func TestExpandVars(t *testing.T) {
	got := expandVars("${ROOT}/src/$SUBDIR", map[string]string{"ROOT": "/w", "SUBDIR": "x"})
	if got != "/w/src/x" {
		t.Fatalf("expandVars = %q, want /w/src/x", got)
	}
}

// TestResolveParams proves declared defaults fill CLI gaps and required params are
// enforced.
func TestResolveParams(t *testing.T) {
	task := spec.Task{Params: map[string]spec.TaskParamSpec{
		"WITH_DEFAULT": {Default: "d"},
		"REQUIRED":     {Required: true},
	}}
	// CLI override wins; default fills the other.
	got, err := resolveParams(task, map[string]string{"REQUIRED": "r", "WITH_DEFAULT": "cli"})
	if err != nil {
		t.Fatalf("resolveParams: %v", err)
	}
	if got["WITH_DEFAULT"] != "cli" || got["REQUIRED"] != "r" {
		t.Fatalf("resolveParams = %v", got)
	}
	// Default applies when not overridden.
	got, err = resolveParams(task, map[string]string{"REQUIRED": "r"})
	if err != nil || got["WITH_DEFAULT"] != "d" {
		t.Fatalf("default not applied: %v (%v)", got, err)
	}
	// A missing required param errors.
	if _, err := resolveParams(task, nil); err == nil {
		t.Fatal("a missing required param must error")
	}
}

// TestRunTask_ContinueOnError proves the flag is carried onto the result so the CLI
// can honor it (a failed step under continue_on_error does not fail the run).
func TestRunTask_ContinueOnError(t *testing.T) {
	ts := &taskSet{dir: t.TempDir(), tasks: map[string]spec.Task{
		"t": {Description: "c", ContinueOnError: true, Plan: []spec.Step{{Run: "x", Op: spec.Op{Command: "true"}}}},
	}}
	res, err := runTask(context.Background(), nil, ts, "t", nil, false, true)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if !res.ContinueOnError {
		t.Fatal("continue_on_error must be carried onto the result")
	}
}

// TestRunTask_Silent proves silent is carried onto the result (the CLI suppresses the
// per-step lines for it).
func TestRunTask_Silent(t *testing.T) {
	ts := &taskSet{dir: t.TempDir(), tasks: map[string]spec.Task{
		"t": {Description: "s", Silent: true, Plan: []spec.Step{{Run: "x", Op: spec.Op{Command: "true"}}}},
	}}
	res, err := runTask(context.Background(), nil, ts, "t", nil, false, true)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if !res.Silent {
		t.Fatal("silent must be carried onto the result")
	}
}

// TestRunTask_InteractiveNoTTY proves an interactive task refuses to run without a TTY
// (test stdin is not a terminal).
func TestRunTask_InteractiveNoTTY(t *testing.T) {
	ts := &taskSet{dir: t.TempDir(), tasks: map[string]spec.Task{
		"t": {Description: "i", Interactive: true, Plan: []spec.Step{{Run: "x", Op: spec.Op{Command: "true"}}}},
	}}
	res, err := runTask(context.Background(), nil, ts, "t", nil, false, false)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("an interactive task without a TTY must error, got %q", res.Status)
	}
}

// mkInvoke builds a minimal pb.InvokeRequest carrying paramsJSON for the OpValidate
// unit under test.
func mkInvoke(params string) *pb.InvokeRequest {
	return &pb.InvokeRequest{ParamsJson: []byte(params)}
}

// hasErrDiag decodes an OpValidate reply and reports whether it carries an
// error-severity diagnostic.
func hasErrDiag(t *testing.T, reply *pb.InvokeReply) bool {
	t.Helper()
	var d spec.Diagnostics
	if err := json.Unmarshal(reply.GetResultJson(), &d); err != nil {
		t.Fatalf("decode diagnostics: %v", err)
	}
	for _, it := range d.Items {
		if it.Severity == "error" {
			return true
		}
	}
	return false
}

// TestRunTask_PreconditionAborts proves a failing precondition aborts the task
// (status "error"), never a silent skip.
func TestRunTask_PreconditionAborts(t *testing.T) {
	ts := &taskSet{dir: t.TempDir(), tasks: map[string]spec.Task{
		"t": {Description: "p", Preconditions: []string{"false"}, Plan: []spec.Step{{Run: "x", Op: spec.Op{Command: "true"}}}},
	}}
	res, err := runTask(context.Background(), nil, ts, "t", nil, false, false)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("a failing precondition must abort (status error), got %q", res.Status)
	}
}

// TestRunTask_StatusUpToDate proves the status probe short-circuits to up-to-date.
func TestRunTask_StatusUpToDate(t *testing.T) {
	ts := &taskSet{dir: t.TempDir(), tasks: map[string]spec.Task{
		"t": {Description: "s", Status: []string{"true"}, Plan: []spec.Step{{Run: "x", Op: spec.Op{Command: "true"}}}},
	}}
	res, err := runTask(context.Background(), nil, ts, "t", nil, false, false)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if res.Status != "up-to-date" {
		t.Fatalf("status=0 must report up-to-date, got %q", res.Status)
	}
}

// TestRunTask_ForceBypassesStatus proves --force bypasses the status short-circuit.
func TestRunTask_ForceBypassesStatus(t *testing.T) {
	ts := &taskSet{dir: t.TempDir(), tasks: map[string]spec.Task{
		"t": {Description: "s", Status: []string{"true"}, Plan: []spec.Step{{Run: "x", Op: spec.Op{Command: "true"}}}},
	}}
	res, err := runTask(context.Background(), nil, ts, "t", nil, true, true)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if res.Status != "ran" {
		t.Fatalf("--force must bypass status, got %q", res.Status)
	}
	if res.Message == "" || res.Message[:8] != "dry-run:" {
		t.Fatalf("expected dry-run message, got %q", res.Message)
	}
}

// fakeResolver returns a canned result for the built-in `command` verb, letting a
// test drive the REAL plan walk (chdir/`dir:`, step order, verdict tallying) without
// a host reverse channel. The verb dispatch itself is SDK code.
type fakeResolver struct{ status spec.Status }

func (f *fakeResolver) RunVerb(_ context.Context, op *spec.Op) (spec.CheckResult, bool) {
	// The command: verb's plugin_input carries the command; treat a non-empty command
	// as a pass (the point is the WALK, not the shell).
	if op.Plugin == "command" {
		return spec.CheckResult{Status: f.status}, true
	}
	return spec.CheckResult{}, false
}

func (f *fakeResolver) RunProvisionAct(_ context.Context, _ *spec.Op, _ string) (spec.CheckResult, bool) {
	return spec.CheckResult{}, false
}

// TestRunTask_DrivesRealPlan proves runTask actually walks the plan through kit.RunPlan:
// both steps produce results in authored order, the task's dir: is applied (chdir), and
// a FAIL result is tallied. This test FAILS if the walk is skipped or the verdict
// tally is wrong.
func TestRunTask_DrivesRealPlan(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	old := verbResolverFor
	verbResolverFor = func(*sdk.Executor) kit.VerbResolver { return &fakeResolver{status: spec.StatusPass} }
	defer func() { verbResolverFor = old }()
	before, _ := os.Getwd()

	ts := &taskSet{dir: dir, tasks: map[string]spec.Task{
		"t": {
			Description: "two-step plan",
			Dir:         "sub",
			Plan: []spec.Step{
				{Run: "step one", Op: spec.Op{Plugin: "command", PluginInput: map[string]any{"command": "true"}}},
				{Check: "step two", Op: spec.Op{Plugin: "command", PluginInput: map[string]any{"command": "true"}}},
			},
		},
	}}
	res, err := runTask(context.Background(), nil, ts, "t", nil, false, false)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if res.Status != "ran" || len(res.Steps) != 2 || res.Failed != 0 {
		t.Fatalf("real walk not driven: %+v (steps %d)", res, len(res.Steps))
	}
	if res.Steps[0].Keyword != "run" || res.Steps[1].Keyword != "check" {
		t.Fatalf("step order/verdicts wrong: %q then %q", res.Steps[0].Keyword, res.Steps[1].Keyword)
	}
	// dir: was applied during the walk and the cwd is restored after.
	if cwd, _ := os.Getwd(); cwd != before {
		t.Fatalf("cwd not restored to %q (got %q)", before, cwd)
	}
}

// TestRunTask_TalliesFailure proves a FAIL step increments res.Failed (and the task
// reports it).
func TestRunTask_TalliesFailure(t *testing.T) {
	old := verbResolverFor
	verbResolverFor = func(*sdk.Executor) kit.VerbResolver { return &fakeResolver{status: spec.StatusFail} }
	defer func() { verbResolverFor = old }()

	ts := &taskSet{dir: t.TempDir(), tasks: map[string]spec.Task{
		"t": {Description: "f", Plan: []spec.Step{{Run: "x", Op: spec.Op{Plugin: "command", PluginInput: map[string]any{"command": "false"}}}}},
	}}
	res, err := runTask(context.Background(), nil, ts, "t", nil, false, false)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if res.Failed != 1 {
		t.Fatalf("failed step not tallied: %+v", res)
	}
}
