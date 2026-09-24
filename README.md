# plugin-task

The generic declarative **task runner** for [opencharly/charly](https://github.com/opencharly/charly):
the replacement for a Taskfile. It is a standalone plugin candy repo (the candy
de-submodule cutover); the Go module lives at `candy/plugin-task/` with module path
`github.com/opencharly/plugin-task/candy/plugin-task`, fetched at the pinned tag and
compiled into charly.

## What it provides

| Capability | Surface | Purpose |
|---|---|---|
| `kind:task` | a `task:` node in `charly.yml` | a named, reusable plan using the same `#Step`/`#Op` grammar a candy's `plan:` uses |
| `command:task` | `charly task [list] [<name>] …` | run a task (and its `depends_on` closure) on the host |
| `verb:task` | `task:` verb step in a plan | compose a declared task into a candy/box/task plan |

## Authoring a task

```yaml
build:
  task:
    description: Build the charly binary
    dir: "$HOME/src/charly"      # $HOME, the task vars, and resolved params all expand
    vars: {CALVER: "2026.267.1200"}
    depends_on: [tidy]
    sources: ["charly/**/*.go", "go.work.sum"]
    generates: ["bin/charly"]
    params: {TARGET: {description: "build target", default: ""}}
    plan:
      - check: go is available
        command: go version
      - run: build the binary
        command: ./scripts/bootstrap-charly.sh
      - check: the binary reports its version
        command: bin/charly version
        stdout: [{matches: "^[0-9]{4}\\.[0-9]{3}\\.[0-9]{4}$"}]
```

Run it:

```bash
charly task build                 # run (with its depends_on closure)
charly task build --param TARGET=x
charly task build --dry-run       # resolve + validate, execute nothing
charly task list --json           # every declared task as JSON
charly task --all                 # every task in dependency order
```

## Full Go-Task parity

`description`, `dir`, `env`, `vars`, `depends_on`, `sources`, `generates`, `status`,
`preconditions`, `silent`, `interactive` (refuses to run without a TTY rather than
silently capturing output), `platforms`, `exclude_platforms`, `timeout` (a ceiling
over the walk), `continue_on_error`, `params` (declared defaults overlaid with
`--param` overrides; `required: true` is enforced), and the ordered `plan:`.

## Design

The engine reuses the SDK's existing plan machinery — no second execution engine:

- `kit.RunPlan` walks the plan; `checkkit.PlanGrammar` supplies the do-mode/context grammar.
- `checkkit.VerbResolver` dispatches each step's verb through the host's provider
  registry over the reverse channel (`InvokeProvider`), so a task can use any verb,
  any plugin verb, `include:` composition, and `agent-*` steps.
- `kit.ShellExecutor{}` is the host venue; `dir:` is applied by chdir around the
  sequential walk (the plan grammar has no cwd field), and `${VAR}`/`$VAR` in `dir:`
  and env expand against the task's vars, params, and the process environment.
