#!/usr/bin/env bash
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "${DIR}/.." && pwd)"
# shellcheck source=scripts/e2e/env.sh
source "${DIR}/e2e/env.sh"

STATE_DIR="${LYCAON_E2E_STATE_DIR:-}"
if [[ -z "${STATE_DIR}" ]]; then
  STATE_DIR="$(mktemp -d -t lycaon-e2e-state.XXXXXX)"
  export LYCAON_E2E_STATE_DIR="${STATE_DIR}"
fi
mkdir -p "${STATE_DIR}"
# shellcheck source=scripts/e2e/sidecar-process.sh
source "${DIR}/e2e/sidecar-process.sh"

PID_FILE="${STATE_DIR}/sidecar.pid"
LOG_FILE="${STATE_DIR}/sidecar.log"

# The caller may remove state as soon as startup fails.
wait_health_or_dump_log() {
  if ! bash "${DIR}/e2e-wait-health.sh"; then
    if [[ -f "${LOG_FILE}" ]]; then
      echo "e2e-sidecar-bg: sidecar never became healthy — last 50 log lines:" >&2
      tail -n 50 "${LOG_FILE}" >&2 || true
    fi
    exit 1
  fi
}

if [[ -f "${PID_FILE}" ]]; then
  old_pid="$(cat "${PID_FILE}")"
  if kill -0 "${old_pid}" 2>/dev/null; then
    e2e_sidecar_matches "$old_pid" || {
      echo "error: existing sidecar pid does not match its recorded identity" >&2
      exit 1
    }
    wait_health_or_dump_log
    exit 0
  fi
fi

# An occupied port could make the health probe validate another sidecar.
SIDECAR_PORT="${LYCAON_E2E_ADDR##*:}"
if command -v lsof >/dev/null 2>&1; then
  foreign_pids="$(lsof -ti "TCP:${SIDECAR_PORT}" -sTCP:LISTEN 2>/dev/null || true)"
  if [[ -n "${foreign_pids}" ]]; then
    echo "error: sidecar port ${SIDECAR_PORT} already in use (pid ${foreign_pids//$'\n'/ })." >&2
    echo "       Stop the other process, or set LYCAON_E2E_ADDR." >&2
    exit 1
  fi
fi

# Compilation time is separate from the server startup deadline.
case "${1:-}" in
  "") bash "${DIR}/e2e-sidecar-build.sh" ;;
  --reuse-built)
    [[ -x "${STATE_DIR}/runtime/sidecar" ]] || {
      echo "error: prepared sidecar is missing from ${STATE_DIR}/runtime" >&2
      exit 1
    }
    ;;
  *) echo "error: unknown sidecar startup option: $1" >&2; exit 1 ;;
esac

# Another stack may claim the port during compilation.
if command -v lsof >/dev/null 2>&1; then
  foreign_pids="$(lsof -ti "TCP:${SIDECAR_PORT}" -sTCP:LISTEN 2>/dev/null || true)"
  if [[ -n "${foreign_pids}" ]]; then
    echo "error: sidecar port ${SIDECAR_PORT} already in use (pid ${foreign_pids//$'\n'/ })." >&2
    echo "       Stop the other process, or set LYCAON_E2E_ADDR." >&2
    exit 1
  fi
fi

nohup bash "${DIR}/e2e-sidecar-serve.sh" >"${LOG_FILE}" 2>&1 &
SIDECAR_PID=$!
e2e_sidecar_started_at "$SIDECAR_PID" >"${STATE_DIR}/sidecar.started"
echo "$SIDECAR_PID" >"${PID_FILE}"

wait_health_or_dump_log
echo "e2e-sidecar-bg: pid=$(cat "${PID_FILE}") log=${LOG_FILE}" >&2
