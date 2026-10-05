# AGENTS.md — plugin-task

Standalone plugin repo for the generic declarative TASK runner (`kind:task`,
`command:task`, `verb:task`) plus five generic maintenance verbs
(`verb:git-submodules`, `verb:file-parity`, `verb:splice-region`,
`verb:module-pins`, `verb:prune`). The Go module lives at `candy/plugin-task/` (module path
`github.com/opencharly/plugin-task/candy/plugin-task`); the root `charly.yml`
declares `discover: candy` so the repo is a project and its candy is scanned.

Canonical files:

- `candy/plugin-task/charly.yml` — the `plugin-task:` candy entity (`plugin:`
  block, `plan:` checks).
- `candy/plugin-task/plugin.go` — `NewProvider`/`NewMeta` and the seven
  capability declarations.
- `candy/plugin-task/grammar.go`, `graph.go`, `runner.go`, `incremental.go`,
  `verbs.go`, `cli.go` — the task engine, dependency graph, incremental
  semantics, the five maintenance verbs, and the CLI.
- `candy/plugin-task/schema/task.cue` + `params/cue_types_gen.go` — the
  self-contained schema and its generated types.
- `candy/plugin-task/cmd/serve/main.go` — the out-of-process serve shim.
- `scripts/gate-task.sh` — the repo's own gate helper: builds a real charly binary
  with this candy compiled in and drives `command:task` + the five maintenance
  verbs live.
- `scripts/check-coherence.sh` — the control that charly's own `sdk`/`spec` pins
  lead this module's requires. Called by `gate-task.sh` (before its build) and by
  the CI `coherence` job; `--self-test` proves it can fail.
- `.github/workflows/ci.yml` + `.github/workflows/tag-on-merge.yml`.
- `README.md` — user overview only; never agent guidance.

## Load these skills first (R0)

- `/charly-internals:plugin` — the plugin authoring reference: the `plugin:`
  block, the unified Provider model, the per-plugin CUE-schema contract,
  placement. Load before touching the provider or schema.
- `/charly-image:layer` — the candy authoring reference (`charly.yml` schema,
  `plan:` step verbs incl. `check:`). Load before editing any entity field or
  plan step.
- `/charly-internals:git-workflow` — before any git/PR action.

There is no dedicated `/charly-*:task` owning skill — this repo's candy carries
no `skill:` entity. The gap is recorded against `opencharly/opencharly#291`;
when one is authored, add it here.

## Build / validate / test

- `cd candy/plugin-task && go build ./...` — compile the plugin module.
- `cd candy/plugin-task && go test ./...` — the dependency closure, CLI parsing,
  validation, and incremental-semantics tests.
- `cd candy/plugin-task && gofmt -l .` — formatting (empty output passes).
- `charly box validate` at the repo root — the structural check (the candy +
  `plugin:` block, CUE schema).
- `scripts/gate-task.sh` at the repo root — the live gate: it clones charly at
  `CHARLY_REF` (default `main`), builds a binary with this candy compiled in, and
  drives `command:task` plus the five maintenance verbs against real git fixtures.
  It is the ONLY live execution of those verbs, so it must keep running.
- CI runs two jobs besides the Go gates: `coherence` on every PR (seconds — the
  same `scripts/check-coherence.sh` control the gate runs, so the vintage-coherence
  failure cannot reach merge), and `gate` on `main` and on demand (the full live
  proof, on the tree that ships). A change to the verbs is proven by running
  `scripts/gate-task.sh` locally; neither job is a substitute for that.
- The merge gate is the **org-wide** `charly/pr-validator` (required check
  `validate / validate`, defined in `opencharly/.github`); this repo has **no**
  per-repo candy gate.

## Modify this repo

- Edit the `plugin-task:` candy entity, the Go source, and `schema/task.cue`
  **together** — the schema is the single source for the `params/` struct, so a
  field change not mirrored in the schema desyncs the generated types.
- The task body is the base-schema `#Task`, validated host-side against
  `#TaskValue`; keep the engine reusing `kit.RunPlan` + `checkkit.VerbResolver`
  over `kit.ShellExecutor{}` — there is no second execution engine.
- The five maintenance verbs stay domain-neutral and parameterized by the repo's
  own data; never bake an org, repo, or distro name into the plugin.

## Landing

Every change lands through a pull request gated by the org-required
`charly/pr-validator`. The landing mechanics — the `feat/` branch, the PR-only
rule, `CHANGELOG/` history, and the tag-on-merge CalVer — are owned by
`/charly-internals:git-workflow` and the umbrella `AGENTS.md` /
`charly/AGENTS.md`; this signpost points at them and does not restate them.
