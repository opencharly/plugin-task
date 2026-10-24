package task

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/spec"
)

// stepenv_test.go — the DATA channel: the task's RESOLVED values reach the step's
// PROCESS ENVIRONMENT as data, so a value is never parsed by a shell on its way to a
// command (opencharly/plugin-task#34).
//
// The TEXT channel (the expander) substitutes a value into a step's command TEXT and
// the shell then PARSES that text, so a value that is DATA becomes SOURCE: a `$(…)`
// inside a value EXECUTES. The environment is the other channel — a value delivered
// as an environment variable is DATA, because the shell never re-parses the result of
// an expansion.
//
// What these tests prove is the HANDOVER and the merge, on the real plan walk. The
// SHELL half — that a value carrying a metacharacter arrives verbatim and the shell
// did not execute it — is proved LIVE, in scripts/gate-task.sh, because a unit test
// with a fake verb has no shell to fool.

// envCapturingResolver records the env the plan walk handed the step's verb, so a
// test can see exactly what the step's PROCESS ENVIRONMENT would carry.
type envCapturingResolver struct {
	env  map[string]string
	seen bool
}

func (r *envCapturingResolver) RunVerb(_ context.Context, op *spec.Op) (spec.CheckResult, bool) {
	if op.Plugin == "command" {
		r.env = op.Env
		r.seen = true
		return spec.CheckResult{Status: spec.StatusPass}, true
	}
	return spec.CheckResult{}, false
}

func (r *envCapturingResolver) RunProvisionAct(_ context.Context, _ *spec.Op, _ string) (spec.CheckResult, bool) {
	return spec.CheckResult{}, false
}

// captureStepEnv drives the REAL plan walk over a one-step task and returns the env
// the step's verb received.
func captureStepEnv(t *testing.T, task spec.Task, params map[string]string) (map[string]string, *runResult) {
	t.Helper()
	old := verbResolverFor
	cap := &envCapturingResolver{}
	verbResolverFor = func(*sdk.Executor) kit.VerbResolver { return cap }
	defer func() { verbResolverFor = old }()

	ts := &taskSet{dir: t.TempDir(), tasks: map[string]spec.Task{"t": task}}
	res, err := runTask(context.Background(), nil, ts, "t", params, false, false)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if !cap.seen {
		t.Fatalf("the plan walk never dispatched the step's verb — the walk did not run")
	}
	return cap.env, res
}

// TestNewTaskRunner_ExportsTheTasksResolvedValues proves the handover: the runner
// carries the task's merged env — its vars, its env, its RESOLVED params, and the
// task's dir — as the values a step's process environment holds.
//
// A lowercase param is in the map: the expander's grammar is uppercase-only, so a
// lowercase param is unreachable from a step's TEXT. The environment is what makes it
// reachable BY NAME (opencharly/plugin-task#34).
func TestNewTaskRunner_ExportsTheTasksResolvedValues(t *testing.T) {
	dir := t.TempDir()
	task := spec.Task{
		Description: "d",
		Vars:        map[string]string{"UPPER": "from-vars"},
		Env:         map[string]string{"TASKENV": "from-env"},
	}
	r, cap := newTaskRunner(nil, dir, task, map[string]string{"out": "a value with spaces"})
	defer cap.cleanup()

	stepEnv := r.StepEnv()
	for name, want := range map[string]string{
		"UPPER":    "from-vars",
		"TASKENV":  "from-env",
		"out":      "a value with spaces",
		"TASK_DIR": dir,
	} {
		got, ok := stepEnv[name]
		if !ok {
			t.Fatalf("the data channel must export %q (present=%v, exported=%v)", name, ok, stepEnv)
		}
		if got != want {
			t.Fatalf("the data channel exported %q=%q, want %q", name, got, want)
		}
	}
}

