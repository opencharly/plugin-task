package task

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/spec"
)

// The THIRD BUCKET (opencharly/plugin-task#35).
//
// A task's summary counted steps and failures. A step that RAN NOTHING — the plan
// runner's StatusSkip — was neither: it was not a failure, so it was counted among
// the steps, and it was not disclosed. The line then read "N step(s), 0 failed" over a
// run in which some of those steps never happened, and a reader took it for a green
// that had run.
//
// The tests below drive the REAL plan walk (a step authored `skip: true` is the plan
// runner's own skip path) and the REAL reporter, so they fail if the bucket is
// dropped anywhere: the tally, the headline, the silent path, the hop aggregate, the
// JSON, or the exit status.

// skippedTaskSet builds a taskSet whose task `name` runs one step that RAN NOTHING
// (authored `skip: true` — the plan runner's own StatusSkip path) and one step that
// passes.
func skippedTaskSet(dir, name string) *taskSet {
	return &taskSet{dir: dir, tasks: map[string]spec.Task{
		name: {Description: "a task with a step that ran nothing", Plan: []spec.Step{
			{
				Run: "the step that ran nothing",
				Op:  spec.Op{Plugin: "command", PluginInput: map[string]any{"command": "true"}, Skip: true},
			},
			{
				Run: "the step that ran",
				Op:  spec.Op{Plugin: "command", PluginInput: map[string]any{"command": "true"}},
			},
		}},
	}}
}

// TestRunTask_SkippedStepIsCountedAndNotFailed proves the tally: the skipped step
// lands in the THIRD bucket, not in "failed" and not in the step count alone.
func TestRunTask_SkippedStepIsCountedAndNotFailed(t *testing.T) {
	old := verbResolverFor
	verbResolverFor = func(*sdk.Executor) kit.VerbResolver { return &fakeResolver{status: spec.StatusPass} }
	defer func() { verbResolverFor = old }()

	ts := skippedTaskSet(t.TempDir(), "mixed")
	res, err := runTask(context.Background(), nil, ts, "mixed", nil, false, false)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if res.Skipped != 1 {
		t.Fatalf("the skipped step must be counted in the third bucket, got Skipped=%d", res.Skipped)
	}
	if res.Failed != 0 {
		t.Fatalf("a step that ran nothing must NOT be counted as a failure, got Failed=%d", res.Failed)
	}
	if len(res.Steps) != 2 {
		t.Fatalf("both steps must be reported, got %d", len(res.Steps))
	}
	if res.Steps[0].Result.Status != spec.StatusSkip {
		t.Fatalf("the skipped step must carry the plan runner's skip status, got %s", res.Steps[0].Result.Status)
	}
	if res.Steps[0].Result.Message == "" {
		t.Fatal("a skipped step must carry its reason — a count with no cause is not a disclosure")
	}
}

// TestPrintResultText_HeadlineNamesTheSkippedCount proves the HEADLINE carries the
// third bucket. The old headline named steps and failures only, which is what made a
// skipped step indistinguishable from a step that ran.
func TestPrintResultText_HeadlineNamesTheSkippedCount(t *testing.T) {
	r := &runResult{
		Name:    "mixed",
		Status:  "ran",
		Steps:   []spec.StepResult{{Keyword: "run", Text: "the step that ran nothing"}},
		Skipped: 1,
	}
	var buf bytes.Buffer
	printResultText(&buf, r)
	got := buf.String()
	if !strings.Contains(got, "task mixed: 1 step(s), 0 failed, 1 skipped") {
		t.Fatalf("the headline must name the skipped count, got:\n%s", got)
	}
}

