#!/usr/bin/env bash
# Stage the Tauri shell's external binaries when any is missing for the host target.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BINARIES_DIR="${ROOT}/lycaon-den/src-tauri/binaries"
TARGET="$(rustc --print host-tuple)"

for name in pw pw-logs pw-document-core bialy; do
  if [[ ! -x "${BINARIES_DIR}/${name}-${TARGET}" ]]; then
    echo "staging missing Tauri external binaries (pw / pw-logs / pw-document-core / bialy) for ${TARGET}" >&2
    exec bash "${ROOT}/scripts/stage-engine.sh" --minimal
  fi
done
