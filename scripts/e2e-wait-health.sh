#!/usr/bin/env bash
# Wait until Lycaon /health responds (used by E2E sidecar bootstrap).
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/e2e/env.sh
source "${DIR}/e2e/env.sh"

URL="${1:-${LYCAON_E2E_HEALTH_URL}}"
TRIES="${LYCAON_E2E_HEALTH_TRIES:-90}"
SLEEP="${LYCAON_E2E_HEALTH_SLEEP:-1}"

for _ in $(seq 1 "${TRIES}"); do
  if curl -sf --max-time 2 "${URL}" >/dev/null 2>&1; then
    exit 0
  fi
  sleep "${SLEEP}"
done

echo "error: backend not healthy at ${URL} after ${TRIES} attempts" >&2
exit 1
