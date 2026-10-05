#!/usr/bin/env bash
# The in-repo fresh-rebuild gate that EXECUTES the changed code path.
#
# plugin-task's command:task + maintenance verbs are compiled-in: they need the
# host's loaded project over the reverse channel, so they cannot be exercised by
# an out-of-process `charly box validate`. This gate proves them the only honest
# way: build a real charly binary with candy/plugin-task compiled IN, then drive
# `charly task` and each maintenance verb end to end.
#
# The charly checkout is built from CHARLY_REF, defaulting to `main` — the ref
# that ADVANCES WITH the plugin source. A pinned tag cannot work here: the LOCAL
# `-replace` below makes Go read THIS working tree's go.mod, whose `require`s
# (github.com/opencharly/sdk, spec) are same-day, while a tag's OTHER compiled-in
# plugins are frozen at the tag's vintage. MVS takes the max, so the working
# tree's sdk/spec win and the tag's sibling plugins — built against the older
# kit/deploykit APIs — stop compiling (`not enough arguments in call to
# kit.ScaffoldCandy`, `deploykit.ResolveContainer`, …). Measured on this repo:
# v2026.267.2313 (pinning sdk v0.2026266.1111 against the working tree's
# v0.2026276.1822) fails to build at all; the identical gate at a ref whose own
# pins LEAD the working tree's builds and runs every verb. `main` gives that
# ordering by construction, and the assertion below — scripts/check-coherence.sh,
# shared with the CI `coherence` job — fails LOUDLY, naming the module and both
# versions, if it ever lapses (charly lags the plugin's bump).
#
# Usage: scripts/gate-task.sh   (from the repo root; needs git, go, a network)
#        CHARLY_REF=<tag|sha> scripts/gate-task.sh   (reproduce a specific vintage)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PLUGIN_DIR="$ROOT/candy/plugin-task"
CHARLY_REF="${CHARLY_REF:-main}"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/plugin-task-gate.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

echo "== fetching charly $CHARLY_REF (compiles candy/plugin-task in) =="
git clone --depth 1 --branch "$CHARLY_REF" https://github.com/opencharly/charly.git "$WORK/charly" 2>&1 | tail -1

# --- the coherence gate: charly's own pins must LEAD the working tree's ---------
# The `-replace` below puts the working tree's requires into the module graph, and MVS
# takes the max, so a charly ref that lags raises sdk/spec past the sibling plugins'
# APIs and the build dies in plugin-box/plugin-build/plugin-cmd, far from the cause.
# Assert the ordering here — BEFORE the build — so a lapse names itself in one line.
# ONE implementation, two callers: this is scripts/check-coherence.sh, shared with the
# CI `coherence` job, which runs the same control against charly `main` over HTTP (R3).
"$ROOT/scripts/check-coherence.sh" "$WORK/charly/charly/go.mod" "$PLUGIN_DIR/go.mod" "charly $CHARLY_REF"

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
# 2) The generic maintenance verbs, each exercised LIVE on a real git fixture.
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

