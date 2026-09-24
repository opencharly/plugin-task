package task

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/opencharly/plugin-task/candy/plugin-task/params"
	"github.com/opencharly/sdk"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
)

// cli.go — the command:task grammar:
//
//	charly task                     list declared tasks
//	charly task list                list declared tasks
//	charly task <name> [opts]       run a task (its depends_on closure first)
//	charly task --all [opts]        run every declared task (dependency order)
//
// Options: --dry-run, --json, --force, --param NAME=VALUE (repeatable).

// runTaskCLI is the single entry point both placements use (CliMain out-of-process,
// Invoke(OpRun) compiled-in), so the command behaves identically either way.
func runTaskCLI(ctx context.Context, ex *sdk.Executor, args []string) error {
	opts, err := parseArgs(args)
	if err != nil {
		return err
	}

	ts, err := loadTaskSet(ctx, ex)
	if err != nil {
		return err
	}

	if opts.list || (opts.name == "" && !opts.all) {
		return printList(ts, opts.json)
	}

	names := []string{opts.name}
	if opts.all {
		names = sortedNames(ts.names())
	}
	order, err := ts.closure(names)
	if err != nil {
		return err
	}

	var results []*runResult
	for _, n := range order {
		// Resolve the task's params (declared defaults <- CLI overrides) BEFORE the
		// run, so required-param errors surface up front.
		resolved, perr := resolveParams(ts.tasks[n], opts.params)
		if perr != nil {
			return fmt.Errorf("task %q: %w", n, perr)
		}
		r, rerr := runTask(ctx, ex, ts, n, resolved, opts.force, opts.dryRun)
		if rerr != nil {
			return rerr
		}
		results = append(results, r)
		// A hard ERROR (aborted precondition / dir failure / no TTY) stops the closure
		// — there is nothing to continue with. In --json mode we still emit the
		// collected results before returning the error (below) so the report is not
		// lost; the non-json path prints as it goes.
		if r.Status == "error" && !opts.json {
			printResultText(r)
			return fmt.Errorf("task %q failed: %s", n, r.Message)
		}
	}

	if opts.json {
		if perr := printResultsJSON(results); perr != nil {
			return perr
		}
	} else {
		for _, r := range results {
			printResultText(r)
		}
	}
	// Exit non-zero when a task aborted or a step failed AND the task does not opt
	// out via continue_on_error — uniformly in text and --json modes (Go-Task's flag
	// lets LATER tasks run, it does not suppress the run's non-zero exit).
	for _, r := range results {
		if r.Status == "error" {
			return fmt.Errorf("task %q failed: %s", r.Name, r.Message)
		}
		if r.Status == "ran" && r.Failed > 0 && !r.ContinueOnError {
			return fmt.Errorf("task %q failed (%s)", r.Name, r.Message)
		}
	}
	return nil
}

type cliOpts struct {
	name   string
	all    bool
	list   bool
	dryRun bool
	json   bool
	force  bool
	params map[string]string
}

func parseArgs(args []string) (cliOpts, error) {
	o := cliOpts{params: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--all" || a == "-a":
			o.all = true
		case a == "--list" || a == "list":
			o.list = true
		case a == "--dry-run" || a == "-n":
			o.dryRun = true
		case a == "--json":
			o.json = true
		case a == "--force" || a == "-f":
			o.force = true
		case a == "--param" || a == "-p":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--param requires NAME=VALUE")
			}
			i++
			k, v, ok := strings.Cut(args[i], "=")
			if !ok || k == "" {
				return o, fmt.Errorf("--param must be NAME=VALUE (got %q)", args[i])
			}
			o.params[k] = v
		case strings.HasPrefix(a, "--param="):
			k, v, ok := strings.Cut(strings.TrimPrefix(a, "--param="), "=")
			if !ok || k == "" {
				return o, fmt.Errorf("--param must be NAME=VALUE")
			}
			o.params[k] = v
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown flag %q", a)
		default:
			if o.name != "" {
				return o, fmt.Errorf("unexpected extra argument %q (only one task name)", a)
			}
			o.name = a
		}
	}
	return o, nil
}

func printList(ts *taskSet, asJSON bool) error {
	names := sortedNames(ts.names())
	if asJSON {
		type item struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Steps       int      `json:"steps"`
			DependsOn   []string `json:"depends_on,omitempty"`
		}
		out := make([]item, 0, len(names))
		for _, n := range names {
			t := ts.tasks[n]
			out = append(out, item{Name: n, Description: t.Description, Steps: len(t.Plan), DependsOn: t.DependsOn})
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(outWriter(), string(b))
		return nil
	}
	if len(names) == 0 {
		fmt.Fprintln(outWriter(), "no tasks declared")
		return nil
	}
	for _, n := range names {
		t := ts.tasks[n]
		fmt.Fprintf(outWriter(), "%-24s %s\n", n, t.Description)
	}
	return nil
}

