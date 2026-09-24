#!/usr/bin/env bash
# The in-repo fresh-rebuild gate that EXECUTES the changed code path.
#
# plugin-task's command:task is compiled-in-only (it needs the host's loaded
# project over the reverse channel), so it cannot be exercised by an
# out-of-process `charly box validate`. This gate proves it the only honest way:
# build a real charly binary with candy/plugin-task compiled IN (the in-proc
# placement a consumer uses), then drive `charly task` end to end.
#
# The charly checkout is pinned to v2026.267.1938 — the tag that first carries
# the generic host-value-gated kind seam (`#TaskValue` → spec.KindValueDefs +
# foldHostValueGatedKind) this plugin's kind:task registration needs. Against an
# older charly the `task:` node fails "no input def registered", which is exactly
# the failure this gate would catch.
#
# Usage: scripts/gate-task.sh   (from the repo root; needs git, go, a network)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PLUGIN_DIR="$ROOT/candy/plugin-task"
CHARLY_TAG="${CHARLY_TAG:-v2026.267.1938}"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/plugin-task-gate.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

echo "== fetching charly $CHARLY_TAG (carries the host-value-gated kind seam) =="
git clone --depth 1 --branch "$CHARLY_TAG" https://github.com/opencharly/charly.git "$WORK/charly" 2>&1 | tail -1

# Compile candy/plugin-task INTO the charly binary (the in-proc placement).
# A throwaway file + a replace directive: the checkout is never committed.
cat > "$WORK/charly/charly/plugins_task_gate.go" <<'GO'
package main

import taskplugin "github.com/opencharly/plugin-task/candy/plugin-task"

func init() { registerCompiledPlugin(taskplugin.NewProvider(), taskplugin.NewMeta()) }
GO

export GOTMPDIR="${GOTMPDIR:-$(mktemp -d)}"
export GOWORK=off
export GOFLAGS=-mod=mod
(
  cd "$WORK/charly/charly"
  go mod edit \
    -require=github.com/opencharly/plugin-task/candy/plugin-task@v0.0.0 \
    -replace=github.com/opencharly/plugin-task/candy/plugin-task="$PLUGIN_DIR"
  echo "== building charly with candy/plugin-task compiled in =="
  go build -buildvcs=false -ldflags "-X main.BuildCalVer=plugin-task-gate" -o "$WORK/charly-bin" .
)

# A project declaring tasks, with NO discover: — the plugin is compiled in, so
# the host resolves kind:task through the seam alone (the in-proc path).
PROJ="$WORK/proj"
mkdir -p "$PROJ"
cat > "$PROJ/charly.yml" <<'YML'
version: 2026.261.1747
greet:
  task:
    description: greet the user
    plan:
      - run: emit the greeting
        command: "echo hello-from-task"
        context: [deploy]
failtask:
  task:
    description: a task that fails
    plan:
      - run: this fails
        command: "false"
        context: [deploy]
YML

cd "$PROJ"
CH="$WORK/charly-bin"

echo "== charly task list =="
"$CH" task list
# greet must be enumerated.
if ! "$CH" task list | grep -q '^greet '; then
  echo "FAIL: greet not listed" >&2
  exit 1
fi

echo "== charly task greet (must run the changed path and exit 0) =="
"$CH" task greet
out="$("$CH" task greet)"
echo "$out" | grep -q 'emit the greeting' || { echo "FAIL: greet step not executed" >&2; exit 1; }
echo "$out" | grep -q '0 failed' || { echo "FAIL: greet reported a failure" >&2; exit 1; }

echo "== charly task failtask (must exit non-zero) =="
if "$CH" task failtask; then
  echo "FAIL: a failing task must exit non-zero" >&2
  exit 1
fi

echo "== charly task failtask --json (report emitted before the non-zero exit) =="
json="$("$CH" task failtask --json 2>&1 || true)"
echo "$json" | grep -q '"failed": 1' || { echo "FAIL: --json report missing the failure tally" >&2; exit 1; }

echo "gate-task: PASS — the compiled-in command:task path executed live against charly $CHARLY_TAG"
