#!/usr/bin/env bash
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/e2e/env.sh
source "${DIR}/e2e/env.sh"

STATE_DIR="${LYCAON_E2E_STATE_DIR:-}"
[[ -n "${STATE_DIR}" ]] || exit 0

# shellcheck source=scripts/e2e/sidecar-process.sh
source "${DIR}/e2e/sidecar-process.sh"
e2e_stop_sidecar
