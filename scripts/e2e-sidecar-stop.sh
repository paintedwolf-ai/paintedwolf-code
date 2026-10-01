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

# The mock model fixture this state directory started, verified by command line.
FIXTURE_PID_FILE="${STATE_DIR}/model-fixture.pid"
if [[ -f "${FIXTURE_PID_FILE}" ]]; then
  fixture_pid="$(cat "${FIXTURE_PID_FILE}")"
  if ps -p "${fixture_pid}" -o command= 2>/dev/null | grep -q 'e2e/model-fixture.ts'; then
    kill "${fixture_pid}" 2>/dev/null || true
  fi
  rm -f "${FIXTURE_PID_FILE}" "${STATE_DIR}/model-fixture.port"
fi
