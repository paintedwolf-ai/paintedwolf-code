#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/e2e/env.sh
source "${DIR}/e2e/env.sh"

export VITE_LYCAON_API_URL="${VITE_LYCAON_API_URL:-${LYCAON_E2E_API_URL}}"
export VITE_LYCAON_API_TOKEN="${VITE_LYCAON_API_TOKEN:-${LYCAON_E2E_TOKEN}}"

echo "e2e-vite: ${LYCAON_E2E_BASE_URL} → ${VITE_LYCAON_API_URL}" >&2

cd "${ROOT}/lycaon-den"
exec bun run vite --host "${LYCAON_E2E_VITE_HOST}" --port "${LYCAON_E2E_VITE_PORT}" --strictPort
