#!/usr/bin/env bash
# Filesystem mutex with stale-holder recovery.

digest_lock_holder_alive() {
  local lockdir="$1"
  local pidfile="${lockdir}/pid"
  local pid=""
  [[ -f "${pidfile}" ]] || return 1
  pid="$(cat "${pidfile}" 2>/dev/null || true)"
  [[ -n "${pid}" ]] || return 1
  kill -0 "${pid}" 2>/dev/null
}

# GNU `stat -f` succeeds with filesystem text, so the GNU form is probed first;
# BSD `stat` rejects `-c`.
digest_lock_mtime() {
  local mtime
  mtime="$(stat -c %Y "$1" 2>/dev/null)" || mtime="$(stat -f %m "$1" 2>/dev/null)" || return 1
  [[ "${mtime}" =~ ^[0-9]+$ ]] || return 1
  printf '%s\n' "${mtime}"
}

digest_try_reclaim_stale() {
  local lockdir="$1"
  [[ -d "${lockdir}" ]] || return 1
  if digest_lock_holder_alive "${lockdir}"; then
    return 1
  fi
  # A new lock may not have published its owner yet.
  if [[ ! -f "${lockdir}/pid" ]]; then
    local age=0 mtime
    if mtime="$(digest_lock_mtime "${lockdir}")"; then
      age=$(( $(date +%s) - mtime ))
    fi
    if (( age < 5 )); then
      return 1
    fi
  fi
  echo "digest lock: reclaiming stale ${lockdir}" >&2
  rm -rf "${lockdir}"
  return 0
}

digest_acquire_lock() {
  local lockdir="$1"
  local timeout="${2:-${DIGEST_LOCK_TIMEOUT:-60}}"
  local waited=0
  while ! mkdir "${lockdir}" 2>/dev/null; do
    digest_try_reclaim_stale "${lockdir}" || true
    if mkdir "${lockdir}" 2>/dev/null; then
      break
    fi
    sleep 1
    waited=$((waited + 1))
    if (( waited >= timeout )); then
      echo "digest lock timeout: ${lockdir}" >&2
      if digest_lock_holder_alive "${lockdir}"; then
        echo "digest lock held by pid $(cat "${lockdir}/pid" 2>/dev/null || echo '?')" >&2
      fi
      return 1
    fi
  done
  echo $$ >"${lockdir}/pid"
  export DIGEST_LOCK_DIR="${lockdir}"
}

digest_release_lock() {
  if [[ -n "${DIGEST_LOCK_DIR:-}" ]]; then
    rm -f "${DIGEST_LOCK_DIR}/pid" 2>/dev/null || true
    rmdir "${DIGEST_LOCK_DIR}" 2>/dev/null || rm -rf "${DIGEST_LOCK_DIR}" 2>/dev/null || true
    unset DIGEST_LOCK_DIR
  fi
}
