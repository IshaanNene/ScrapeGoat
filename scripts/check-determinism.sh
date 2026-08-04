#!/usr/bin/env bash
# Fails if the crawl path reaches for the wall clock or global randomness.
#
# The engine takes time and entropy from injected sources so that a crawl can be
# replayed — see docs/design/0001-deterministic-crawl.md. Breaking that is silent:
# nothing fails, the output simply stops being reproducible, and you find out when
# a replay diverges months later. So it is checked mechanically.
#
# Runs alongside golangci-lint's forbidigo rule rather than instead of it, because
# this script is what CI actually gates on and needs no linter to be installed.
set -euo pipefail

# Packages inside the determinism boundary.
GUARDED=(internal/engine internal/clock internal/fetchlog)

# clock.go is the one place allowed to call the standard library's time package —
# it is the implementation the rest of the code depends on. Individual lines can
# also opt out with a trailing `//determinism:allow <reason>`, which keeps the
# exception visible at the call site rather than buried in this script.
ALLOWLIST='internal/clock/clock.go'

# A line whose first non-space character is // is prose. Documenting the rule
# must not trip the rule — otherwise the only way to explain why time.Now() is
# banned is to not write it down.
is_comment() {
  [[ "${1#*:*:}" =~ ^[[:space:]]*// ]]
}

fail=0

for pkg in "${GUARDED[@]}"; do
  while IFS= read -r hit; do
    file="${hit%%:*}"
    [[ "$file" == $ALLOWLIST ]] && continue
    [[ "$file" == *_test.go ]] && continue
    [[ "$hit" == *"//determinism:allow"* ]] && continue
    is_comment "$hit" && continue
    echo "  $hit"
    fail=1
  done < <(grep -rnE '\b(time\.(Now|Since|Sleep|After|Tick|NewTimer|NewTicker))\(' \
             --include='*.go' "$pkg" 2>/dev/null || true)
done

for pkg in "${GUARDED[@]}"; do
  while IFS= read -r hit; do
    file="${hit%%:*}"
    [[ "$file" == *_test.go ]] && continue
    [[ "$hit" == *"//determinism:allow"* ]] && continue
    is_comment "$hit" && continue
    # A method call on an injected *rand.Rand is fine; a package-level one is not.
    grep -qE '\b(rng|r|e\.rand|s\.engine\.rand)\.' <<<"$hit" && continue
    echo "  $hit"
    fail=1
  done < <(grep -rnE '(^|[^.[:alnum:]_])rand\.(Int|Uint|Float|Perm|Shuffle|N)' \
             --include='*.go' "$pkg" 2>/dev/null || true)
done

if [[ $fail -ne 0 ]]; then
  cat >&2 <<'MSG'

Determinism boundary violated.

The packages above must take time from the injected clock.Clock and randomness
from the injected *rand.Rand, so that a crawl is reproducible.

  time.Now()          -> e.clock.Now()      (or rm.clock, cb.clock, t.clock)
  time.Since(t)       -> e.clock.Since(t)
  time.NewTimer(d)    -> e.clock.NewTimer(d)
  time.NewTicker(d)   -> e.clock.NewTicker(d)
  rand.IntN(n)        -> rng.IntN(n), with rng threaded from engine.rand

Background: docs/design/0001-deterministic-crawl.md
MSG
  exit 1
fi

echo "determinism boundary: clean"
