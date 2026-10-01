#!/usr/bin/env bash
# Stop engines that hold a dev-store lock, listen on LYCAON_ADDR, or match the
# bundled binary name. The held lock is the authority: a bundled or ephemeral
# bind has no fixed port.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/config-dir.sh
source "${ROOT}/scripts/config-dir.sh"
# shellcheck source=scripts/listen-port.sh
source "${ROOT}/scripts/listen-port.sh"

export LYCAON_DEV="${LYCAON_DEV:-1}"
export LYCAON_CONFIG_DIR="${LYCAON_CONFIG_DIR:-$(lycaon_config_dir)}"
export LYCAON_ADDR="${LYCAON_ADDR:-127.0.0.1:8787}"
_port="${LYCAON_ADDR##*:}"
# bundled externalBin name (sidecar.rs BUNDLED_SIDECAR_NAME)
BUNDLED_SIDECAR_NAME="pw"

if ! command -v lsof >/dev/null 2>&1; then
  echo "error: lsof required to find sidecar listener" >&2
  exit 1
fi

stopped=0
foreign_listener=0
foreign_lock_holder=0
stopped_pids=""

stop_engine_pid() {
  local pid="$1"
  local origin="$2"
  local comm
  kill -0 "${pid}" 2>/dev/null || return 0
  if ! is_engine_pid "${pid}"; then
    return 1
  fi
  comm="$(ps -p "${pid}" -o comm= 2>/dev/null | tr -d '[:space:]')"
  echo "stopping ${comm} (pid ${pid}) — ${origin}" >&2
  kill "${pid}" 2>/dev/null || true
  stopped=1
  stopped_pids="${stopped_pids} ${pid}"
}

# Store lock holders.
for lock_file in "${LYCAON_CONFIG_DIR}/locks/"*.engine.lock; do
  [[ -e "${lock_file}" ]] || continue
  lock_pids="$(lsof -t "${lock_file}" 2>/dev/null || true)"
  for pid in ${lock_pids}; do
    if ! stop_engine_pid "${pid}" "holds ${lock_file}"; then
      comm="$(ps -p "${pid}" -o comm= 2>/dev/null || true)"
      echo "leaving ${comm} (pid ${pid}) — not a Painted Wolf Code engine" >&2
      foreign_lock_holder=1
    fi
  done
done

# LYCAON_ADDR listener. Port 0 is ephemeral and has no conventional bind.
if [[ "${_port}" == "0" ]]; then
  echo "ephemeral LYCAON_ADDR has no conventional listener to sweep" >&2
else
  pids="$(lsof -ti "TCP:${_port}" -sTCP:LISTEN 2>/dev/null || true)"
  if [[ -z "${pids}" ]]; then
    echo "no listener on port ${_port}" >&2
  else
    for pid in ${pids}; do
      if ! stop_engine_pid "${pid}" "listens on :${_port}"; then
        comm="$(ps -p "${pid}" -o comm= 2>/dev/null || true)"
        echo "leaving ${comm} (pid ${pid}) — not a lycaon sidecar" >&2
        foreign_listener=1
      fi
    done
  fi
fi

# Bundled binary, by executable name (not command-line text).
bundled="$(pgrep -x "${BUNDLED_SIDECAR_NAME}" 2>/dev/null || true)"
for pid in ${bundled}; do
  stop_engine_pid "${pid}" "bundled ${BUNDLED_SIDECAR_NAME}" || true
done

# SIGTERM; wait past the engine drain budget so a following wipe sees a free store.
if [[ -n "${stopped_pids}" ]]; then
  echo "waiting for engines to drain${stopped_pids}" >&2
fi
for attempt in $(seq 1 90); do
  still_running=0
  for pid in ${stopped_pids}; do
    if kill -0 "${pid}" 2>/dev/null; then
      still_running=1
    fi
  done
  [[ "${still_running}" -eq 0 ]] && break
  sleep 0.5
done
if [[ "${still_running:-0}" -ne 0 ]]; then
  echo "engines still running after 45s — sending SIGKILL${stopped_pids}" >&2
  for pid in ${stopped_pids}; do
    kill -9 "${pid}" 2>/dev/null || true
  done
  still_running=0
  for attempt in $(seq 1 20); do
    still_running=0
    for pid in ${stopped_pids}; do
      if kill -0 "${pid}" 2>/dev/null; then
        still_running=1
      fi
    done
    [[ "${still_running}" -eq 0 ]] && break
    sleep 0.1
  done
  if [[ "${still_running}" -ne 0 ]]; then
    echo "one or more engines did not stop after SIGKILL" >&2
    exit 1
  fi
fi
if [[ "${foreign_lock_holder}" -eq 1 ]]; then
  echo "a dev-store engine lock is held by something that is not a Painted Wolf Code engine" >&2
  exit 1
fi
if [[ "${foreign_listener}" -eq 1 ]]; then
  echo "port ${_port} is held by something that is not a lycaon engine" >&2
  exit 1
fi

if [[ "${stopped}" -eq 0 ]]; then
  echo "no Painted Wolf Code engine running (dev store locks, port ${_port}, or bundled ${BUNDLED_SIDECAR_NAME})" >&2
fi
