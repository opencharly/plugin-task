package task

import (
	"context"
	"encoding/json"
	"testing"

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

// TestExpandParams proves ${VAR} / $VAR substitution for dir resolution.
func TestExpandParams(t *testing.T) {
	got := expandParams("${ROOT}/src/$SUBDIR", map[string]string{"ROOT": "/w", "SUBDIR": "x"})
	if got != "/w/src/x" {
		t.Fatalf("expandParams = %q, want /w/src/x", got)
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