// TestRunTask_StepEnvCarriesAParamAsData is the DISCRIMINATING test: a param's value
// reaches the step's verb VERBATIM, metacharacters and all. Before the data channel
// the step's env was empty, so this value reached the step nowhere at all.
func TestRunTask_StepEnvCarriesAParamAsData(t *testing.T) {
	evil := `$(touch /tmp/r1-repro/PWNED) "double" 'single' $HOME; rm -rf /`
	env, _ := captureStepEnv(t, spec.Task{
		Description: "d",
		Plan:        []spec.Step{{Run: "x", Op: spec.Op{Plugin: "command", PluginInput: map[string]any{"command": "true"}}}},
	}, map[string]string{"out": evil})

	got, ok := env["out"]
	if !ok {
		t.Fatalf("the step's process env must carry the param (present=%v, env=%v)", ok, env)
	}
	if got != evil {
		t.Fatalf("the param must reach the step VERBATIM, got %q want %q", got, evil)
	}
}

// TestRunTask_StepsOwnEnvWinsOnACollision proves the collision rule the data channel
// inherits: the step's OWN `env:` wins, because the author spoke last about their own
// step and the host's value is a default. The second param proves the channel is
// ON at all — a test that asserts only the collision would also pass with no channel.
func TestRunTask_StepsOwnEnvWinsOnACollision(t *testing.T) {
	env, _ := captureStepEnv(t, spec.Task{
		Description: "d",
		Plan: []spec.Step{{Run: "x", Op: spec.Op{
			Plugin:      "command",
			Env:         map[string]string{"out": "step-wins"},
			PluginInput: map[string]any{"command": "true"},
		}}},
	}, map[string]string{"out": "host-value", "other": "host-value-too"})

	if got := env["out"]; got != "step-wins" {
		t.Fatalf("the step's own env must win the collision, got %q", got)
	}
	if got := env["other"]; got != "host-value-too" {
		t.Fatalf("a name with no collision must carry the host's value, got %q", got)
	}
}

// TestRunTask_StdinBytesAreNotExported proves the boundary of the data channel: the
// piped bytes are a runtime STREAM, not a resolved value, and they are NOT exported.
//
// The reason is mechanical, not stylistic: the command verb renders an exported value
// into the command TEXT, and a NUL byte (a piped binary stream carries one) makes the
// exec un-runnable — "fork/exec /usr/bin/sh: invalid argument". The stream keeps the
// channel it has always had: ${TASK_STDIN} / ${TASK_STDIN_FILE} in the step's text.
func TestRunTask_StdinBytesAreNotExported(t *testing.T) {
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	go func() {
		_, _ = w.Write([]byte("piped-bytes"))
		_ = w.Close()
	}()
	defer func() { os.Stdin = old }()

	env, _ := captureStepEnv(t, spec.Task{
		Description: "d",
		Plan: []spec.Step{{Run: "x", Op: spec.Op{
			Plugin:      "command",
			PluginInput: map[string]any{"command": `printf %s "${TASK_STDIN}"`},
		}}},
	}, map[string]string{"kept": "a resolved value"})

	// The channel is ON — a resolved value is exported — while the stream is NOT.
	if got := env["kept"]; got != "a resolved value" {
		t.Fatalf("the data channel must export the resolved values, got kept=%q", got)
	}
	if v, ok := env["TASK_STDIN"]; ok {
		t.Fatalf("the piped bytes must NOT be exported, got TASK_STDIN=%q", v)
	}
	if _, ok := env["TASK_STDIN_FILE"]; ok {
		t.Fatal("the stdin temp file must NOT be exported either")
	}
}

// TestRunTask_NonExportableNameIsDisclosed proves a value the SHELL cannot export is
// DISCLOSED, never lost silently: a name outside ^[A-Za-z_][A-Za-z0-9_]*$ cannot be
// an environment variable, and a value that vanishes silently reads exactly like a
// host that never supplied it.
func TestRunTask_NonExportableNameIsDisclosed(t *testing.T) {
	_, res := captureStepEnv(t, spec.Task{
		Description: "d",
		Plan:        []spec.Step{{Run: "x", Op: spec.Op{Plugin: "command", PluginInput: map[string]any{"command": "true"}}}},
	}, map[string]string{"my-param": "value"})

	if len(res.Steps) == 0 {
		t.Fatal("the walk produced no step result")
	}
	msg := res.Steps[0].Result.Message
	if !strings.Contains(msg, "not exported (not a valid shell identifier): my-param") {
		t.Fatalf("a name the shell cannot export must be disclosed on the step's message, got %q", msg)
	}
}
