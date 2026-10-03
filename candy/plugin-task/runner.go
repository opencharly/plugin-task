package task

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/checkkit"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/sdk/workflowkit"
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
	// ContinueOnError / Silent are carried from the task so the CLI decides the
	// exit status and output shape without re-reading the entity.
	ContinueOnError bool
	Silent          bool
}

// runTask runs ONE task's own plan (dependencies are driven by the closure walk in
// the CLI), honoring the platform gate, preconditions, the incremental staleness
// model, the interactive guard, and the task timeout. It never recurses into
// depends_on. params is the RESOLVED parameter set (declared defaults overlaid with
// CLI overrides — see workflowkit.ResolveArgs).
func runTask(ctx context.Context, ex *sdk.Executor, ts *taskSet, name string, params map[string]string, force, dryRun bool) (*runResult, error) {
	t, ok := ts.tasks[name]
	if !ok {
		return nil, fmt.Errorf("unknown task %q (declared tasks: %s)", name, strings.Join(workflowkit.SortedNames(ts.tasks), ", "))
	}
	res := &runResult{Name: name, ContinueOnError: t.ContinueOnError, Silent: t.Silent}

	if reason := platformSkip(t); reason != "" {
		res.Status = "skipped-platform"
		res.Message = reason
		return res, nil
	}

	// interactive: true — the task requires a terminal (a prompt, an editor, a
	// password read). Refuse to run it without one rather than silently capturing
	// output. Stdin is a character device iff it is a real TTY.
	if t.Interactive && !stdinIsTerminal() {
		res.Status = "error"
		res.Message = "task is interactive: true but stdin is not a terminal"
		return res, nil
	}

	dir := workflowkit.ResolveTaskDir(ts.dir, t.Dir, workflowkit.MergedEnv(t, params))

	// preconditions — a failing precondition ABORTS (never a silent skip).
	for _, pc := range t.Preconditions {
		if code, err := runHostShell(ctx, dir, workflowkit.MergedEnv(t, params), pc); err != nil || code != 0 {
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

	// timeout: a task-level ceiling over the whole plan walk.
	walkCtx := ctx
	if t.Timeout != "" {
		if d, derr := time.ParseDuration(t.Timeout); derr == nil && d > 0 {
			var cancel context.CancelFunc
			walkCtx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
		}
	}

	runner, stdin := newTaskRunner(ex, ts.dir, t, params)
	// The captured-stdin temp file lives only for THIS run — remove it however the walk
	// exits (the error/early-return paths included).
	defer stdin.cleanup()
	set := &spec.LabelDescriptionSet{
		Candy: []spec.LabeledDescription{{Origin: "task:" + name, Description: t.Description, Plan: t.Plan}},
	}
	// dir: parity. The plan-step grammar has no cwd field and the host `command` verb
	// runs through the in-process ShellExecutor (whose cwd is the process cwd), so the
	// task's workdir is applied by chdir around the walk and restored after. The task
	// CLI is a single-threaded host command (RunPlan walks sequentially in one
	// goroutine), so this is safe and is exactly Go-Task's `dir:` semantics.
	if dir != ts.dir {
		if old, gerr := os.Getwd(); gerr == nil {
			if cerr := os.Chdir(dir); cerr != nil {
				res.Status = "error"
				res.Message = fmt.Sprintf("cannot enter task dir %q: %v", dir, cerr)
				return res, nil
			}
			defer func() { _ = os.Chdir(old) }()
		}
	}
	steps := kit.RunPlan(walkCtx, runner, set, false)
	res.Steps = steps
	if stdin.truncated {
		// A runaway pipe was clipped at maxStdinBytes. Record it on the FIRST step's
		// Message — the text reporter prints step messages, never res.Message on a
		// successful run — so the truncation can never pass silently. Steps are the
		// walk's own slice, so the shared backing array carries the note into the report.
		note := fmt.Sprintf("TASK_STDIN truncated at %d bytes", maxStdinBytes)
		if len(steps) > 0 && steps[0].Result.Message != "" {
			steps[0].Result.Message = note + "\n" + steps[0].Result.Message
		} else if len(steps) > 0 {
			steps[0].Result.Message = note
		} else {
			res.Message = note
		}
	}
	for _, s := range steps {
		if s.Result.Status == spec.StatusFail {
			res.Failed++
		}
	}
	res.Status = "ran"
	if res.Failed > 0 {
		res.Message = fmt.Sprintf("%d step(s) failed", res.Failed)
	}
	return res, nil
}

// verbResolverFor builds the plan walk's verb resolver. It is a package VARIABLE so a
// test can substitute a fake and drive the REAL plan walk (the chdir/`dir:`
// application, step order, verdict tallying) without a host reverse channel — the
// verb dispatch itself is SDK code already covered by sdk/kit's own suite.
var verbResolverFor = func(ex *sdk.Executor) kit.VerbResolver {
	return &checkkit.VerbResolver{Ex: ex, Env: spec.CheckEnv{Mode: "live", VenueKind: "host"}}
}

// newTaskRunner builds the kit.Runner the plan walk drives: a host ShellExecutor
// venue, the verb resolver (over the reverse-channel executor, so each step's verb
// dispatches through the host provider registry), and an env carrying the task's
// vars + resolved params + env for ${VAR} expansion, plus the captured-stdin
// exposure when stdin is a pipe.
//
// It also returns the stdinCapture whose temp file the caller MUST remove once the
// walk is done (runTask defers stdin.cleanup()).
func newTaskRunner(ex *sdk.Executor, projDir string, t spec.Task, params map[string]string) (*kit.Runner, stdinCapture) {
	env := workflowkit.MergedEnv(t, params)
	env["TASK_DIR"] = workflowkit.ResolveTaskDir(projDir, t.Dir, env)
	stdin := captureStdin(env)

	// The plan walk requires a Verbs resolver; command:task is compiled-in so the
	// reverse-channel executor is always present. A nil ex (out-of-process CliMain)
	// is rejected at the CLI entry, so this is unreachable in practice. Exec MUST be
	// a spec.DeployExecutor whose venue descriptor round-trips (kit.ShellExecutor is
	// the "shell" arm of DescriptorFromExecutor) — a custom wrapper would serialize
	// to no venue and the host would have nil exec (RCA of the first RDD run).
	verbs := verbResolverFor(ex)
	r := kit.NewRunner(kit.RunnerConfig{
		Exec:    kit.ShellExecutor{},
		Mode:    kit.ModeLive,
		Env:     env,
		Verbs:   verbs,
		Grammar: taskGrammar{base: checkkit.PlanGrammar{}},
	})
	if sr, ok := verbs.(interface{ SetRunner(*kit.Runner) }); ok {
		sr.SetRunner(r)
	}
	return r, stdin
}

// maxStdinBytes bounds the piped-stdin read (1 MiB) so a runaway pipe cannot exhaust
// memory; a longer stream is clipped and the clip is reported on a step Message.
const maxStdinBytes = 1 << 20

// stdinCapture is the task's piped stdin exposed to its steps: TASK_STDIN carries the
// bytes and TASK_STDIN_FILE names a temp file holding them, so a step — and a lobster
// `run:` step above it — can read `$TASK_STDIN` / `$TASK_STDIN_FILE`. file is "" when
// stdin is a terminal, in which case NEITHER var is defined: an interactive `charly
// task` must never block on (or consume) the terminal.
type stdinCapture struct {
	file      string
	truncated bool
}

// cleanup removes the capture's temp file. runTask defers it so the file is dropped on
// every exit path (error/early-return included); it is a no-op when none was created.
func (c stdinCapture) cleanup() {
	if c.file != "" {
		_ = os.Remove(c.file)
	}
}

// captureStdin reads stdin when it is NOT a terminal and adds TASK_STDIN /
// TASK_STDIN_FILE to env. Both vars are ALWAYS defined in that mode — possibly empty
// (empty stdin yields TASK_STDIN="" and a TASK_STDIN_FILE naming an empty file) — so a
// workflow step can branch on their PRESENCE. When stdin IS a terminal it is left
// unread and the vars stay absent.
func captureStdin(env map[string]string) stdinCapture {
	if stdinIsTerminal() {
		return stdinCapture{}
	}
	data, rerr := io.ReadAll(io.LimitReader(os.Stdin, maxStdinBytes+1))
	if rerr != nil {
		// An unreadable stdin is not fatal: still define the pair (empty) so the
		// PRESENCE contract holds.
		env["TASK_STDIN"] = ""
		env["TASK_STDIN_FILE"] = ""
		return stdinCapture{}
	}
	truncated := len(data) > maxStdinBytes
	if truncated {
		data = data[:maxStdinBytes]
	}
	env["TASK_STDIN"] = string(data)
	cap := stdinCapture{truncated: truncated}
	f, ferr := os.CreateTemp("", "charly-task-stdin-*")
	if ferr != nil {
		// No temp file available: TASK_STDIN still carries the bytes and the FILE var is
		// present-but-empty, so a step can still test its PRESENCE.
		env["TASK_STDIN_FILE"] = ""
		return cap
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(f.Name())
		env["TASK_STDIN_FILE"] = ""
		return cap
	}
	cap.file = f.Name()
	env["TASK_STDIN_FILE"] = cap.file
	return cap
}

// stdinIsTerminal reports whether stdin is a real terminal (via the terminal
// ioctl, not a character-device heuristic — /dev/null is a char device but NOT a
// terminal, which a Stat-based check wrongly accepts).
func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// runHostShell runs a shell snippet on the HOST in dir, with the merged env exported,
// returning its exit code. Used for preconditions/status — a task is a host-native
// construct, so os/exec is the venue here (the SAME mechanism ShellExecutor/RunCapture
// uses).
func runHostShell(ctx context.Context, dir string, env map[string]string, script string) (int, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range env {
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
