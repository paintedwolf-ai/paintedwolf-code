#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if ! python3 "$ROOT/scripts/test-execution.py" holding; then
  exec python3 "$ROOT/scripts/test-execution.py" run --name "gocache:trim" -- bash "$0" "$@"
fi

unused_hours="${GOCACHE_TRIM_UNUSED_HOURS:-168}"
case "${unused_hours}" in
  '' | *[!0-9]*) echo "GOCACHE_TRIM_UNUSED_HOURS must be a nonnegative integer" >&2; exit 2 ;;
esac
unused_min=$((unused_hours * 60))

seen=""

consider() {
  local cache="$1"
  local kind="${2:-go}"
  if [[ -z "${cache}" || "${cache}" == "off" || ! -d "${cache}" ]]; then
    return 0
  fi
  cache="$(cd "${cache}" && pwd)"
  case "${seen}" in
    *"|${cache}|"*) return 0 ;;
  esac
  seen="${seen}|${cache}|"
  if [[ "${kind}" == "go" ]]; then
    python3 "${ROOT}/scripts/verification_cache.py" "${cache}" --unused-hours "${unused_hours}"
    return
  fi
  find "${cache}" -type f -mmin "+${unused_min}" -delete 2>/dev/null
  find "${cache}" -mindepth 1 -type d -empty -delete 2>/dev/null
}

consider "$(go env GOCACHE 2>/dev/null || true)"
# An explicit cache override does not change the default cache location.
consider "$(env -u GOCACHE go env GOCACHE 2>/dev/null || true)"
CHECKOUT_CACHE="$(bash "${ROOT}/scripts/checkout-cache-dir.sh" 2>/dev/null || true)"
if [[ -n "${CHECKOUT_CACHE}" ]]; then
  for d in "${CHECKOUT_CACHE}"/golangci-cache-*; do
    consider "${d}" diagnostic
  done
fi

echo "gocache-trim: applied Go cache budget and >${unused_hours}h age cleanup" >&2
exit 0
