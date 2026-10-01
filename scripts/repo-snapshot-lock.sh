#!/usr/bin/env bash
# Writers publish changed bytes under an exclusive lock.
# Readers retry commands when the publication epoch changes.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=snapshot-publish.sh
source "$SCRIPT_DIR/snapshot-publish.sh"

WRITER_TIMEOUT="${PW_SNAPSHOT_WRITER_TIMEOUT:-900}"
READ_ATTEMPTS="${PW_SNAPSHOT_READ_ATTEMPTS:-3}"

usage() {
  echo "usage: $0 generate|read -- command [args...]" >&2
  echo "       $0 status" >&2
  echo "       $0 holding" >&2
  exit 2
}

warn() {
  echo "repo-snapshot: $*" >&2
}

file_age_seconds() {
  local mtime
  mtime="$(stat -f '%m' "$1" 2>/dev/null || stat -c '%Y' "$1" 2>/dev/null || true)"
  [[ "$mtime" =~ ^[0-9]+$ ]] || { printf '?\n'; return; }
  printf '%s\n' "$(($(date +%s) - mtime))"
}

token_live() {
  local token="${1:-}" pid
  case "$token" in
    w-*|r-*) ;;
    *) return 1 ;;
  esac
  pid="$(printf '%s\n' "$token" | cut -d- -f2)"
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  kill -0 "$pid" 2>/dev/null
}

writer_live() {
  [[ -f "$SNAPSHOT_WRITER_FILE" ]] || return 1
  local pid started
  pid="$(sed -n '1p' "$SNAPSHOT_WRITER_FILE" 2>/dev/null || true)"
  started="$(sed -n '3p' "$SNAPSHOT_WRITER_FILE" 2>/dev/null || true)"
  snapshot_pid_alive "$pid" "$started"
}

writer_describe() {
  local pid cmd
  pid="$(sed -n '1p' "$SNAPSHOT_WRITER_FILE" 2>/dev/null || true)"
  cmd="$(sed -n '4p' "$SNAPSHOT_WRITER_FILE" 2>/dev/null || true)"
  printf 'pid %s (held %ss): %s\n' "${pid:-?}" "$(file_age_seconds "$SNAPSHOT_WRITER_FILE")" "${cmd:-unknown command}"
}

reclaim_dead_holders() {
  if [[ -f "$SNAPSHOT_WRITER_FILE" ]] && ! writer_live; then
    rm -f "$SNAPSHOT_WRITER_FILE"
  fi
  local f pid started
  for f in "$SNAPSHOT_WAITERS_DIR"/*; do
    [[ -f "$f" ]] || continue
    pid="$(basename "$f")"
    started="$(sed -n '1p' "$f" 2>/dev/null || true)"
    if ! snapshot_pid_alive "$pid" "$started"; then
      rm -f "$f"
    fi
  done
}

# An abandoned publication window is closed before readers continue.
settled_epoch() {
  local v waited=0
  while true; do
    v="$(snapshot_epoch_read)"
    if ((v % 2 == 0)); then
      printf '%s\n' "$v"
      return
    fi
    if writer_live; then
      if ((waited > 0)) && ((waited % 300 == 0)); then
        warn "waiting for publish window to close: $(writer_describe)"
      fi
      sleep 0.1
      waited=$((waited + 1))
      continue
    fi
    snapshot_mutex_acquire
    v="$(snapshot_epoch_read)"
    if ((v % 2 == 1)) && ! writer_live; then
      snapshot_epoch_set "$((v + 1))"
      warn "publish window was left open by a writer that is gone; generated files may be torn — rerun the codegen task if checks fail"
    fi
    snapshot_mutex_release
  done
}

child_pid=""
watchdog_pid=""
stop_watchdog() {
  if [[ -n "$watchdog_pid" ]]; then
    kill -TERM -- "$watchdog_pid" 2>/dev/null || true
    wait "$watchdog_pid" 2>/dev/null || true
    watchdog_pid=""
  fi
}
stop_child() {
  local signal="$1" code="$2"
  if [[ -n "$child_pid" ]]; then
    kill "-$signal" -- "-$child_pid" 2>/dev/null || true
    for _ in {1..40}; do
      if ! kill -0 "$child_pid" 2>/dev/null; then
        break
      fi
      sleep 0.05
    done
    if kill -0 "$child_pid" 2>/dev/null; then
      kill -KILL -- "-$child_pid" 2>/dev/null || true
    fi
    wait "$child_pid" 2>/dev/null || true
    child_pid=""
  fi
  stop_watchdog
  exit "$code"
}

run_child() {
  set -m
  "$@" &
  child_pid=$!
  set +m
  set +e
  wait "$child_pid"
  child_rc=$?
  set -e
  child_pid=""
}

status_report() {
  local v
  v="$(snapshot_epoch_read)"
  if ((v % 2 == 0)); then
    echo "epoch: $v (settled)"
  else
    echo "epoch: $v (publish window open)"
  fi
  if [[ -f "$SNAPSHOT_WRITER_FILE" ]]; then
    if writer_live; then
      echo "writer: $(writer_describe)"
    else
      echo "writer: DEAD holder $(writer_describe)"
    fi
  else
    echo "writer: none"
  fi
  local f pid cmd n=0
  for f in "$SNAPSHOT_WAITERS_DIR"/*; do
    [[ -f "$f" ]] || continue
    n=$((n + 1))
    pid="$(basename "$f")"
    cmd="$(sed -n '2p' "$f" 2>/dev/null || true)"
    if [[ -z "$cmd" ]]; then
      cmd="$(ps -p "$pid" -o args= 2>/dev/null | sed 's/^[[:space:]]*//' || true)"
    fi
    echo "waiter: pid $pid (queued $(file_age_seconds "$f")s): ${cmd:-unknown command}"
  done
  if ((n == 0)); then
    echo "waiters: none"
  fi
  if ((v % 2 == 1)) && ! writer_live; then
    echo "anomaly: publish window open with no live writer — the next reader will force it closed"
  fi
}

