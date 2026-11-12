package task

// task_schema_corpus_test.go — the closedness corpus for the `kind: task` AUTHORED body,
// which is THIS repo's own CUE (`schema/task.cue`'s `#Task` / `#TaskValue`).
//
// MOVED HERE FROM opencharly/spec AS PART OF spec#304 unit 2. While the body lived in the
// contract module, `spec/schema/task_test.go` held these cases. Unit 2 moves the defs into
// this plugin's schema and its later leg deletes both defs from `spec`, so the corpus moves
// with the defs it judges — a def that no longer exists cannot be covered from the repo
// that no longer declares it, and deleting the corpus instead would be a silent coverage
// loss (the 29+5-case lesson unit 1 measured).
//
// WHY IT IS A CLOSEDNESS CORPUS AND NOT A DECODE ROUND-TRIP (R2): the closure lives ONLY in
// CUE. The generated `params.Task` renders every one of these fields as a plain Go
// `string`/`[]spec.Step`, so a Go-level decode cannot tell a closed body from an open one —
// a test that only decodes VALID documents passes identically with the closedness reverted
// and therefore proves nothing. Each reject case below unifies a CONCRETE value into the def
// and demands the same judgement the host's value gate makes on an authored charly.yml, so
// it FAILS the moment a field is reopened.
//
// The schema is composed the way THIS plugin serves it — `schemacompose.ServedText` over the
// body `NewMeta().Describe` puts on the wire (schema_served_test.go's `servedSchema`) — so
// the corpus exercises the shipped unit, never a hand-written excerpt of it.

import (
	"context"
	"testing"

	"cuelang.org/go/cue"

	pb "github.com/opencharly/spec/proto"
)

// taskValueValidate unifies a concrete authored task body into the served `#TaskValue`
// def and returns the resulting error (nil = accepted), at the same concreteness the
// host's gate uses for this kind.
func taskValueValidate(t *testing.T, value string, opts ...cue.Option) error {
	t.Helper()
	schema := servedSchema(t)
	def := schema.LookupPath(cue.ParsePath("#TaskValue"))
	if err := def.Err(); err != nil {
		t.Fatalf("#TaskValue does not resolve in the served schema: %v", err)
	}
	in := def.Context().CompileString(value)
	if err := in.Err(); err != nil {
		t.Fatalf("compile value: %v", err)
	}
	return def.Unify(in).Validate(opts...)
}

// TestTaskValue_ValidTask passes a realistic task (every field set, incl. a plan with a
// builtin verb, a plugin-verb step, and an include) through the host-side value gate def
// #TaskValue.
func TestTaskValue_ValidTask(t *testing.T) {
	body := `{
	description: "build the binary"
	dir: "/workspace"
	env: {CGO_ENABLED: "0"}
	vars: {VERSION: "1.2.3"}
	depends_on: ["tidy"]
	sources: ["**/*.go"]
	generates: ["bin/charly"]
	status: ["test -f bin/charly"]
	timeout: "30m"
	params: {N: {description: "n", default: 1, required: false}}
	plan: [
		{check: "go is present", command: "go version", context: ["deploy"]},
		{run: "run the build", command: "./build.sh"},
		{run: "plugin verb step", plugin: "command", plugin_input: {command: "true"}},
		{include: "tidy"},
	]
}`
	if err := taskValueValidate(t, body, cue.Concrete(true)); err != nil {
		t.Fatalf("valid task must satisfy #TaskValue: %v", err)
	}
}

// TestTaskValue_RejectsUnknownField proves the gate is CLOSED: a typo'd task-level field
// is rejected.
func TestTaskValue_RejectsUnknownField(t *testing.T) {
	if err := taskValueValidate(t, `{description: "x", bogus_field: 1}`, cue.Concrete(true)); err == nil {
		t.Fatal("a typo'd task field must be rejected by #TaskValue")
	}
}

// TestTaskValue_RejectsUnknownStepField proves a task's plan steps are typed against the
// CLOSED #Step/#Op grammar this schema IMPORTS — an unknown Op field is rejected.
func TestTaskValue_RejectsUnknownStepField(t *testing.T) {
	body := `{
	description: "x",
	plan: [{run: "bad step", command: "true", not_an_op_field: 1}],
}`
	if err := taskValueValidate(t, body); err == nil {
		t.Fatal("an unknown step field must be rejected by #Step/#Op")
	}
}

// TestTaskValue_RequiresDescription proves the ADE identity field is required under the
// CONCRETE gate.
func TestTaskValue_RequiresDescription(t *testing.T) {
	if err := taskValueValidate(t, `{dir: "/x"}`, cue.Concrete(true)); err == nil {
		t.Fatal("a task missing description must fail the concrete gate")
	}
}

// TestTaskValueIsTheDefTheKindCapabilityDeclares pins the OTHER half of the move: the def
// name the schema declares is the def name the kind capability advertises as its InputDef.
// A rename on either side breaks the host's resolution silently (the kind falls to the flat
// plugin-input path), so the two are asserted together.
func TestTaskValueIsTheDefTheKindCapabilityDeclares(t *testing.T) {
	caps, err := NewMeta().Describe(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	var declared string
	for _, c := range caps.GetProvided() {
		if c.GetClass() == "kind" && c.GetWord() == "task" {
			declared = c.GetInputDef()
		}
	}
	if declared != "#TaskValue" {
		t.Fatalf("the kind capability declares InputDef %q, want #TaskValue — the host resolves the gate def from this declaration", declared)
	}
	if err := servedSchema(t).LookupPath(cue.ParsePath(declared)).Err(); err != nil {
		t.Fatalf("the declared InputDef %q does not resolve in the served schema: %v", declared, err)
	}
}
