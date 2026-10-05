package task

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/spec"
)

// TestParseArgs_OutputMutuallyExclusive proves --output parses and that combining it
// with --json is a hard error (each owns stdout with a different shape).
func TestParseArgs_OutputMutuallyExclusive(t *testing.T) {
	o, err := parseArgs([]string{"t", "--output"})
	if err != nil || !o.output {
		t.Fatalf("--output must parse: %+v (%v)", o, err)
	}
	if _, err := parseArgs([]string{"t", "--output", "--json"}); err == nil {
		t.Fatal("--output + --json must be a hard error")
	}
}

// TestRunTaskCLI_OutputMode drives the REAL CLI and proves --output emits the FINAL
// step's CapturedValue raw on stdout while the human report goes to stderr — the exact
// contract a lobster `run:` step relies on to read a charly step's value as `$id.stdout`
// / `$id.json`. This test FAILS if the value is wrapped, decorated, or routed to the
// report stream.
func TestRunTaskCLI_OutputMode(t *testing.T) {
	oldResolver := verbResolverFor
	verbResolverFor = func(*sdk.Executor) kit.VerbResolver {
		return &fakeResolver{status: spec.StatusPass, captured: "CAPTURED-RAW-VALUE"}
	}
	defer func() { verbResolverFor = oldResolver }()

	oldCache := entityCache
	entityCache = map[string]spec.Task{
		"capture": {Description: "produce a value", Plan: []spec.Step{
			{Run: "produce a value", Op: spec.Op{Plugin: "command", PluginInput: map[string]any{"command": "true"}}},
		}},
	}
	defer func() { entityCache = oldCache }()

	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outW, errW
	runErr := runTaskCLI(context.Background(), nil, []string{"capture", "--output"})
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	if runErr != nil {
		t.Fatalf("runTaskCLI --output: %v", runErr)
	}
	stdout, _ := io.ReadAll(outR)
	stderr, _ := io.ReadAll(errR)
	if string(stdout) != "CAPTURED-RAW-VALUE\n" {
		t.Fatalf("stdout must carry ONLY the raw captured value + newline, got %q", stdout)
	}
	if !strings.Contains(string(stderr), "task capture:") {
		t.Fatalf("the human report must be on stderr, got %q", stderr)
	}
}

// TestNestedVerdict_CarriesTheStepReason is the plugin-task#12 regression.
//
// Only the task-level Message crosses a nested `task:` hop: nestedVerdict's caller
// returns verbResult(… msg) and drops r.Steps, and the single CheckResult it builds
// has no field for nested steps. Pre-fix it passed the bare "N step(s) failed", so a
// composite task failed with no path and no reason — the leaf's own message existed at
// the top level and was destroyed exactly at the hop. That made a real umbrella
// `task sync` failure (sync -> pins -> git-submodules over ~424 submodules) transient
// and permanently un-diagnosable.
//
// This test drives nestedVerdict — the function the hop's loop ACTUALLY calls — so it
// fails if that path regresses to the bare r.Message. (A test driving a helper beside
// the call site instead would pass after such a regression: measured, that is exactly
// how the first version of this test was wrong.)
func TestNestedVerdict_CarriesTheStepReason(t *testing.T) {
	r := &runResult{
		Name:    "pins",
		Status:  "ran",
		Failed:  1,
		Message: "1 step(s) failed", // the flattened aggregate the fix replaces
		Steps: []spec.StepResult{
			{
				Keyword: "run",
				Text:    "bump the pins (charly rolls first)",
				Result: spec.CheckResult{
					Status:  spec.StatusFail,
					Message: "exit=2, want 0 (stderr: index.lock exists)",
				},
			},
		},
	}

	st, got := nestedVerdict("pins", r)
	if st != spec.StatusFail {
		t.Fatalf("a failed task must yield a FAIL verdict, got %s", st)
	}
	// The task is still named (existing consumers grep the `task "x" failed` shape).
	if !strings.Contains(got, `task "pins" failed`) {
		t.Fatalf("the nested verdict must name the task, got:\n%s", got)
	}
	// The count is still the headline.
	if !strings.Contains(got, "1 step(s) failed") {
		t.Fatalf("the nested verdict must still lead with the count, got:\n%s", got)
	}
	// The PATH to the failure: the failing step's keyword + authored text.
	if !strings.Contains(got, "[fail] run — bump the pins (charly rolls first)") {
		t.Fatalf("the nested verdict must name the failing step's path, got:\n%s", got)
	}
	// The REASON: the leaf step's own message, which is the whole point of the fix.
	if !strings.Contains(got, "exit=2, want 0 (stderr: index.lock exists)") {
		t.Fatalf("the nested verdict must carry the failing step's own message, got:\n%s", got)
	}
}

