#!/usr/bin/env bash
# Identity checks for the sidecar launched into one E2E state directory.

e2e_sidecar_started_at() {
  ps -p "$1" -o lstart= 2>/dev/null | sed 's/^[[:space:]]*//; s/[[:space:]]*$//'
}

e2e_sidecar_matches() {
  local pid="$1" expected current
  [[ "$pid" =~ ^[0-9]+$ ]] && (( pid > 1 )) || return 1
  expected="$(cat "${STATE_DIR}/sidecar.started" 2>/dev/null || true)"
  current="$(e2e_sidecar_started_at "$pid" || true)"
  [[ -n "$expected" && "$expected" == "$current" ]]
}

e2e_stop_sidecar() {
  local first_signal="${1:-TERM}" pid
  [[ -f "${STATE_DIR}/sidecar.pid" ]] || return 0
  pid="$(cat "${STATE_DIR}/sidecar.pid")"
  [[ "$pid" =~ ^[0-9]+$ ]] && (( pid > 1 )) || return 1
  if kill -0 "$pid" 2>/dev/null; then
    if ! e2e_sidecar_matches "$pid"; then
      echo "error: sidecar pid ${pid} no longer matches its recorded identity; leaving it untouched" >&2
      return 1
    fi
    kill -"$first_signal" "$pid" 2>/dev/null || true
    for _ in $(seq 1 50); do
      e2e_sidecar_matches "$pid" || break
      sleep 0.1
    done
    if e2e_sidecar_matches "$pid"; then
      kill -KILL "$pid" 2>/dev/null || true
    fi
  fi
  rm -f "${STATE_DIR}/sidecar.pid" "${STATE_DIR}/sidecar.started"
}
