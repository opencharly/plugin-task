package task

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/opencharly/plugin-task/candy/plugin-task/params"
	"github.com/opencharly/sdk"
	pb "github.com/opencharly/spec/proto"
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
		r, rerr := runTask(ctx, ex, ts, n, opts.params, opts.force, opts.dryRun)
		if rerr != nil {
			return rerr
		}
		results = append(results, r)
		if r.Status == "error" && !opts.json {
			printResultText(nil, r)
			return fmt.Errorf("task %q failed: %s", n, r.Message)
		}
	}

	if opts.json {
		return printResultsJSON(results)
	}
	for _, r := range results {
		printResultText(ts, r)
	}
	// A failed step (non-continue_on_error) exits non-zero.
	for _, r := range results {
		if r.Status == "error" || (r.Status == "ran" && r.Failed > 0) {
			return fmt.Errorf("task %q failed", r.Name)
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

func printResultText(ts *taskSet, r *runResult) {
	switch r.Status {
	case "up-to-date":
		fmt.Fprintf(outWriter(), "task %s: %s\n", r.Name, r.Message)
	case "skipped-platform":
		fmt.Fprintf(outWriter(), "task %s: skipped — %s\n", r.Name, r.Message)
	case "error":
		fmt.Fprintf(outWriter(), "task %s: ERROR — %s\n", r.Name, r.Message)
	default:
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
// plugin_input `{"task":..., "param":[...]}`. The two are distinguished by which
// shape the params JSON matches (the command envelope's "args" key).
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
	// verb:task step — decode the typed plugin_input and run the named task.
	var in params.TaskInput
	if len(req.GetParamsJson()) > 0 {
		if jerr := json.Unmarshal(req.GetParamsJson(), &in); jerr != nil {
			return nil, fmt.Errorf("plugin-task: decode verb input: %w", jerr)
		}
	}
	ts, lerr := loadTaskSet(ctx, ex)
	if lerr != nil {
		return nil, lerr
	}
	params_ := map[string]string{}
	for _, kv := range in.Param {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			params_[k] = v
		}
	}
	order, cerr := ts.closure([]string{in.Task})
	if cerr != nil {
		return nil, cerr
	}
	for _, n := range order {
		r, rerr := runTask(ctx, ex, ts, n, params_, false, false)
		if rerr != nil {
			return nil, rerr
		}
		if r.Status == "error" || (r.Status == "ran" && r.Failed > 0) {
			return nil, fmt.Errorf("task %q failed: %s", n, r.Message)
		}
	}
	return &pb.InvokeReply{}, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// outWriter is os.Stdout (indirection kept for tests).
func outWriter() io.Writer { return os.Stdout }

var _ = sort.Strings
