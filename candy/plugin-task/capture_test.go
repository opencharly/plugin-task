package task

import (
	"io"
	"os"
	"testing"
)

// captureStreams runs fn with os.Stdout and os.Stderr pointed at pipes and returns
// what each stream received.
//
// ONE shape for the whole package: a test that asserts on what the reporter wrote
// must not re-implement the capture, because a capture written twice is a capture
// written once correctly and once wrongly. This one is written correctly:
//
//   - BOTH ends of BOTH pipes are kept. os.Pipe returns (read, write); a pipe whose
//     write end is dropped can never be drained, so the read end would block a later
//     writer forever and the descriptor would leak.
//   - The write ends are closed BEFORE the drain. io.ReadAll returns EOF only when
//     every write end is closed, and a pipe left undrained blocks the next writer.
//   - The globals go back on every exit path, including a panic in fn — which the
//     testing package recovers and then runs the NEXT test with. Without the
//     restore, that test would write its output into a pipe nobody reads.
//   - The restore happens before the caller asserts, so a failure message is never
//     written to a closed pipe (and lost).
func captureStreams(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = outR.Close()
		_ = outW.Close()
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	defer func() {
		os.Stdout, os.Stderr = oldOut, oldErr
	}()
	fn()
	_ = outW.Close()
	_ = errW.Close()
	out, _ := io.ReadAll(outR)
	_ = outR.Close()
	errOut, _ := io.ReadAll(errR)
	_ = errR.Close()
	return string(out), string(errOut)
}