# --- bump-order fixture: `src` is ITSELF a rolling submodule of `umb2`, stale, and
# its `inner` gitlink advances during the bump. `target2` must be pinned from src's
# NEW gitlink. A wrong order would pin target2 from src's OLD gitlink and then
# advance src, violating policy B.
(
  cd "$FIX" && git init -q -b main leaf && cd leaf
  echo L1 > l && git add -A && G commit -qm L1; L1="$(git rev-parse HEAD)"
  echo L2 > l && G commit -qam L2; L2="$(git rev-parse HEAD)"
  printf 'L1=%s\nL2=%s\n' "$L1" "$L2" > "$FIX/leafshas"
  cd "$FIX" && git init -q -b main srcorigin && cd srcorigin
  echo seed > s && git add -A && G commit -qm seed
  printf '[submodule "inner"]\n\tpath = inner\n\turl = %s\n' "$FIX/leaf" > .gitmodules
  git add .gitmodules && G commit -qm gm
  . "$FIX/leafshas"
  git update-index --add --cacheinfo "160000,$L1,inner" && G commit -qm "inner=L1"; A="$(git rev-parse HEAD)"
  git update-index --add --cacheinfo "160000,$L2,inner" && G commit -qm "inner=L2"; B="$(git rev-parse HEAD)"
  printf 'A=%s\nB=%s\n' "$A" "$B" > "$FIX/order"
  cd "$FIX" && git clone -q --bare srcorigin srcorigin.git
)
mkdir -p "$FIX/umb2"; cd "$FIX/umb2"; git init -q -b main
echo u2 > u && git add -A && G commit -qm u
git -c protocol.file.allow=always submodule add -q "$FIX/srcorigin.git" src
git -c protocol.file.allow=always submodule add -q "$FIX/leaf" target2
G commit -qm subs
. "$FIX/order"; . "$FIX/leafshas"
git update-index --add --cacheinfo "160000,$A,src"
git update-index --add --cacheinfo "160000,$L1,target2"
G commit -qm "stale pins"
git config -f .gitmodules submodule.src.branch main
git config -f .gitmodules submodule.target2.branch main
git add .gitmodules && G commit -qm branches
git -c protocol.file.allow=always submodule update --init -q src target2

cat > "$FIX/umb/charly.yml" <<'YML'
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
splice-check:
  task:
    description: assert the marked region is current
    dir: .
    plan:
      - check: the region is current
        splice-region: {mode: check, target: TARGET, fragment: FRAG, begin: "BEGIN-X", end: "END-X"}
        context: [runtime]
