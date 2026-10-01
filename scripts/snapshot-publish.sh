#!/usr/bin/env bash
# Even epochs are settled; odd epochs mark publication of changed bytes.

SNAPSHOT_SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SNAPSHOT_LOCK_DIR="${PW_LOCK_ROOT:-$(python3 "$SNAPSHOT_SCRIPTS_DIR/artifact_paths.py" locks "$SNAPSHOT_SCRIPTS_DIR/..")}/repo-snapshot.lockdir"
SNAPSHOT_MUTEX_DIR="$SNAPSHOT_LOCK_DIR/mutex"
SNAPSHOT_WRITER_FILE="$SNAPSHOT_LOCK_DIR/writer"
SNAPSHOT_WAITERS_DIR="$SNAPSHOT_LOCK_DIR/writer-waiters"
SNAPSHOT_EPOCH_FILE="$SNAPSHOT_LOCK_DIR/epoch"

snapshot_process_started_at() {
  ps -p "$1" -o lstart= 2>/dev/null | sed 's/^[[:space:]]*//'
}

snapshot_pid_alive() {
  local pid="$1" started="$2"
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  local current
  current="$(snapshot_process_started_at "$pid")"
  [[ -z "$started" || "$started" == "$current" ]]
}

snapshot_mutex_is_initializing() {
  local modified now
  [[ -d "$SNAPSHOT_MUTEX_DIR" ]] || return 1
  modified="$(stat -f '%m' "$SNAPSHOT_MUTEX_DIR" 2>/dev/null || stat -c '%Y' "$SNAPSHOT_MUTEX_DIR" 2>/dev/null || true)"
  [[ "$modified" =~ ^[0-9]+$ ]] || return 1
  now="$(date +%s)"
  ((now - modified < 30))
}

SNAPSHOT_MUTEX_CLAIM=""

snapshot_mutex_acquire() {
  local stale_checks=0
  while true; do
    mkdir -p "$SNAPSHOT_LOCK_DIR" "$SNAPSHOT_WAITERS_DIR"
    if mkdir "$SNAPSHOT_MUTEX_DIR" 2>/dev/null; then
      local claim
      claim="$$-$(date +%s)-${RANDOM:-0}-${RANDOM:-0}"
      if printf '%s\n' "$claim" >"$SNAPSHOT_MUTEX_DIR/claim" &&
        printf '%s\n' "$$" >"$SNAPSHOT_MUTEX_DIR/pid" &&
        printf '%s\n' "$(snapshot_process_started_at "$$")" >"$SNAPSHOT_MUTEX_DIR/started"; then
        SNAPSHOT_MUTEX_CLAIM="$claim"
        return
      fi
      if [[ "$(cat "$SNAPSHOT_MUTEX_DIR/claim" 2>/dev/null || true)" == "$claim" ]]; then
        rm -rf "$SNAPSHOT_MUTEX_DIR"
      fi
      sleep 0.05
      continue
    fi
    local holder_pid holder_started
    holder_pid="$(cat "$SNAPSHOT_MUTEX_DIR/pid" 2>/dev/null || true)"
    holder_started="$(cat "$SNAPSHOT_MUTEX_DIR/started" 2>/dev/null || true)"
    if [[ "$holder_pid" == "$$" ]]; then
      return
    fi
    if snapshot_pid_alive "$holder_pid" "$holder_started"; then
      stale_checks=0
      sleep 0.05
      continue
    fi
    # An empty mutex may still be initializing.
    if [[ -z "$holder_pid" ]] && snapshot_mutex_is_initializing; then
      stale_checks=0
      sleep 0.05
      continue
    fi
    stale_checks=$((stale_checks + 1))
    if ((stale_checks < 10)); then
      sleep 0.05
      continue
    fi
    rm -rf "$SNAPSHOT_MUTEX_DIR"
    stale_checks=0
  done
}

snapshot_mutex_release() {
  if [[ -n "$SNAPSHOT_MUTEX_CLAIM" ]] && [[ "$(cat "$SNAPSHOT_MUTEX_DIR/claim" 2>/dev/null || true)" == "$SNAPSHOT_MUTEX_CLAIM" ]]; then
    rm -rf "$SNAPSHOT_MUTEX_DIR"
  fi
  SNAPSHOT_MUTEX_CLAIM=""
}

snapshot_epoch_read() {
  local v
  v="$(cat "$SNAPSHOT_EPOCH_FILE" 2>/dev/null || true)"
  if [[ "$v" =~ ^[0-9]+$ ]]; then
    printf '%s\n' "$v"
  else
    printf '0\n'
  fi
}

# Atomic epoch write; callers hold the snapshot mutex.
snapshot_epoch_set() {
  local value="$1" tmp
  mkdir -p "$SNAPSHOT_LOCK_DIR"
  tmp="$SNAPSHOT_EPOCH_FILE.tmp.$$"
  printf '%s\n' "$value" >"$tmp"
  mv -f "$tmp" "$SNAPSHOT_EPOCH_FILE"
}

# The first changed file opens publication.
snapshot_publish_open() {
  local v
  snapshot_mutex_acquire
  v="$(snapshot_epoch_read)"
  if ((v % 2 == 0)); then
    snapshot_epoch_set "$((v + 1))"
  fi
  snapshot_mutex_release
}

# An unchanged run leaves the epoch intact.
snapshot_publish_finish() {
  local v
  snapshot_mutex_acquire
  v="$(snapshot_epoch_read)"
  if ((v % 2 == 1)); then
    snapshot_epoch_set "$((v + 1))"
  fi
  snapshot_mutex_release
}

# Atomic replacement requires the staged file to share the destination filesystem.
snapshot_publish_file() {
  local staged="$1" dest="$2"
  if [[ ! -f "$staged" ]]; then
    echo "snapshot_publish_file: staged file missing: $staged" >&2
    return 1
  fi
  if [[ -f "$dest" ]] && cmp -s "$staged" "$dest"; then
    rm -f "$staged"
    return 0
  fi
  snapshot_publish_open
  mkdir -p "$(dirname "$dest")"
  mv -f "$staged" "$dest"
}

snapshot_remove_file() {
  local dest="$1"
  [[ -e "$dest" ]] || return 0
  snapshot_publish_open
  rm -f "$dest"
}
