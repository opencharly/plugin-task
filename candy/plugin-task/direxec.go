package task

import (
	"context"

	"github.com/opencharly/spec/shellquote"
	"github.com/opencharly/spec/spec"
)

// direxec.go — the directory-honoring executor: a thin spec.CheckExecutor wrapper that
// runs every script inside the task's `dir:`. The plan-step grammar has no cwd field,
// so a task's workdir is applied at the EXECUTOR boundary (one generic primitive that
// covers every step uniformly) rather than by rewriting authored step text — the
// behavior is "the task runs in its dir", exactly Go-Task's `dir:` semantics.
type dirExecutor struct {
	inner spec.CheckExecutor
	dir   string
}

func (d dirExecutor) RunCapture(ctx context.Context, script string) (string, string, int, error) {
	return d.inner.RunCapture(ctx, "cd "+shellquote.ShellQuote(d.dir)+" && { "+script+"; }")
}

// Kind mirrors the inner venue (a task's steps run on the host).
func (d dirExecutor) Kind() string { return d.inner.Kind() }