mods:
  task:
    description: adopt the source pins under tools/*
    dir: .
    plan:
      - run: adopt and tidy
        module-pins: {source_go_mod: core/go.mod, glob: "tools/*", keys: [github.com/opencharly/sdk]}
        context: [deploy]
mods-check:
  task:
    description: assert the source pins hold under tools/*
    dir: .
    plan:
      - check: the pins hold
        module-pins: {mode: check, source_go_mod: core/go.mod, glob: "tools/*", keys: [github.com/opencharly/sdk]}
        context: [runtime]
missing-pin:
  task:
    description: verify against a pinned path with no gitlink (must fail loudly)
    dir: .
    plan:
      - check: the missing twin fails
        git-submodules: {mode: verify, pinned_from: ../src, pin_map: {target: nope/absent}}
        context: [runtime]
prune:
  task:
    description: reap merged-upstream worktrees and branches (local ancestry only)
    dir: .
    plan:
      - run: prune merged worktrees and branches
        prune: {mode: prune, base: main, local_only: true}
        context: [deploy]
YML

# A second project exercising the LOAD-BEARING bump order (pinned_from is itself a
# rolling stale submodule).
cat > "$FIX/umb2/charly.yml" <<'YML'
order-bump:
  task:
    description: bump target2 from src (which itself rolls first)
    dir: .
    plan:
      - run: bump
        git-submodules: {mode: bump, pinned_from: src, pin_map: {target2: inner}}
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

echo "== verb:git-submodules verify (missing twin -> must FAIL loudly, never vacuous) =="
if "$CH" task missing-pin; then echo "FAIL: a missing gitlink must fail verify" >&2; exit 1; fi

echo "== verb:git-submodules bump (uninitialized submodule -> must FAIL loudly, never walk up) =="
# charly#768: a present-but-uninitialized submodule dir must NOT be operated on via
# git's walk-up to the umbrella. Build a fixture with an uninit submodule, record the
# UMBRELLA's HEAD, run bump, and assert (a) it fails and (b) the umbrella is untouched.
UNI="$WORK/uninit"; mkdir -p "$UNI"
(
  cd "$UNI" && git init -q -b main
  echo u > u.txt && git add -A && G commit -qm u
  printf '[submodule "gap"]\n\tpath = gap\n\turl = %s\n\tbranch = main\n' "$FIX/subsrc" > .gitmodules
  git add .gitmodules && G commit -qm gm
  git update-index --add --cacheinfo "160000,$C2,gap" && G commit -qm pin
  mkdir -p gap   # present-but-UNINITIALIZED
)
cat > "$UNI/charly.yml" <<'YML'
bump-all:
  task:
    description: bump over a set containing an uninitialized submodule
    dir: .
    plan:
      - run: bump
        git-submodules: {mode: bump}
        context: [deploy]
status-all:
  task:
    description: list the pins (an uninitialized submodule must not walk up)
    dir: .
    plan:
      - check: the pin table reports the submodule
        git-submodules: {mode: status}
        context: [runtime]
YML
umb_head_before="$(git -C "$UNI" rev-parse HEAD)"
umb_branch_before="$(git -C "$UNI" rev-parse --abbrev-ref HEAD)"
if (cd "$UNI" && "$CH" task bump-all); then
  echo "FAIL: bump over an uninitialized submodule must FAIL LOUD" >&2; exit 1
fi
umb_head_after="$(git -C "$UNI" rev-parse HEAD)"
umb_branch_after="$(git -C "$UNI" rev-parse --abbrev-ref HEAD)"
[ "$umb_head_before" = "$umb_head_after" ] || { echo "FAIL: the umbrella HEAD moved (walk-up detach): $umb_head_before -> $umb_head_after" >&2; exit 1; }
[ "$umb_branch_before" = "$umb_branch_after" ] || { echo "FAIL: the umbrella branch changed (walk-up detach): $umb_branch_before -> $umb_branch_after" >&2; exit 1; }
echo "   uninitialized-submodule bump failed loud; umbrella untouched ($umb_branch_before @ ${umb_head_before:0:9})"

echo "== verb:git-submodules status (uninitialized submodule -> '- (uninitialized)', never the umbrella HEAD) =="
# charly#768: the status read must go through submoduleAt too. Pre-fix, `git -C
# <empty-dir> rev-parse --short HEAD` walked up and printed the UMBRELLA's HEAD as the
# submodule's pin. Assert the marker is present AND the umbrella's own short HEAD is
# NOT reported as the pin.
status_out="$(cd "$UNI" && "$CH" task status-all)"
echo "$status_out" | grep -q '0 failed' || { echo "FAIL: status did not run" >&2; echo "$status_out"; exit 1; }
echo "$status_out" | grep -q -- '- (uninitialized)' || { echo "FAIL: uninitialized submodule not marked in status" >&2; echo "$status_out"; exit 1; }
umb_short="$(git -C "$UNI" rev-parse --short HEAD)"
if echo "$status_out" | grep -q "$umb_short"; then
  echo "FAIL: status printed the umbrella HEAD ($umb_short) as a submodule pin (walk-up regression)" >&2; echo "$status_out"; exit 1
fi
echo "   status reported '- (uninitialized)'; umbrella HEAD ($umb_short) not leaked as a pin"

echo "== verb:git-submodules bump ORDER (pinned_from is itself rolled first) =="
. "$FIX/order"; . "$FIX/leafshas"
(cd "$FIX/umb2" && "$CH" task order-bump) | grep -q '0 failed' || { echo "FAIL: order-bump did not run" >&2; exit 1; }
src_got="$(git -C "$FIX/umb2" ls-files -s src | awk '{print $2}')"
tgt_got="$(git -C "$FIX/umb2" ls-files -s target2 | awk '{print $2}')"
[ "$src_got" = "$B" ] || { echo "FAIL: src did not roll to $B (got $src_got)" >&2; exit 1; }
[ "$tgt_got" = "$L2" ] || { echo "FAIL: target2 pinned to $tgt_got, want L2=$L2 (L1=$L1 means stale src read)" >&2; exit 1; }

echo "== verb:file-parity (identical must PASS, drift must FAIL) =="
par_out="$("$CH" task parity)"; echo "$par_out" | grep -q '0 failed' || { echo "FAIL: identical pair must pass" >&2; exit 1; }
echo drifted >> r.copy
if "$CH" task parity; then echo "FAIL: drifted pair must fail" >&2; exit 1; fi
cp r r.copy

echo "== verb:splice-region (stale -> check must FAIL, sync -> current) =="
# A stale target: overwrite the region, then `check` must fail before sync fixes it.
printf 'head\nBEGIN-X\nstale\nEND-X\ntail\n' > TARGET
if "$CH" task splice-check; then echo "FAIL: a stale region must fail splice check" >&2; exit 1; fi
spl_out="$("$CH" task splice)"; echo "$spl_out" | grep -q '0 failed' || { echo "FAIL: splice did not run" >&2; exit 1; }
grep -q 'new' TARGET || { echo "FAIL: splice did not apply the fragment region" >&2; exit 1; }
if grep -q 'old' TARGET; then echo "FAIL: splice left the stale region" >&2; exit 1; fi
if grep -q 'stale' TARGET; then echo "FAIL: splice left the stale region" >&2; exit 1; fi
spl2_out="$("$CH" task splice-check)"; echo "$spl2_out" | grep -q '0 failed' || { echo "FAIL: splice check must pass after sync" >&2; exit 1; }

echo "== verb:module-pins (stale -> must FAIL, adopt -> must PASS) =="
if "$CH" task mods-check; then echo "FAIL: stale pins must fail module-pins check" >&2; exit 1; fi
mods_out="$("$CH" task mods)"; echo "$mods_out" | grep -q '0 failed' || { echo "FAIL: module-pins did not run" >&2; exit 1; }
grep -q 'github.com/opencharly/sdk v1.2.3' tools/m1/go.mod || { echo "FAIL: module-pins did not adopt the pin" >&2; exit 1; }
mods2_out="$("$CH" task mods-check)"; echo "$mods2_out" | grep -q '0 failed' || { echo "FAIL: module-pins check must pass after adopt" >&2; exit 1; }

# ---------------------------------------------------------------------------
# 3) verb:prune — reap merged-upstream worktrees + branches LIVE.
# The fixture is a real repo whose `main` IS the merge base, with a merged
# worktree/branch and an unmerged one; prune must remove the former and keep the
# latter. `local_only: true` keeps the gate hermetic (no gh, no network).
# ---------------------------------------------------------------------------
PRUNE="$WORK/pruneproj"
mkdir -p "$PRUNE"
(
  cd "$PRUNE" && git init -q -b main
  echo base > f && git add -A && G commit -qm base
  # merged branch = fast-forward onto main (ancestor)
  git branch feat/merged main
  # unmerged branch = a commit NOT on main
  git switch -q -c feat/wip && echo wip > w && git add -A && G commit -qm wip && git switch -q main
  # a linked session worktree on each
  git worktree add -q .worktrees/aa-merged feat/merged
  git worktree add -q .worktrees/bb-wip feat/wip
)
cat > "$PRUNE/charly.yml" <<'YML'
prune:
  task:
    description: reap merged-upstream worktrees and branches
    dir: .
    plan:
      - run: prune merged worktrees and branches
        prune: {mode: prune, base: main, local_only: true}
        context: [deploy]
YML
echo "== verb:prune (removes merged worktree+branch, keeps unmerged) =="
prune_out="$(cd "$PRUNE" && "$CH" task prune)"; echo "$prune_out" | grep -q '0 failed' || { echo "FAIL: prune did not run" >&2; echo "$prune_out"; exit 1; }
(cd "$PRUNE" && git rev-parse -q --verify refs/heads/feat/merged >/dev/null) && { echo "FAIL: merged branch survived prune" >&2; exit 1; }
(cd "$PRUNE" && git rev-parse -q --verify refs/heads/feat/wip >/dev/null) || { echo "FAIL: unmerged branch was pruned" >&2; exit 1; }
[ -d "$PRUNE/.worktrees/aa-merged" ] && { echo "FAIL: merged worktree survived prune" >&2; exit 1; }
[ -f "$PRUNE/.worktrees/bb-wip/.git" ] || { echo "FAIL: unmerged worktree was removed" >&2; exit 1; }

echo "gate-task: PASS — command:task + all five maintenance verbs executed live against charly $CHARLY_REF"
