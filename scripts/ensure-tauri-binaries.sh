#!/usr/bin/env bash
# Stage the Tauri shell's external binaries when any is missing for the host target.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BINARIES_DIR="${ROOT}/lycaon-den/src-tauri/binaries"
TARGET="$(rustc --print host-tuple)"

# Native tests compile the same resource manifest as packaged builds.
if [[ ! -f "${ROOT}/lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md" ]]; then
  if [[ ! -f "${ROOT}/THIRD-PARTY-NOTICES.md" ]]; then
    echo "error: native resources are missing; run ./task licenses:notices after ./task setup-dev -- --frontend" >&2
    exit 1
  fi
  cp "${ROOT}/THIRD-PARTY-NOTICES.md" "${ROOT}/lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md"
fi

for name in pw pw-logs pw-document-core bialy; do
  if [[ ! -x "${BINARIES_DIR}/${name}-${TARGET}" ]]; then
    echo "staging missing Tauri external binaries (pw / pw-logs / pw-document-core / bialy) for ${TARGET}" >&2
    exec bash "${ROOT}/scripts/stage-engine.sh" --minimal
  fi
done
