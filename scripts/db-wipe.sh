#!/usr/bin/env bash
# Remove the development store and state keyed by its durable identities.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/config-dir.sh
source "${ROOT}/scripts/config-dir.sh"

export LYCAON_DEV="${LYCAON_DEV:-1}"
DB_PATH="${LYCAON_DB_PATH:-$(lycaon_config_dir)/store.db}"
DATA_DIR="$(dirname "${DB_PATH}")"

# Open-file holders are the authoritative wipe guard.
if ! command -v lsof >/dev/null 2>&1; then
  echo "error: lsof required to confirm no engine has ${DB_PATH} open" >&2
  exit 1
fi
holders="$(lsof -t -- "${DB_PATH}" 2>/dev/null || true)"
if [[ -n "${holders}" ]]; then
  echo "error: ${DB_PATH} is open by pid(s): ${holders//$'\n'/ }" >&2
  echo "       stop the engine first: ./task den:sidecar:stop" >&2
  exit 1
fi

for path in "${DB_PATH}" "${DB_PATH}-wal" "${DB_PATH}-shm" "${DB_PATH}-journal"; do
  if [[ -e "${path}" ]]; then
    rm -f "${path}"
    echo "removed ${path}" >&2
  fi
done

for name in web-index.db source-observations.db; do
  for suffix in "" -wal -shm -journal; do
    path="${DATA_DIR}/${name}${suffix}"
    if [[ -e "${path}" ]]; then
      rm -f -- "${path}"
      echo "removed ${path}" >&2
    fi
  done
done

for name in projects source-content drafts session-checkpoints worker-branches worker-seeds worker-baselines session-worktrees document-outbox scratch; do
  path="${DATA_DIR}/${name}"
  if [[ -e "${path}" ]]; then
    rm -rf -- "${path}"
    echo "removed ${path}" >&2
  fi
done

echo "store-coupled development state cleared under: ${DATA_DIR}" >&2
