package task

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/checkkit"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/spec/spec"
)

// runner.go — the host-native plan executor. It reuses kit.RunPlan + a
// checkkit.VerbResolver over kit.ShellExecutor{} — the SAME engine
// candy/plugin-check drives — so there is no second execution engine. A task's
// `plan:` steps run in authored order on the host; each step's verb dispatches
// through the host's provider registry over the reverse channel (InvokeProvider).

// taskSet is the name→task map a run resolves against, plus the project dir.
type taskSet struct {
	tasks map[string]spec.Task
	dir   string
}

// loadTaskSet loads the project's `kind:task` entities through the reverse channel
// (the plugin-clean pattern) and returns them keyed by name, with the project dir.
// A nil executor (out-of-process, no reverse channel) falls back to the OpLoad
// cache; an absent/empty project yields an empty set.
func loadTaskSet(ctx context.Context, ex *sdk.Executor) (*taskSet, error) {
	ts := &taskSet{tasks: map[string]spec.Task{}}
	dir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve cwd: %w", err)
	}
	ts.dir = dir
	if ex == nil {
		for name, t := range entityCache {
			ts.tasks[name] = t
		}
		return ts, nil
	}
	uf, ok, lerr := loaderkit.LoadUnifiedViaExecutor(ctx, ex, dir)
	if lerr != nil {
		return nil, fmt.Errorf("load project: %w", lerr)
	}
	if ok && uf != nil {
		for name, raw := range uf.PluginKinds["task"] {
			var t spec.Task
			if jerr := json.Unmarshal(raw, &t); jerr != nil {
				return nil, fmt.Errorf("task %q: decode: %w", name, jerr)
			}
			ts.tasks[name] = t
		}
	}
	return ts, nil
}

// runResult is the outcome of one task invocation.
type runResult struct {
	Name    string
	Steps   []spec.StepResult
	Failed  int
	Status  string // "ran" | "up-to-date" | "skipped-platform" | "error"
	Message string
}

// runTask runs ONE task's own plan (dependencies are driven by runClosure), honoring
// the platform gate, preconditions, and the incremental staleness model. It never
// recurses into depends_on.
func runTask(ctx context.Context, ex *sdk.Executor, ts *taskSet, name string, params map[string]string, force, dryRun bool) (*runResult, error) {
	t, ok := ts.tasks[name]
	if !ok {
		return nil, fmt.Errorf("unknown task %q (declared tasks: %s)", name, strings.Join(ts.names(), ", "))
	}
	res := &runResult{Name: name}

	if reason := platformSkip(t); reason != "" {
		res.Status = "skipped-platform"
		res.Message = reason
		return res, nil
	}

	dir := resolveTaskDir(ts.dir, t.Dir, params)

	// preconditions — a failing precondition ABORTS (never a silent skip).
	for _, pc := range t.Preconditions {
		if code, err := runHostShell(ctx, dir, t.Env, params, pc); err != nil || code != 0 {
			res.Status = "error"
			res.Message = fmt.Sprintf("precondition failed: %s (exit %d)", pc, code)
			return res, nil
		}
	}

	// incremental staleness — status probes all exit 0, or every generated path
	// newer than every source ⇒ up to date (skipped, unless --force).
	if !force {
		if up, why := taskUpToDate(ctx, dir, t, params); up {
			res.Status = "up-to-date"
			res.Message = why
			return res, nil
		}
	}

	if dryRun {
		res.Status = "ran"
		res.Message = fmt.Sprintf("dry-run: %d step(s) would run", len(t.Plan))
		return res, nil
	}

	runner := newTaskRunner(ex, ts.dir, t, params)
	set := &spec.LabelDescriptionSet{
		Candy: []spec.LabeledDescription{{Origin: "task:" + name, Description: t.Description, Plan: t.Plan}},
	}
	steps := kit.RunPlan(ctx, runner, set, false)
	res.Steps = steps
	for _, s := range steps {
		if s.Result.Status == spec.StatusFail {
			res.Failed++
		}
	}
	res.Status = "ran"
	if res.Failed > 0 && !t.ContinueOnError {
		res.Message = fmt.Sprintf("%d step(s) failed", res.Failed)
	}
	return res, nil
}

// newTaskRunner builds the kit.Runner the plan walk drives: a host ShellExecutor
// venue, a checkkit.VerbResolver over the reverse-channel executor (so each step's
// verb dispatches through the host provider registry), and an env carrying the
// task's vars + params + env for ${VAR} expansion.
func newTaskRunner(ex *sdk.Executor, projDir string, t spec.Task, params map[string]string) *kit.Runner {
	env := map[string]string{}
	for k, v := range t.Vars {
		env[k] = v
	}
	for k, v := range t.Env {
		env[k] = v
	}
	for k, v := range params {
		env[k] = v
	}
	env["TASK_DIR"] = resolveTaskDir(projDir, t.Dir, params)

	// The plan walk requires a Verbs resolver; command:task is compiled-in so the
	// reverse-channel executor is always present. A nil ex (out-of-process CliMain)
	// is rejected at the CLI entry, so this is unreachable in practice.
	verbs := &checkkit.VerbResolver{Ex: ex, Env: spec.CheckEnv{Mode: "live", VenueKind: "host"}}
	var exec kit.Executor = kit.ShellExecutor{}
	if dir := resolveTaskDir(projDir, t.Dir, params); dir != "" {
		exec = dirExecutor{inner: kit.ShellExecutor{}, dir: dir}
	}
	r := kit.NewRunner(kit.RunnerConfig{
		Exec:    exec,
		Mode:    kit.ModeLive,
		Env:     env,
		Verbs:   verbs,
		Grammar: checkkit.PlanGrammar{},
	})
	verbs.SetRunner(r)
	return r
}

func (ts *taskSet) names() []string {
	out := make([]string, 0, len(ts.tasks))
	for n := range ts.tasks {
		out = append(out, n)
	}
	return out
}

// resolveTaskDir resolves a task's dir against the project root, expanding ${VAR}
// references from the params first (so dir: "${WORKDIR}/src" works).
func resolveTaskDir(projDir, dir string, params map[string]string) string {
	if dir == "" {
		return projDir
	}
	dir = expandParams(dir, params)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(projDir, dir)
	}
	return dir
}

func expandParams(s string, params map[string]string) string {
	for k, v := range params {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
		s = strings.ReplaceAll(s, "$"+k, v)
	}
	return s
}

// runHostShell runs a shell snippet on the HOST in dir, with the task's env + params
// exported, returning its exit code. Used for preconditions/status — a task is a
// host-native construct, so os/exec is the venue here (the SAME mechanism
// ShellExecutor/RunCapture uses).
func runHostShell(ctx context.Context, dir string, env map[string]string, params map[string]string, script string) (int, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	for k, v := range params {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	return 1, err
}