func printResultText(r *runResult) {
	switch r.Status {
	case "up-to-date":
		fmt.Fprintf(outWriter(), "task %s: %s\n", r.Name, r.Message)
	case "skipped-platform":
		fmt.Fprintf(outWriter(), "task %s: skipped — %s\n", r.Name, r.Message)
	case "error":
		fmt.Fprintf(outWriter(), "task %s: ERROR — %s\n", r.Name, r.Message)
	default:
		// silent: true suppresses the per-step lines, keeping only the summary.
		if r.Silent {
			fmt.Fprintf(outWriter(), "task %s: %d step(s), %d failed\n", r.Name, len(r.Steps), r.Failed)
			return
		}
		fmt.Fprintf(outWriter(), "task %s: %d step(s), %d failed\n", r.Name, len(r.Steps), r.Failed)
		for _, s := range r.Steps {
			fmt.Fprintf(outWriter(), "  [%s] %s — %s\n", s.Result.Status.String(), s.Keyword, firstLine(s.Text))
		}
	}
}

func printResultsJSON(results []*runResult) error {
	type stepOut struct {
		Keyword string `json:"keyword"`
		Text    string `json:"text"`
		Status  string `json:"status"`
		Message string `json:"message,omitempty"`
	}
	type resOut struct {
		Name    string    `json:"name"`
		Status  string    `json:"status"`
		Message string    `json:"message,omitempty"`
		Failed  int       `json:"failed"`
		Steps   []stepOut `json:"steps,omitempty"`
	}
	out := make([]resOut, 0, len(results))
	for _, r := range results {
		ro := resOut{Name: r.Name, Status: r.Status, Message: r.Message, Failed: r.Failed}
		for _, s := range r.Steps {
			ro.Steps = append(ro.Steps, stepOut{Keyword: s.Keyword, Text: s.Text, Status: s.Result.Status.String(), Message: s.Result.Message})
		}
		out = append(out, ro)
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(outWriter(), string(b))
	return nil
}

// invokeOpRun dispatches an OpRun request. A command:task invocation carries the
// charly command envelope `{"args":[...]}`; a verb:task step carries the typed
// plugin_input `{"task":..., "param":[...]}`. The two are distinguished by the
// command envelope's "args" key.
func invokeOpRun(ctx context.Context, req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	ex, err := sdk.ExecutorForInvoke(ctx, req.GetExecutorBrokerId())
	if err != nil {
		return nil, fmt.Errorf("plugin-task: reverse-channel executor: %w", err)
	}
	var probe map[string]json.RawMessage
	if len(req.GetParamsJson()) > 0 {
		_ = json.Unmarshal(req.GetParamsJson(), &probe)
	}
	if _, isCommand := probe["args"]; isCommand {
		var cmdEnv struct {
			Args []string `json:"args"`
		}
		_ = json.Unmarshal(req.GetParamsJson(), &cmdEnv)
		if err := runTaskCLI(ctx, ex, cmdEnv.Args); err != nil {
			return nil, err
		}
		return &pb.InvokeReply{}, nil
	}
	// verb:task step — the params carry the desugared Op, whose plugin_input holds
	// the typed #TaskInput. Extract that nested object (the sibling plugin-pipeline
	// verb does the same) and run the named task, returning a spec.CheckResult the
	// plan harness decodes (an empty reply is not a valid verdict).
	var opEnv struct {
		PluginInput params.TaskInput `json:"plugin_input"`
	}
	if len(req.GetParamsJson()) > 0 {
		if jerr := json.Unmarshal(req.GetParamsJson(), &opEnv); jerr != nil {
			return nil, fmt.Errorf("plugin-task: decode verb input: %w", jerr)
		}
	}
	in := opEnv.PluginInput
	ts, lerr := loadTaskSet(ctx, ex)
	if lerr != nil {
		return nil, lerr
	}
	cli := map[string]string{}
	for _, kv := range in.Param {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			cli[k] = v
		}
	}
	order, cerr := ts.closure([]string{in.Task})
	if cerr != nil {
		return verbResult(spec.StatusFail, cerr.Error())
	}
	ran, failed := 0, 0
	for _, n := range order {
		resolved, perr := resolveParams(ts.tasks[n], cli)
		if perr != nil {
			return verbResult(spec.StatusFail, fmt.Sprintf("task %q: %v", n, perr))
		}
		r, rerr := runTask(ctx, ex, ts, n, resolved, false, false)
		if rerr != nil {
			return verbResult(spec.StatusFail, rerr.Error())
		}
		ran++
		failed += r.Failed
		if r.Status == "error" {
			return verbResult(spec.StatusFail, fmt.Sprintf("task %q: %s", n, r.Message))
		}
		if r.Status == "ran" && r.Failed > 0 && !r.ContinueOnError {
			return verbResult(spec.StatusFail, fmt.Sprintf("task %q failed (%s)", n, r.Message))
		}
	}
	return verbResult(spec.StatusPass, fmt.Sprintf("ran %d task(s), %d step(s) failed", ran, failed))
}

// verbResult marshals a verb verdict as the reply the plan harness decodes. The wire
// shape carries `status` as the lowercase verdict WORD ("pass"/"fail"/"skip"), matching
// pluginCheckResult on the decode side (a numeric enum fails to unmarshal).
func verbResult(status spec.Status, msg string) (*pb.InvokeReply, error) {
	b, err := json.Marshal(struct {
		Status  string `json:"status"`
		Message string `json:"message,omitempty"`
	}{Status: status.String(), Message: msg})
	if err != nil {
		return nil, err
	}
	return &pb.InvokeReply{ResultJson: b}, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// outWriter is os.Stdout (indirection kept for tests).
func outWriter() io.Writer { return os.Stdout }
