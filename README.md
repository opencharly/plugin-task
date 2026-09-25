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
| `verb:git-submodules` | `git-submodules:` verb step | status / bump / verify `.gitmodules` pins (incl. the policy-B comparison of a repo's pins against a pinned checkout's gitlinks) |
| `verb:file-parity` | `file-parity:` verb step | check / sync that paired files are byte-identical |
| `verb:splice-region` | `splice-region:` verb step | splice a marked region from a fragment into a target |
| `verb:module-pins` | `module-pins:` verb step | adopt a source go.mod's require pins across every module a glob matches, then tidy |

## Generic maintenance verbs

The four maintenance verbs replace repository shell scripts. Each is **domain-neutral**
and parameterized by the repo's OWN data (paths, pairs, pins) carried in its authored
input, so the plugin stays reusable by any repository — no org name, repo name, or
distro list is baked in. They are host-native: like the task engine they act on the
repository checkout the charly process runs in.

```yaml
# Bump every submodule pin per policy B (distro-* pins == charly's gitlinks;
# everything else rolls to its own default-branch HEAD).
sync:
  task:
    description: Bump all submodule pins per policy B
    plan:
      - run: bump the pins
        git-submodules:
          mode: bump
          pinned_from: charly
          pin_map:
            box/arch: box/arch
            box/fedora: box/fedora
        context: [deploy]

# Assert the harness config files are identical to charly's twins.
harness:
  task:
    description: Check harness config parity
    plan:
      - check: the shared harness files are identical
        file-parity:
          pairs:
            - {left: .claude/hooks/pre-commit-gate.sh, right: charly/.claude/hooks/pre-commit-gate.sh}
        context: [runtime]

# Splice the generated skill dispatcher into AGENTS.md.
skills:
  task:
    description: Splice the generated R0 dispatcher into AGENTS.md
    plan:
      - run: splice the dispatcher region
        splice-region:
          target: AGENTS.md
          fragment: marketplace/DISPATCHER.md
          begin: "<!-- BEGIN GENERATED SKILL DISPATCHER -->"
          end: "<!-- END GENERATED SKILL DISPATCHER -->"
        context: [deploy]

# Adopt the shared sdk/spec pins across every tools/* module.
mods-tidy:
  task:
    description: Adopt the shared sdk/spec pins, then tidy
    plan:
      - run: adopt and tidy
        module-pins:
          source_go_mod: charly/go.mod
          glob: tools/*
          keys: [github.com/opencharly/sdk, github.com/opencharly/spec]
        context: [deploy]
```

Each verb's input is validated against its `#<Name>Input` CUE def at load; the handler
returns a `pass`/`fail`/`skip` verdict the plan harness decodes. A `git-submodules
verify` FAILS LOUDLY on a missing gitlink on either side (never a vacuous empty==empty
pass); `module-pins check` reports every stale module.


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
