#!/usr/bin/env bash
# Clear boot restore state without app preferences.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/config-dir.sh
source "${ROOT}/scripts/config-dir.sh"

export LYCAON_DEV="${LYCAON_DEV:-1}"
CONFIG_DIR="${LYCAON_CONFIG_DIR:-$(lycaon_config_dir)}"
STATE_DIR="${CONFIG_DIR}/app-state-v1"
MARKER="${CONFIG_DIR}/.den-fresh-pending"

wipe_session_state() {
  if [[ ! -d "${STATE_DIR}" ]]; then
    echo "den fresh: no app-state at ${STATE_DIR}" >&2
    return 0
  fi
  local cleared=0 key encoded
  for key in \
    lastActiveProjectId \
    lastSessionSnapshot \
    cachedProjects \
    transcriptRowHeights \
    recents; do
    encoded="$(printf '%s' "${key}" | od -An -tx1 | tr -d ' \n')"
    if [[ -f "${STATE_DIR}/${encoded}.json" ]]; then
      rm -f "${STATE_DIR}/${encoded}.json"
      cleared=1
    fi
  done
  if [[ "${cleared}" -eq 1 ]]; then
    echo "den fresh: cleared boot restore slices from ${STATE_DIR}" >&2
  else
    echo "den fresh: session state already empty in ${STATE_DIR}" >&2
  fi
}

mark_fresh_pending() {
  mkdir -p "${CONFIG_DIR}"
  date -u +%Y-%m-%dT%H:%M:%SZ >"${MARKER}"
  echo "den fresh: marked pending — ./task den:dev will reset session state before launch" >&2
}

consume_fresh_pending() {
  if [[ ! -f "${MARKER}" ]]; then
    return 0
  fi
  echo "den fresh: applying pending session reset (sidecar:fresh was run)" >&2
  wipe_session_state
  rm -f "${MARKER}"
  echo "den fresh: quit any open Den window so the new shell loads from disk" >&2
}

usage() {
  cat >&2 <<EOF
usage: $(basename "$0") <wipe|mark|consume>

  wipe    Clear Den boot restore keys, recents, and transcript row heights
          in ${CONFIG_DIR}/app-state-v1/ (prefs kept).

  mark    Record that a sidecar fresh run happened; den:dev consumes on launch.

  consume If mark is pending, run wipe and clear the marker (den:dev entry).
EOF
  exit 2
}

cmd="${1:-}"
case "${cmd}" in
  wipe) wipe_session_state ;;
  mark) mark_fresh_pending ;;
  consume) consume_fresh_pending ;;
  *) usage ;;
esac