// TestNestedVerdict_PassIsSilent proves a healthy task yields StatusPass with no
// message, so the hop keeps walking (the loop only stops on a non-pass).
func TestNestedVerdict_PassIsSilent(t *testing.T) {
	st, msg := nestedVerdict("ok", &runResult{Status: "ran", Failed: 0})
	if st != spec.StatusPass || msg != "" {
		t.Fatalf("a healthy task must be a silent PASS, got %s %q", st, msg)
	}
}

// TestNestedVerdict_MultilineIndent proves a multi-line step message keeps its shape
// (every line indented under the step, like formatStepLine) and that PASSING steps are
// NOT listed — the nested verdict names only what failed.
func TestNestedVerdict_MultilineIndent(t *testing.T) {
	r := &runResult{
		Status: "ran",
		Failed: 1,
		Steps: []spec.StepResult{
			{Keyword: "run", Text: "a step that passed",
				Result: spec.CheckResult{Status: spec.StatusPass, Message: "should not appear"}},
			{Keyword: "check", Text: "the failing step",
				Result: spec.CheckResult{Status: spec.StatusFail, Message: "line one\nline two"}},
		},
	}

	_, got := nestedVerdict("t", r)

	if strings.Contains(got, "should not appear") {
		t.Fatalf("a PASSING step must not be listed in a failure verdict, got:\n%s", got)
	}
	for _, want := range []string{"[fail] check — the failing step", "      line one", "      line two"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in the nested verdict, got:\n%s", want, got)
		}
	}
}

// TestNestedVerdict_ErrorStatusUnchanged proves the sibling arm — a task that hard
// ERRORED — still reports its own task-level Message (there are no step results to
// carry in that case), so the fix did not disturb it.
func TestNestedVerdict_ErrorStatusUnchanged(t *testing.T) {
	st, got := nestedVerdict("t", &runResult{Status: "error", Message: "cannot enter task dir \"x\": no such file"})
	if st != spec.StatusFail {
		t.Fatalf("an errored task must yield FAIL, got %s", st)
	}
	if !strings.Contains(got, `task "t": cannot enter task dir "x": no such file`) {
		t.Fatalf("the error arm must keep its own message, got:\n%s", got)
	}
}

// TestNestedFailure_TopLevelNotEnriched pins the R3 boundary: the TOP level already
// prints every step line (printResultText) and printResultsJSON already emits the
// steps array, so enriching the top-level res.Message would DUPLICATE the reason. The
// aggregate must therefore stay the bare count at the source, and only the hop renders
// the detail. This test fails if someone "fixes" the flattening in runner.go instead
// of at the hop.
func TestNestedFailure_TopLevelNotEnriched(t *testing.T) {
	old := verbResolverFor
	verbResolverFor = func(*sdk.Executor) kit.VerbResolver {
		return &fakeResolver{status: spec.StatusFail, message: "leaf-reason: boom"}
	}
	defer func() { verbResolverFor = old }()

	ts := &taskSet{dir: t.TempDir(), tasks: map[string]spec.Task{
		"t": {Description: "f", Plan: []spec.Step{
			{Run: "the leaf step", Op: spec.Op{Plugin: "command", PluginInput: map[string]any{"command": "false"}}},
		}},
	}}
	res, err := runTask(context.Background(), nil, ts, "t", nil, false, false)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if res.Message != "1 step(s) failed" {
		t.Fatalf("the top-level aggregate must stay the bare count (the reporter prints the steps), got %q", res.Message)
	}
	// The reason still EXISTS on the step, which is what the hop reads.
	if len(res.Steps) != 1 || !strings.Contains(res.Steps[0].Result.Message, "leaf-reason: boom") {
		t.Fatalf("the leaf reason must remain on the step, got %+v", res.Steps)
	}
	// End to end through the SAME verdict the hop uses, the reason now crosses.
	if _, msg := nestedVerdict("t", res); !strings.Contains(msg, "leaf-reason: boom") {
		t.Fatalf("the hop must carry the leaf reason end to end, got:\n%s", msg)
	}
}
