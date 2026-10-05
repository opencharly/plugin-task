#!/usr/bin/env bash
# Assert that a charly checkout's OWN sdk/spec pins LEAD this module's requires.
#
# WHY THIS IS A CONTROL, NOT A NICETY. scripts/gate-task.sh builds a real charly
# binary with candy/plugin-task compiled in, through a LOCAL `-replace`. That replace
# puts THIS module's `require`s (github.com/opencharly/sdk, github.com/opencharly/spec)
# into charly's module graph, and MVS takes the max — so a charly ref whose OWN pins
# are older RAISES sdk/spec past the APIs charly's OTHER compiled-in plugins were built
# against, and the build dies fifty errors deep in plugin-box / plugin-build /
# plugin-cmd with errors that name neither this repo nor the cause. Measured at charly
# v2026.267.2313 (pinning sdk v0.2026266.1111 against this module's v0.2026276.1822):
# 50 such errors, and the gate never reached a single verb assertion (plugin-task#14).
#
# The ordering below is the invariant that makes the build coherent, and asserting it
# HERE — before the build — makes a lapse name itself in one line instead of fifty.
#
# ONE IMPLEMENTATION, TWO CALLERS (R3): scripts/gate-task.sh runs it against the charly
# checkout it is about to build, and the CI `coherence` job runs it against charly
# `main`'s go.mod fetched over HTTP. Neither carries its own copy.
#
# Usage: scripts/check-coherence.sh <charly-go.mod> [<plugin-go.mod>] [<label>]
#        scripts/check-coherence.sh --self-test
#
# Exit 0 when charly's pins lead (or equal) this module's requires. Exit 1 otherwise,
# naming the module and both versions. A module ABSENT from either file is an error,
# never a silent pass — the control must not be vacuously green.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Both go.mod shapes must be read: the BLOCK form (`require (\n\t<path> <ver>\n)`,
# where $1 is the path) and the single-line form (`require <path> <ver>`, where $1 is
# the `require` keyword). A `replace` line carries `=>` in the version slot, so
# `$2/$3 ~ /^v/` reads the REQUIRE and never a same-path replace directive.
mod_pin() {
  awk -v m="$2" '
    $1==m && $2 ~ /^v/            {print $2; exit}
    $1=="require" && $2==m && $3 ~ /^v/ {print $3; exit}
  ' "$1"
}

# CalVer components are NOT zero-padded ("0.2026277.352" vs "0.2026276.1822"), so the
# three dotted parts are compared numerically, never as strings.
calver_ge() { # $1 >= $2 ?
  local -a hi lo
  IFS=. read -ra hi <<<"${1#v}"
  IFS=. read -ra lo <<<"${2#v}"
  local i
  for i in 0 1 2; do
    (( 10#${hi[i]:-0} > 10#${lo[i]:-0} )) && return 0
    (( 10#${hi[i]:-0} < 10#${lo[i]:-0} )) && return 1
  done
  return 0
}

check() {
  local charly_gomod="$1" plugin_gomod="$2" label="${3:-charly}"
  local fail=0 m have want
  for m in github.com/opencharly/sdk github.com/opencharly/spec; do
    have="$(mod_pin "$charly_gomod" "$m")"
    want="$(mod_pin "$plugin_gomod" "$m")"
    if [ -z "$have" ] || [ -z "$want" ]; then
      echo "FAIL: $m is not required by both files ($label: '${have:-<absent>}', module: '${want:-<absent>}')" >&2
      fail=1
      continue
    fi
    if ! calver_ge "$have" "$want"; then
      echo "FAIL: $label pins $m $have, but this working tree requires $want." >&2
      echo "      The -replace puts the working tree's requires in the graph and MVS takes the" >&2
      echo "      max, so $m would be raised past what $label's other plugins were built for." >&2
      echo "      Build against a charly ref whose pins lead the working tree's (main does, once" >&2
      echo "      charly has re-synced), or re-sync charly first." >&2
      fail=1
    fi
  done
  if [ "$fail" -eq 0 ]; then
    echo "coherence OK: $label's sdk/spec pins lead this module's requires"
  fi
  return "$fail"
}

self_test() {
  local d
  d="$(mktemp -d)"
  # `d` is function-local, so the EXIT trap must not assume it is still set when an
  # assertion returns early (set -u would abort the trap itself).
  trap 'rm -rf "${d:-}"' EXIT
  # charly LEADING this module (equal on spec, ahead on sdk) — must pass.
  printf 'module m\n\nrequire github.com/opencharly/sdk v0.2026277.352\nrequire github.com/opencharly/spec v0.2026276.1636\n' > "$d/lead.mod"
  # LAGGING on sdk (the v2026.267.2313 shape) — must fail.
  printf 'module m\n\nrequire github.com/opencharly/sdk v0.2026266.1111\nrequire github.com/opencharly/spec v0.2026267.834\n' > "$d/lag.mod"
  # ABSENT pin — must fail, never pass silently.
  printf 'module m\n\nrequire github.com/opencharly/spec v0.2026276.1636\n' > "$d/missing.mod"
  printf 'module m\n\nrequire github.com/opencharly/sdk v0.2026276.1822\nrequire github.com/opencharly/spec v0.2026276.1636\n' > "$d/req.mod"

  check "$d/lead.mod" "$d/req.mod" synthetic >/dev/null || {
    echo "SELF-TEST FAIL: a leading ref must pass" >&2; return 1; }
  if check "$d/lag.mod" "$d/req.mod" synthetic >/dev/null 2>&1; then
    echo "SELF-TEST FAIL: a lagging ref must fail" >&2; return 1
  fi
  if check "$d/missing.mod" "$d/req.mod" synthetic >/dev/null 2>&1; then
    echo "SELF-TEST FAIL: an absent pin must fail, never pass silently" >&2; return 1
  fi
  echo "self-test OK: leading passes, lagging fails, absent pin fails"
}

case "${1:-}" in
  --self-test) self_test ;;
  "")
    echo "usage: $0 <charly-go.mod> [<plugin-go.mod>] [<label>]" >&2
    echo "       $0 --self-test" >&2
    exit 2 ;;
  *)
    check "$1" "${2:-$ROOT/candy/plugin-task/go.mod}" "${3:-charly}" ;;
esac
