#!/usr/bin/env bash
# Report per-package statement coverage for internal/*.
#
# Usage:
#   scripts/coverage-packages.sh
#   COVERAGE_PKG_MIN=50 COVERAGE_PKG_ENFORCE=1 scripts/coverage-packages.sh
#   COVERAGE_PROFILE=/path/to/profile scripts/coverage-packages.sh   # reuse a run
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if ! python3 "$ROOT/scripts/test-execution.py" holding; then
  exec python3 "$ROOT/scripts/test-execution.py" run --name "check:coverage:packages" -- bash "$0" "$@"
fi
cd "${ROOT}/lycaon"

MIN="$(python3 "$ROOT/scripts/coverage_policy.py" "${COVERAGE_PKG_MIN-$(python3 "$ROOT/scripts/coverage_policy.py" get go.package)}")"
ENFORCE="${COVERAGE_PKG_ENFORCE:-0}"
[[ "$ENFORCE" == 0 || "$ENFORCE" == 1 ]] || { echo "COVERAGE_PKG_ENFORCE must be 0 or 1" >&2; exit 2; }
EXEMPT_FILE="${ROOT}/lycaon/coverage-exempt.txt"

profile="${COVERAGE_PROFILE:-}"
if [[ -z "${profile}" ]]; then
  profile="$(mktemp -t lycaon-coverage-pkg.XXXXXX)"
  trap 'rm -f "${profile}"' EXIT

  echo "Running go test -short -coverprofile on ./internal/..."
  bash "$ROOT/scripts/go-test-digest.sh" --name check:coverage:packages -- -coverprofile="${profile}" ./internal/...
fi
[[ -s "${profile}" ]] || { echo "coverage-packages: empty profile" >&2; exit 1; }

# Exemptions are import-path suffixes; comments and blanks are ignored.
declare -a exempt=()
if [[ -f "${EXEMPT_FILE}" ]]; then
  while IFS= read -r line; do
    line="${line%%#*}"
    line="$(printf '%s' "${line}" | tr -d '[:space:]')"
    [[ -n "${line}" ]] && exempt+=("${line}")
  done <"${EXEMPT_FILE}"
fi

is_exempt() {
  local pkg="$1" e
  for e in "${exempt[@]+"${exempt[@]}"}"; do
    [[ "${pkg}" == "${e}" || "${pkg}" == */"${e}" ]] && return 0
  done
  return 1
}

# Aggregate cover-profile statement ranges by package.
report="$(awk '
  NR == 1 { next }
  {
    split($1, location, ":")
    file = location[1]
    sub(/\/[^\/]+$/, "", file)
    statements = $2 + 0
    total[file] += statements
    if (($3 + 0) > 0) covered[file] += statements
  }
  END {
    for (pkg in total) {
      if (total[pkg] > 0) printf "%s %.17g\n", pkg, (covered[pkg] * 100.0) / total[pkg]
    }
  }
' "${profile}" | sort)"

[[ -n "${report}" ]] || { echo "coverage-packages: no packages in profile" >&2; exit 1; }

offenders=0
skipped=0
while read -r pkg pct; do
  [[ -n "${pkg}" ]] || continue
  if is_exempt "${pkg}"; then
    skipped=$((skipped + 1))
    continue
  fi
  if awk -v actual="$pct" -v minimum="$MIN" 'BEGIN {exit !(actual < minimum)}'; then
    printf '  %6.1f%%  %s\n' "${pct}" "${pkg}"
    offenders=$((offenders + 1))
  fi
done <<<"${report}"

total_pkgs="$(wc -l <<<"${report}" | tr -d ' ')"
if (( offenders == 0 )); then
  echo "coverage-packages: OK — ${total_pkgs} packages, none below ${MIN}% (${skipped} exempt)"
  exit 0
fi

echo "coverage-packages: ${offenders}/${total_pkgs} packages below ${MIN}% (${skipped} exempt)" >&2
if [[ "${ENFORCE}" == "1" ]]; then
  exit 1
fi
echo "Advisory only (COVERAGE_PKG_ENFORCE=1 to gate)." >&2
exit 0
