package task

import (
	"fmt"

	"github.com/opencharly/plugin-task/candy/plugin-task/params"
)

// resolve.go — task-argument resolution and the merged step environment.
//
// These two functions (and closure.go's two) lived in `sdk/workflowkit` because the SDK's
// pipeline lowering was their second caller: ONE implementation for the plugin-task CLI
// and the lowering (plugin-task CHANGELOG 2026.277.0711). opencharly/spec#304 unit 1 moved
// that lowering into the plugin that owns the pipeline kind, so `sdk` has no caller left —
// and a helper typed on a KIND's body cannot stay in a kind-blind mechanism library. They
// move home with the body they are typed on. `workflowkit.ResolveTaskDir` and
// `workflowkit.ExpandVars` stay in the SDK: they take strings, know no task, and are
// shared.

// resolveArgs overlays a task's declared `params:` defaults with the CLI-supplied
// overrides, and enforces `required: true`. A required param with neither an
// override nor a default is a hard error. An undeclared CLI arg passes through
// (forward-compat, and vars-like usage).
func resolveArgs(t params.Task, cli map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for name, ps := range t.Params {
		if v, ok := cli[name]; ok {
			out[name] = v
			continue
		}
		if ps.Default != nil {
			out[name] = fmt.Sprint(ps.Default)
			continue
		}
		if ps.Required {
			return nil, fmt.Errorf("required parameter %q not supplied (--param %s=VALUE)", name, name)
		}
	}
	// Pass through any CLI param not declared (forward-compat, and vars-like usage).
	for k, v := range cli {
		if _, declared := t.Params[k]; !declared {
			out[k] = v
		}
	}
	return out, nil
}

// mergedEnv is the variable map every step + the dir resolution sees: the task's
// vars, then its env, then the RESOLVED args. Later sources win on a key collision.
func mergedEnv(t params.Task, args map[string]string) map[string]string {
	env := map[string]string{}
	for k, v := range t.Vars {
		env[k] = v
	}
	for k, v := range t.Env {
		env[k] = v
	}
	for k, v := range args {
		env[k] = v
	}
	return env
}
