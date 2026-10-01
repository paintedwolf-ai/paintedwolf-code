#!/usr/bin/env bash
# Restarting with the retained store exercises abrupt shutdown recovery.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPTS="$(cd "${DIR}/.." && pwd)"
ROOT="$(cd "${SCRIPTS}/.." && pwd)"

# shellcheck source=scripts/e2e/env.sh
source "${SCRIPTS}/e2e/env.sh"

STATE_DIR="${LYCAON_E2E_STATE_DIR:-}"
if [[ -z "${STATE_DIR}" ]]; then
  echo "error: LYCAON_E2E_STATE_DIR is required (run inside an active den:harness lease)" >&2
  exit 1
fi

PID_FILE="${STATE_DIR}/sidecar.pid"
LOG_FILE="${STATE_DIR}/sidecar.log"

if [[ ! -f "${PID_FILE}" ]]; then
  echo "error: no sidecar.pid in ${STATE_DIR}" >&2
  exit 1
fi

if [[ ! -x "${STATE_DIR}/runtime/sidecar" ]]; then
  echo "error: prepared sidecar is missing; leaving the running process untouched" >&2
  exit 1
fi

# shellcheck source=scripts/e2e/sidecar-process.sh
source "${SCRIPTS}/e2e/sidecar-process.sh"
echo "crash-restart: SIGKILL sidecar pid=$(cat "${PID_FILE}")" >&2
e2e_stop_sidecar KILL

# The lease and database survive the process restart.
export LYCAON_E2E_STATE_DIR="${STATE_DIR}"
bash "${SCRIPTS}/e2e-sidecar-bg.sh" --reuse-built

NEW_PID="$(cat "${PID_FILE}")"
echo "crash-restart: sidecar back pid=${NEW_PID} log=${LOG_FILE} state=${STATE_DIR}" >&2
echo "crash-restart: ok pid=${NEW_PID}"