run_generate() {
  local token waiter_file cmd_line timeout_flag waited=0
  token="w-$$-$(date +%s)-${RANDOM:-0}"
  waiter_file="$SNAPSHOT_WAITERS_DIR/$$"
  cmd_line="$*"

  mkdir -p "$SNAPSHOT_LOCK_DIR" "$SNAPSHOT_WAITERS_DIR"
  # shellcheck disable=SC2064 -- expand paths now; they are fixed for this run
  trap "stop_watchdog; rm -f '$waiter_file'; release_writer" EXIT
  trap 'stop_child INT 130' INT
  trap 'stop_child TERM 143' TERM
  trap 'stop_child HUP 129' HUP

  while true; do
    snapshot_mutex_acquire
    reclaim_dead_holders
    if printf '%s\n%s\n' "$(snapshot_process_started_at "$$")" "$cmd_line" >"$waiter_file"; then
      snapshot_mutex_release
      break
    fi
    snapshot_mutex_release
    sleep 0.05
  done

  while true; do
    snapshot_mutex_acquire
    reclaim_dead_holders
    if ! writer_live; then
      if printf '%s\n%s\n%s\n%s\n' "$$" "$token" "$(snapshot_process_started_at "$$")" "$cmd_line" >"$SNAPSHOT_WRITER_FILE"; then
        rm -f "$waiter_file"
        snapshot_mutex_release
        break
      fi
    fi
    snapshot_mutex_release
    if ((waited > 0)) && ((waited % 150 == 0)); then
      warn "waiting for writer: $(writer_describe)"
    fi
    sleep 0.1
    waited=$((waited + 1))
  done

  export PW_REPO_SNAPSHOT_TOKEN="$token"
  timeout_flag="$SNAPSHOT_LOCK_DIR/writer-timeout.$$"
  rm -f "$timeout_flag"

  set -m
  "$@" &
  child_pid=$!
  python3 "$SCRIPT_DIR/process-group-watchdog.py" \
    "$WRITER_TIMEOUT" "$child_pid" "$timeout_flag" "$cmd_line" &
  watchdog_pid=$!
  set +m
  set +e
  wait "$child_pid"
  child_rc=$?
  set -e
  child_pid=""
  stop_watchdog

  # The child can exit with a publication window still open.
  snapshot_publish_finish
  if [[ -f "$timeout_flag" ]]; then
    rm -f "$timeout_flag"
    exit 124
  fi
  exit "$child_rc"
}

release_writer() {
  snapshot_mutex_acquire
  if [[ -f "$SNAPSHOT_WRITER_FILE" ]] && [[ "$(sed -n '2p' "$SNAPSHOT_WRITER_FILE" 2>/dev/null || true)" == "${PW_REPO_SNAPSHOT_TOKEN:-}" ]]; then
    rm -f "$SNAPSHOT_WRITER_FILE"
  fi
  reclaim_dead_holders
  snapshot_mutex_release
}

run_read() {
  local attempt epoch_before epoch_after
  export PW_REPO_SNAPSHOT_TOKEN="r-$$-$(date +%s)-${RANDOM:-0}"
  trap 'stop_child INT 130' INT
  trap 'stop_child TERM 143' TERM
  trap 'stop_child HUP 129' HUP
  for ((attempt = 1; attempt <= READ_ATTEMPTS; attempt++)); do
    epoch_before="$(settled_epoch)"
    run_child "$@"
    epoch_after="$(settled_epoch)"
    if [[ "$epoch_before" == "$epoch_after" ]]; then
      exit "$child_rc"
    fi
    warn "generated files changed while this command ran (epoch $epoch_before -> $epoch_after); rerunning (attempt $attempt/$READ_ATTEMPTS): $*"
  done
  warn "generated files kept changing across $READ_ATTEMPTS attempts; giving up: $*"
  exit 1
}

mode="${1:-}"
[[ -n "$mode" ]] || usage
shift || true

case "$mode" in
  holding)
    if token_live "${PW_REPO_SNAPSHOT_TOKEN:-}"; then
      exit 0
    fi
    exit 1
    ;;
  status)
    status_report
    exit 0
    ;;
  generate|read)
    [[ "${1:-}" == "--" ]] || usage
    shift
    [[ $# -gt 0 ]] || usage
    ;;
  *)
    usage
    ;;
esac

# Nested writers reuse the enclosing writer lock.
# Enclosing epoch checks cover nested reads.
existing_token="${PW_REPO_SNAPSHOT_TOKEN:-}"
if token_live "$existing_token"; then
  if [[ "$mode" == "read" ]]; then
    exec "$@"
  fi
  if [[ "$mode" == "generate" && "$existing_token" == w-* ]]; then
    exec "$@"
  fi
fi

if [[ "$mode" == "generate" ]]; then
  run_generate "$@"
else
  run_read "$@"
fi
