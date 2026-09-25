#!/usr/bin/env bash
# The in-repo fresh-rebuild gate that EXECUTES the changed code path.
#
# plugin-task's command:task + maintenance verbs are compiled-in: they need the
# host's loaded project over the reverse channel, so they cannot be exercised by
# an out-of-process `charly box validate`. This gate proves them the only honest
# way: build a real charly binary with candy/plugin-task compiled IN, then drive
# `charly task` and each maintenance verb end to end.
#
# The charly checkout is pinned to v2026.267.2313 — the tag that compiles
# candy/plugin-task into the binary. A LOCAL `-replace` points that compiled-in
# module at THIS working tree, so the artifact under test IS the changed source.
# (v2026.267.2313 predates the maintenance verbs, so without the replace the verb
# words would not resolve — exactly the failure this gate would catch.)
#
# Usage: scripts/gate-task.sh   (from the repo root; needs git, go, a network)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PLUGIN_DIR="$ROOT/candy/plugin-task"
CHARLY_TAG="${CHARLY_TAG:-v2026.267.2313}"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/plugin-task-gate.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

echo "== fetching charly $CHARLY_TAG (compiles candy/plugin-task in) =="
git clone --depth 1 --branch "$CHARLY_TAG" https://github.com/opencharly/charly.git "$WORK/charly" 2>&1 | tail -1

export GOTMPDIR="${GOTMPDIR:-$(mktemp -d)}"
export GOWORK=off
export GOFLAGS=-mod=mod
(
  cd "$WORK/charly/charly"
  # Point the already-compiled-in plugin module at THIS working tree so the built
  # binary ships the changed source.
  go mod edit -replace="github.com/opencharly/plugin-task/candy/plugin-task=$PLUGIN_DIR"
  echo "== building charly with candy/plugin-task compiled in from the working tree =="
  go build -buildvcs=false -ldflags "-X main.BuildCalVer=plugin-task-gate" -o "$WORK/charly-bin" .
)

CH="$WORK/charly-bin"

# ---------------------------------------------------------------------------
# 1) command:task — list / run / fail / --json
# ---------------------------------------------------------------------------
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

echo "== charly task list =="
"$CH" task list | grep -q '^greet ' || { echo "FAIL: greet not listed" >&2; exit 1; }
greet_out="$("$CH" task greet)"; echo "$greet_out" | grep -q '0 failed' || { echo "FAIL: greet did not run" >&2; exit 1; }
if "$CH" task failtask; then echo "FAIL: a failing task must exit non-zero" >&2; exit 1; fi
json="$("$CH" task failtask --json 2>&1 || true)"
echo "$json" | grep -q '"failed": 1' || { echo "FAIL: --json report missing the tally" >&2; exit 1; }
# ---------------------------------------------------------------------------
# 2) The four maintenance verbs, each exercised LIVE on a real git fixture.
# ---------------------------------------------------------------------------
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
FIX="$WORK/fixture"
mkdir -p "$FIX"
G() { git -c user.name=t -c user.email=t@t "$@"; }

# submodule source with two commits.
(
  cd "$FIX" && git init -q -b main subsrc && cd subsrc
  echo v1 > f && git add -A && G commit -qm v1
  C1="$(git rev-parse HEAD)"
  echo v2 > f && G commit -qam v2
  C2="$(git rev-parse HEAD)"
  printf 'C1=%s\nC2=%s\n' "$C1" "$C2" > "$FIX/shas"
)
# pinned repo `src` pins target -> v2; umbrella `umb` starts stale at v1.
for repo in src umb; do
  mkdir -p "$FIX/$repo"; cd "$FIX/$repo"; git init -q -b main
  echo "$repo" > r && git add -A && G commit -qm base
  printf '[submodule "target"]\n\tpath = target\n\turl = %s\n' "$FIX/subsrc" > .gitmodules
  git add .gitmodules
  . "$FIX/shas"
  if [ "$repo" = src ]; then SHA="$C2"; else SHA="$C1"; fi
  git update-index --add --cacheinfo "160000,$SHA,target"
  G commit -qm "pin target"
done
# Check out the umb submodule so `bump` can git-switch it.
(cd "$FIX/umb" && git -c protocol.file.allow=always submodule update --init -q target)

