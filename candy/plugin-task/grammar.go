package task

import "github.com/opencharly/spec/spec"

// grammar.go — the task plan grammar. A task is a host-native construct: its steps
// run UNCONDITIONALLY, in authored order, in the task's venue. The check engine's
// build-vs-runtime context gating (a `context: [build]` install step vs a
// `context: [runtime]` probe) has no meaning for a task — there is no image build
// timeline to separate — so the task grammar reports every step in-context. This is
// the grammar SEAM's purpose (kit.PlanGrammar is an interface precisely so a driver
// supplies its own); it is not a workaround.
//
// EffectiveDo + ContextsLabel keep the canonical checkkit behavior, so a `run:`
// step's do:act and a `check:` step's do:assert still resolve exactly as everywhere
// else, and a step MAY still author `context:` (it is simply not gated on).
type taskGrammar struct {
	base interface {
		EffectiveDo(op *spec.Op) spec.DoMode
		ContextsLabel(op *spec.Op) string
	}
}

func (g taskGrammar) EffectiveDo(op *spec.Op) spec.DoMode { return g.base.EffectiveDo(op) }

// InContext is always true: a task's steps run regardless of build/runtime mode.
func (g taskGrammar) InContext(op *spec.Op, runtime bool) bool { return true }

func (g taskGrammar) ContextsLabel(op *spec.Op) string { return g.base.ContextsLabel(op) }
