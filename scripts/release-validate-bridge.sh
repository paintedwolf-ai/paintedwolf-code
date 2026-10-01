#!/usr/bin/env bash
# Check both public source-generation offers before preparing bridge publication.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
[[ $# -eq 1 && -f "$1" ]] || { echo "Usage: release-validate-bridge.sh MANIFEST" >&2; exit 2; }
FILE="$1"
GENERATION="$(jq -er .update_keys.signing_generation "${FILE}")"
[[ "$(jq -er .update_keys.embedded_generation "${FILE}")" != "${GENERATION}" ]] || exit 0
: "${DOWNLOAD_BASE_URL:?missing DOWNLOAD_BASE_URL}"
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/release-bridge.XXXXXX")"
trap 'rm -rf "${WORKDIR}"' EXIT
for channel in stable preview; do
  current="${WORKDIR}/${channel}.json"
  status="$(curl --silent --show-error --output "${current}" --write-out '%{http_code}' \
    -H 'Cache-Control: no-cache, no-store' \
    "${DOWNLOAD_BASE_URL%/}/updates/${channel}/key-${GENERATION}/latest.json")"
  case "${status}" in
    200)
      bash "${ROOT}/scripts/release-validate-updater-manifest.sh" --file "${current}"
      python3 - "${ROOT}/scripts" "${FILE}" "${current}" "${GENERATION}" <<'PY'
import json, sys
sys.path.insert(0, sys.argv[1])
from update_keys import load_registry, validate_binding, validate_bridge_advance
current = json.load(open(sys.argv[3]))
validate_binding(load_registry(), current, int(sys.argv[4]))
validate_bridge_advance(json.load(open(sys.argv[2])), current)
PY
      ;;
    404) ;;
    *) echo "error: cannot validate source-generation ${channel} feed: HTTP ${status}" >&2; exit 1 ;;
  esac
done
