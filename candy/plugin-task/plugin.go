// Package task is the generic declarative task runner for charly. It provides:
//
//   - kind:task     — a named, host-native, REUSABLE plan authored as a
//     `task:` node in charly.yml (the base-schema #Task, host-value-gated).
//   - command:task  — `charly task [list] [<name>] [--dry-run] [--json]
//     [--param k=v] [--all]`, the executor.
//   - verb:task     — composes a declared task into a candy/box/task plan.
//
// It is the declarative replacement for a Taskfile: any task a repository
// needs (build/test/lint/release/verify/notify) is a task entity, and its steps
// use the SAME #Step/#Op grammar a candy's plan uses. The engine reuses
// kit.RunPlan + a pluginVerbResolver over kit.ShellExecutor{} (host-native) —
// exactly the pattern candy/plugin-check uses — so there is no second execution
// engine.
package task

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencharly/sdk"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
)

//go:embed schema/*.cue
var schemaFS embed.FS

const calver = "2026.267.1200"

// entityCache holds the kind:task entity bodies the loader decoded (OpLoad),
// keyed by entity name — the command reads them here (same process, no reparse).
var entityCache = map[string]spec.Task{}

// NewProvider returns the task provider (all three capabilities).
func NewProvider() pb.ProviderServer { return &provider{} }

// NewMeta advertises the three capabilities + the self-contained CUE schema. The
// kind capability is Validates:true so the host dispatches OpValidate (the
// CONCRETE gate closing the required-field gap the closedness-only host value
// gate leaves). No InputDef on the kind capability: the entity body is the base
// #Task/#TaskValue, validated host-side (a self-contained plugin schema cannot
// carry `plan: [...#Step]`).
func NewMeta() pb.PluginMetaServer {
	return sdk.NewMeta(calver, []sdk.ProvidedCapability{
		{Class: "kind", Word: "task", Validates: true},
		{Class: "verb", Word: "task", InputDef: "#TaskInput", Primary: "task"},
		{Class: "command", Word: "task"},
	}, schemaFS)
}

type provider struct{ pb.UnimplementedProviderServer }

// Invoke dispatches by op: OpLoad (kind decode), OpValidate (the deep concrete
// check), and OpRun (the command:task CLI or the verb:task probe).
func (p *provider) Invoke(ctx context.Context, req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	switch req.GetOp() {
	case sdk.OpLoad:
		return invokeLoad(req)
	case sdk.OpValidate:
		return invokeValidate(req)
	case sdk.OpRun:
		switch req.GetReserved() {
		case "task":
			// Both the command:task CLI (compiled-in) and a verb:task step dispatch
			// through OpRun with the same word. The command path carries command args
			// (`{"args":[...]}`); the verb path carries a plugin_input map
			// (`{"task":...,"param":[...]}`), distinguished by the input shape.
			return invokeOpRun(ctx, req)
		default:
			return nil, fmt.Errorf("plugin-task: unsupported word %q", req.GetReserved())
		}
	default:
		return nil, fmt.Errorf("plugin-task: unsupported op %q", req.GetOp())
	}
}

// invokeLoad decodes + caches the authored kind:task body. The host has already
// validated the body against #TaskValue (closedness); concreteness is enforced
// by invokeValidate.
func invokeLoad(req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	var body spec.Task
	if len(req.GetParamsJson()) > 0 {
		if err := json.Unmarshal(req.GetParamsJson(), &body); err != nil {
			return nil, fmt.Errorf("task entity decode: %w", err)
		}
	}
	name := req.GetReserved()
	if name != "" {
		entityCache[name] = body
	}
	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("task entity marshal: %w", err)
	}
	return &pb.InvokeReply{ResultJson: out}, nil
}

// invokeValidate is the deep OpValidate check: the CONCRETE gate the
// closedness-only host value gate cannot express (missing required fields) plus
// task-semantic checks (a plan is present; depends_on names resolve within the
// loaded set is intentionally NOT checked here — the entity may reference a task
// loaded later — the runner surfaces an unknown dependency at run time).
func invokeValidate(req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	var body spec.Task
	if len(req.GetParamsJson()) > 0 {
		if err := json.Unmarshal(req.GetParamsJson(), &body); err != nil {
			return nil, fmt.Errorf("task validate decode: %w", err)
		}
	}
	var diags spec.Diagnostics
	if body.Description == "" {
		diags.Items = append(diags.Items, spec.Diagnostic{
			Severity: "error", Path: "description",
			Message: "task description is required (the ADE identity contract)",
		})
	}
	if len(body.Plan) == 0 {
		diags.Items = append(diags.Items, spec.Diagnostic{
			Severity: "error", Path: "plan",
			Message: "task has no plan (nothing to run)",
		})
	}
	out, err := json.Marshal(diags)
	if err != nil {
		return nil, fmt.Errorf("task validate marshal: %w", err)
	}
	return &pb.InvokeReply{ResultJson: out}, nil
}

// CliMain is the out-of-process CLI entrypoint (only reached when task is NOT
// compiled in). The command NEEDS the host's loaded project (the task entities)
// over the reverse channel; out-of-process CliMain has none, so it errors
// clearly. The canonical placement is compiled-in (Invoke → reverse channel).
func CliMain(args []string) int {
	if err := runTaskCLI(context.Background(), nil, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
