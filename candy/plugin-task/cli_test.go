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
