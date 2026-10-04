package task

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/term"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/spec"
)

// TestCaptureStdin_PipeExposesVars proves a piped stdin defines BOTH TASK_STDIN (the
// bytes) and TASK_STDIN_FILE (a temp file holding them) — the presence contract a
// workflow step branches on — and that cleanup removes the temp file.
func TestCaptureStdin_PipeExposesVars(t *testing.T) {
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	if _, err := w.WriteString("piped-input"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	env := map[string]string{}
	cap := captureStdin(env)
	os.Stdin = old

	if v, ok := env["TASK_STDIN"]; !ok || v != "piped-input" {
		t.Fatalf("TASK_STDIN must be defined with the piped bytes, got %q (present=%v)", v, ok)
	}
	fp, ok := env["TASK_STDIN_FILE"]
	if !ok || fp == "" {
		t.Fatalf("TASK_STDIN_FILE must be defined and name a temp file, got %q (present=%v)", fp, ok)
	}
	if b, rerr := os.ReadFile(fp); rerr != nil || string(b) != "piped-input" {
		t.Fatalf("TASK_STDIN_FILE must hold the piped bytes, got %q (%v)", b, rerr)
	}
	cap.cleanup()
	if _, serr := os.Stat(fp); !os.IsNotExist(serr) {
		t.Fatalf("cleanup must remove the temp file %q", fp)
	}
}

// TestCaptureStdin_EmptyPipeStillDefinesVars proves the presence contract holds for an
// EMPTY pipe: both vars are defined, TASK_STDIN is empty, and TASK_STDIN_FILE names an
// empty file (a step can branch on presence without a value).
func TestCaptureStdin_EmptyPipeStillDefinesVars(t *testing.T) {
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	_ = w.Close()
	env := map[string]string{}
	cap := captureStdin(env)
	os.Stdin = old
	defer cap.cleanup()

	if v, ok := env["TASK_STDIN"]; !ok || v != "" {
		t.Fatalf(`empty stdin must define TASK_STDIN="" , got %q (present=%v)`, v, ok)
	}
	fp, ok := env["TASK_STDIN_FILE"]
	if !ok || fp == "" {
		t.Fatalf("empty stdin must still define TASK_STDIN_FILE, got %q (present=%v)", fp, ok)
	}
	if fi, serr := os.Stat(fp); serr != nil || fi.Size() != 0 {
		t.Fatalf("TASK_STDIN_FILE must name an empty file, got %v (%v)", fi, serr)
	}
}

// TestCaptureStdin_TruncatesAtBound proves the 1 MiB bound: a longer stream is clipped
// to exactly maxStdinBytes and flagged, so runTask can report it (never a silent clip).
func TestCaptureStdin_TruncatesAtBound(t *testing.T) {
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	go func() {
		_, _ = w.Write(make([]byte, maxStdinBytes+4096))
		_ = w.Close()
	}()
	env := map[string]string{}
	cap := captureStdin(env)
	os.Stdin = old
	defer cap.cleanup()

	if !cap.truncated {
		t.Fatal("a stream past maxStdinBytes must be reported as truncated")
	}
	if got := len(env["TASK_STDIN"]); got != maxStdinBytes {
		t.Fatalf("TASK_STDIN = %d bytes, want the %d-byte bound", got, maxStdinBytes)
	}
}

// TestRunTask_RecordsStdinTruncation proves the clip reaches a STEP Message — the text
// reporter prints step messages, never res.Message on a successful run — so a truncated
// stdin can never pass silently.
func TestRunTask_RecordsStdinTruncation(t *testing.T) {
	oldResolver := verbResolverFor
	verbResolverFor = func(*sdk.Executor) kit.VerbResolver { return &fakeResolver{status: spec.StatusPass} }
	defer func() { verbResolverFor = oldResolver }()

	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	go func() {
		_, _ = w.Write(make([]byte, maxStdinBytes+1))
		_ = w.Close()
	}()

	// The plan must reference TASK_STDIN: stdin is read only for a task that can
	// observe it (taskConsumesStdin), and this test is about a task that does.
	ts := &taskSet{dir: t.TempDir(), tasks: map[string]spec.Task{
		"t": {Description: "d", Plan: []spec.Step{{Run: "x", Op: spec.Op{Plugin: "command", PluginInput: map[string]any{"command": "printf %s \"$TASK_STDIN\" | wc -c"}}}}},
	}}
	res, err := runTask(context.Background(), nil, ts, "t", nil, false, false)
	os.Stdin = old
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if len(res.Steps) == 0 || !strings.Contains(res.Steps[0].Result.Message, "TASK_STDIN truncated") {
		t.Fatalf("stdin truncation must be recorded on a step Message, got %+v", res.Steps)
	}
}

// TestTaskConsumesStdin_GatesTheRead proves the read happens exactly when a step can
// observe it. A plan naming neither var reads NOTHING — proved against an OPEN pipe
// whose writer is never closed: an unconditional read would block here forever, so the
// test completing at all IS the assertion (it is bounded by the go test timeout).
func TestTaskConsumesStdin_GatesTheRead(t *testing.T) {
	step := func(cmd string) spec.Task {
		return spec.Task{Description: "d", Plan: []spec.Step{{Run: "x", Op: spec.Op{Plugin: "command", PluginInput: map[string]any{"command": cmd}}}}}
	}
	for _, tc := range []struct {
		name string
		task spec.Task
		want bool
	}{
		{"TASK_STDIN", step("cat \"$TASK_STDIN_FILE\""), true},
		{"TASK_STDIN_FILE", step("test -f \"$TASK_STDIN_FILE\""), true},
		{"neither", step("echo hello"), false},
		{"empty plan", spec.Task{Description: "d"}, false},
	} {
		if got := taskConsumesStdin(tc.task); got != tc.want {
			t.Errorf("taskConsumesStdin(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}

	// The gate must prevent the READ, not merely the vars' values: with a never-closed
	// pipe as stdin, an ungated call hangs. This is the regression the gate exists for.
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close(); _ = r.Close() }()
	os.Stdin = r
	defer func() { os.Stdin = old }()

	noStdin := step("echo hello")
	done := make(chan stdinCapture, 1)
	go func() {
		_, cap := newTaskRunner(nil, t.TempDir(), noStdin, nil)
		done <- cap
	}()
	select {
	case cap := <-done:
		cap.cleanup()
	case <-time.After(10 * time.Second):
		t.Fatal("a plan naming neither TASK_STDIN nor TASK_STDIN_FILE must NOT read stdin — the run blocked on an open pipe")
	}
}

// TestCaptureStdin_TTYLeavesVarsAbsent proves a terminal stdin is left UNREAD and BOTH
// vars stay ABSENT, so an interactive `charly task` never blocks on (or consumes) it.
// No TTY is faked: when this environment exposes no pseudo-terminal the arm SKIPS
// visibly rather than asserting on a fake.
func TestCaptureStdin_TTYLeavesVarsAbsent(t *testing.T) {
	tty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil || !term.IsTerminal(int(tty.Fd())) {
		if tty != nil {
			_ = tty.Close()
		}
		t.Skip("SKIP: no usable pseudo-terminal in this environment (TTY-absent arm not exercised)")
	}
	defer func() { _ = tty.Close() }()
	old := os.Stdin
	os.Stdin = tty
	env := map[string]string{}
	cap := captureStdin(env)
	os.Stdin = old
	cap.cleanup()
	if _, ok := env["TASK_STDIN"]; ok {
		t.Fatal("TASK_STDIN must NOT be defined when stdin is a terminal")
	}
	if _, ok := env["TASK_STDIN_FILE"]; ok {
		t.Fatal("TASK_STDIN_FILE must NOT be defined when stdin is a terminal")
	}
}