cat > "$FIX/umb/charly.yml" <<'YML'
version: 2026.261.1747
pins-verify:
  task:
    description: verify policy B
    dir: .
    plan:
      - check: the pin holds
        git-submodules: {mode: verify, pinned_from: ../src, pin_map: {target: target}}
        context: [runtime]
pins-bump:
  task:
    description: bump the target pin from src
    dir: .
    plan:
      - run: bump
        git-submodules: {mode: bump, pinned_from: ../src, pin_map: {target: target}}
        context: [deploy]
parity:
  task:
    description: file parity
    dir: .
    plan:
      - check: the pair is identical
        file-parity: {mode: check, pairs: [{left: r, right: r.copy}]}
        context: [runtime]
splice:
  task:
    description: splice a marked region
    dir: .
    plan:
      - run: splice
        splice-region: {target: TARGET, fragment: FRAG, begin: "BEGIN-X", end: "END-X"}
        context: [deploy]
mods:
  task:
    description: adopt the source pins under tools/*
    dir: .
    plan:
      - run: adopt and tidy
        module-pins: {source_go_mod: core/go.mod, glob: "tools/*", keys: [github.com/opencharly/sdk]}
        context: [deploy]
YML

cd "$FIX/umb"
cp r r.copy
printf 'head\nBEGIN-X\nold\nEND-X\ntail\n' > TARGET
printf 'x\nBEGIN-X\nnew\nEND-X\ny\n' > FRAG
mkdir -p core tools/m1 tools/stub
printf 'module core\n\ngo 1.26\n\nrequire github.com/opencharly/sdk v1.2.3\n' > core/go.mod
printf 'module github.com/opencharly/sdk\n\ngo 1.26\n' > tools/stub/go.mod
printf 'package sdk\n' > tools/stub/sdk.go
printf 'module m1\n\ngo 1.26\n\nrequire github.com/opencharly/sdk v1.0.0\n\nreplace github.com/opencharly/sdk => ../stub\n' > tools/m1/go.mod
printf 'package m1\n\nimport _ "github.com/opencharly/sdk"\n' > tools/m1/m1.go

echo "== verb:git-submodules verify (stale -> must FAIL) =="
if "$CH" task pins-verify; then echo "FAIL: stale pin must fail verify" >&2; exit 1; fi

echo "== verb:git-submodules bump (must move the pin to src's twin) =="
bump_out="$("$CH" task pins-bump)"; echo "$bump_out" | grep -q '0 failed' || { echo "FAIL: bump did not run" >&2; exit 1; }
want="$(git -C "$FIX/src" ls-tree HEAD target | awk '{print $3}')"
got="$(git ls-files -s target | awk '{print $2}')"
[ "$want" = "$got" ] || { echo "FAIL: bump did not adopt src's pin ($got != $want)" >&2; exit 1; }

echo "== verb:git-submodules verify (now current -> must PASS) =="
verify_out="$("$CH" task pins-verify)"; echo "$verify_out" | grep -q '0 failed' || { echo "FAIL: verify must pass after bump" >&2; exit 1; }

echo "== verb:file-parity (identical must PASS, drift must FAIL) =="
par_out="$("$CH" task parity)"; echo "$par_out" | grep -q '0 failed' || { echo "FAIL: identical pair must pass" >&2; exit 1; }
echo drifted >> r.copy
if "$CH" task parity; then echo "FAIL: drifted pair must fail" >&2; exit 1; fi
cp r r.copy

echo "== verb:splice-region (must rewrite the region) =="
spl_out="$("$CH" task splice)"; echo "$spl_out" | grep -q '0 failed' || { echo "FAIL: splice did not run" >&2; exit 1; }
grep -q 'new' TARGET || { echo "FAIL: splice did not apply the fragment region" >&2; exit 1; }
if grep -q 'old' TARGET; then echo "FAIL: splice left the stale region" >&2; exit 1; fi

echo "== verb:module-pins (must adopt the source pin) =="
mods_out="$("$CH" task mods)"; echo "$mods_out" | grep -q '0 failed' || { echo "FAIL: module-pins did not run" >&2; exit 1; }
grep -q 'github.com/opencharly/sdk v1.2.3' tools/m1/go.mod || { echo "FAIL: module-pins did not adopt the pin" >&2; exit 1; }

echo "gate-task: PASS — command:task + all four maintenance verbs executed live against charly $CHARLY_TAG"
