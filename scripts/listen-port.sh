#!/usr/bin/env bash
# Sourced. Listen-port helpers for local Den / e2e start.
set -euo pipefail

_LISTEN_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
_LISTEN_DEN="${_LISTEN_ROOT}/lycaon-den"

_listen_pid_comm() {
  ps -p "$1" -o comm= 2>/dev/null | tr -d '[:space:]'
}

_listen_pid_base() {
  local comm
  comm="$(_listen_pid_comm "$1")"
  # macOS comm is often a path.
  printf '%s\n' "${comm##*/}"
}

_listen_pid_cwd() {
  lsof -a -p "$1" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p' | head -n1
}

is_engine_pid() {
  local base
  base="$(_listen_pid_base "$1")"
  case "${base}" in
    lycaon-dev|lycaon|pw) return 0 ;;
    *) return 1 ;;
  esac
}

# Development-server arguments can omit the checkout path.
is_den_vite_pid() {
  local pid="$1" base args cwd
  base="$(_listen_pid_base "${pid}")"
  case "${base}" in
    bun|node|vite) ;;
    *) return 1 ;;
  esac
  args="$(ps -p "${pid}" -ww -o args= 2>/dev/null || true)"
  if [[ "${args}" == *"${_LISTEN_DEN}"* ]]; then
    return 0
  fi
  cwd="$(_listen_pid_cwd "${pid}")"
  [[ -n "${cwd}" && "${cwd}" == "${_LISTEN_DEN}"* ]]
}

_listen_pids() {
  lsof -ti "TCP:$1" -sTCP:LISTEN 2>/dev/null || true
}

_wait_listen_port_free() {
  local port="$1" label="$2"
  local i
  for i in 1 2 3 4 5 6 7 8 9 10; do
    [[ -z "$(_listen_pids "${port}")" ]] && return 0
    sleep 0.2
  done
  echo "error: port ${port} still busy after stopping ${label} listener" >&2
  exit 1
}

_engine_listen_addr_is_live() {
  local addr="$1"
  local url pid i
  url="http://${addr}/health"
  command -v curl >/dev/null 2>&1 || return 1
  # curl --max-time can stall on a mute listen socket.
  curl -sf --connect-timeout 1 --max-time 1 "${url}" >/dev/null 2>&1 &
  pid=$!
  for i in 1 2 3 4 5 6 7 8 9 10; do
    if ! kill -0 "${pid}" 2>/dev/null; then
      wait "${pid}"
      return $?
    fi
    sleep 0.1
  done
  kill -9 "${pid}" 2>/dev/null || true
  wait "${pid}" 2>/dev/null || true
  return 1
}

# ensure_engine_listen_port ADDR is_ours_fn label [foreign_hint]
# GET /health 2xx refuses; only an unresponsive ours is SIGTERM'd.
ensure_engine_listen_port() {
  local addr="$1"
  local is_ours="$2"
  local label="$3"
  local foreign_hint="${4:-set LYCAON_ADDR}"
  local port="${addr##*:}"
  local pid comm foreign=0 live=0 dead_ours="" pids
  if ! command -v lsof >/dev/null 2>&1; then
    return 0
  fi
  pids="$(_listen_pids "${port}")"
  [[ -z "${pids}" ]] && return 0
  for pid in ${pids}; do
    if "${is_ours}" "${pid}"; then
      if ! command -v curl >/dev/null 2>&1; then
        echo "error: curl required to check whether port ${port} holds a live engine — ${foreign_hint}" >&2
        live=1
        continue
      fi
      # /health can lag the listen socket.
      if _engine_listen_addr_is_live "${addr}" || { sleep 0.2; _engine_listen_addr_is_live "${addr}"; }; then
        comm="$(_listen_pid_comm "${pid}")"
        echo "error: port ${port} is held by a live engine (${comm} pid ${pid}) — run ./task den:sidecar:stop to replace it" >&2
        live=1
      else
        dead_ours="${dead_ours} ${pid}"
      fi
    else
      foreign=1
      comm="$(ps -p "${pid}" -o comm= 2>/dev/null || true)"
      echo "error: port ${port} in use by ${comm} (pid ${pid}) — ${foreign_hint}" >&2
    fi
  done
  if [[ "${live}" -eq 1 || "${foreign}" -eq 1 ]]; then
    exit 1
  fi
  for pid in ${dead_ours}; do
    comm="$(_listen_pid_comm "${pid}")"
    echo "${label} — stopping unresponsive ${comm} on :${port} (pid ${pid})" >&2
    kill "${pid}" 2>/dev/null || true
  done
  _wait_listen_port_free "${port}" "${label}"
}

# reclaim_listen_port PORT is_ours_fn label [foreign_hint]
reclaim_listen_port() {
  local port="$1"
  local is_ours="$2"
  local label="$3"
  local foreign_hint="${4:-free the port}"
  local pid comm foreign=0 pids
  if ! command -v lsof >/dev/null 2>&1; then
    return 0
  fi
  pids="$(_listen_pids "${port}")"
  [[ -z "${pids}" ]] && return 0
  for pid in ${pids}; do
    if "${is_ours}" "${pid}"; then
      comm="$(_listen_pid_comm "${pid}")"
      echo "${label} — stopping ${comm} on :${port} (pid ${pid})" >&2
      kill "${pid}" 2>/dev/null || true
    else
      foreign=1
      comm="$(ps -p "${pid}" -o comm= 2>/dev/null || true)"
      echo "error: port ${port} in use by ${comm} (pid ${pid}) — ${foreign_hint}" >&2
    fi
  done
  if [[ "${foreign}" -eq 1 ]]; then
    exit 1
  fi
  _wait_listen_port_free "${port}" "${label}"
}