// TestPrintResultText_SilentStillDisclosesTheSkippedStep proves the SILENT path
// keeps its meaning (no per-step output) while a skipped step still leaves a trace:
// a silent task that hid the skip would report a count with no cause, which is the
// reading this bucket exists to end.
func TestPrintResultText_SilentStillDisclosesTheSkippedStep(t *testing.T) {
	r := &runResult{
		Name:    "quiet",
		Status:  "ran",
		Silent:  true,
		Skipped: 1,
		Steps: []spec.StepResult{
			{Keyword: "run", Text: "the step that ran nothing", Result: spec.CheckResult{Status: spec.StatusSkip, Message: "skip: true"}},
			{Keyword: "run", Text: "the ordinary step", Result: spec.CheckResult{Status: spec.StatusPass, Message: "exit=0"}},
		},
	}
	var buf bytes.Buffer
	printResultText(&buf, r)
	got := buf.String()
	if !strings.Contains(got, "0 failed, 1 skipped") {
		t.Fatalf("the silent headline must still carry the skipped count, got:\n%s", got)
	}
	if !strings.Contains(got, "the step that ran nothing") || !strings.Contains(got, "skip: true") {
		t.Fatalf("the silent path must still disclose WHY the step did not run, got:\n%s", got)
	}
	if strings.Contains(got, "the ordinary step") {
		t.Fatalf("the silent path must keep suppressing the steps that ran, got:\n%s", got)
	}
}

// TestHopTasks_SkippedCountSurvivesTheHop proves the hop's AGGREGATE keeps the third
// bucket. The aggregate used to be dropped whenever the nested run had step messages
// and no failures — which is exactly the shape a skipped run has, so the count of
// what did not run was the thing that got dropped.
func TestHopTasks_SkippedCountSurvivesTheHop(t *testing.T) {
	old := verbResolverFor
	verbResolverFor = func(*sdk.Executor) kit.VerbResolver { return &fakeResolver{status: spec.StatusPass} }
	defer func() { verbResolverFor = old }()

	ts := skippedTaskSet(t.TempDir(), "mixed")

	st, msg := hopTasks(context.Background(), nil, ts, "mixed", nil)
	if st != spec.StatusPass {
		t.Fatalf("a task whose step ran nothing must still PASS, got %s", st)
	}
	if !strings.Contains(msg, "1 skipped") {
		t.Fatalf("the hop's aggregate must carry the skipped count, got:\n%s", msg)
	}
	if !strings.Contains(msg, "ran 1 task(s)") {
		t.Fatalf("the hop must keep the aggregate when a step ran nothing, got:\n%s", msg)
	}
}

// TestPrintResultsJSON_CarriesTheSkippedCount proves the JSON report carries the
// bucket too — a machine reader of `charly task --json` must see it as well.
func TestPrintResultsJSON_CarriesTheSkippedCount(t *testing.T) {
	results := []*runResult{{
		Name:    "mixed",
		Status:  "ran",
		Skipped: 1,
		Steps:   []spec.StepResult{{Keyword: "run", Text: "the step that ran nothing", Result: spec.CheckResult{Status: spec.StatusSkip}}},
	}}
	oldOut := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	jerr := printResultsJSON(results)
	_ = w.Close()
	os.Stdout = oldOut
	if jerr != nil {
		t.Fatalf("printResultsJSON: %v", jerr)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	var decoded []struct {
		Failed  int `json:"failed"`
		Skipped int `json:"skipped"`
	}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("the JSON report must decode: %v (%s)", err, buf.String())
	}
	if len(decoded) != 1 || decoded[0].Skipped != 1 {
		t.Fatalf("the JSON report must carry the skipped count, got %s", buf.String())
	}
}

// TestRunTaskCLI_SkippedStepExitsZero proves the EXIT STATUS stays honest: a step
// that ran nothing is not a failure, so the run exits 0 — while the summary says
// plainly that a step did not run.
func TestRunTaskCLI_SkippedStepExitsZero(t *testing.T) {
	old := verbResolverFor
	verbResolverFor = func(*sdk.Executor) kit.VerbResolver { return &fakeResolver{status: spec.StatusPass} }
	defer func() { verbResolverFor = old }()

	oldCache := entityCache
	entityCache = map[string]spec.Task{
		"mixed": {Description: "a task with a step that ran nothing", Plan: []spec.Step{
			{
				Run: "the step that ran nothing",
				Op:  spec.Op{Plugin: "command", PluginInput: map[string]any{"command": "true"}, Skip: true},
			},
		}},
	}
	defer func() { entityCache = oldCache }()

	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errW, _, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outW, errW
	runErr := runTaskCLI(context.Background(), nil, []string{"mixed"})
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	if runErr != nil {
		t.Fatalf("a task whose only step ran nothing must exit 0, got %v", runErr)
	}
	report, _ := io.ReadAll(outR)
	if !strings.Contains(string(report), "0 failed, 1 skipped") {
		t.Fatalf("the report must name the skipped count, got:\n%s", report)
	}
}
