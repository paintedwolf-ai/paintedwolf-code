#!/usr/bin/env bash
# Stage the maintained artifact; verify packaged bytes through the built sidecar.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MODE=stage
if [[ "${1:-}" == --verify ]]; then MODE=verify; shift; fi
ENGINE_ROOT="${1:?engine root directory}"
TARGET="${2:?rust host target triple}"
case "${ENGINE_ROOT}" in
  /*|[A-Za-z]:/*|[A-Za-z]:\\*) ;;
  *) ENGINE_ROOT="${PWD}/${ENGINE_ROOT}" ;;
esac
if [[ "${MODE}" == verify ]]; then
  SIDECAR="${3:?built sidecar required for verification}"
  exec "${SIDECAR}" scan engines verify-bundled --root "${ENGINE_ROOT}"
fi
ARTIFACT_DIR="${3:-}"
if [[ -z "${ARTIFACT_DIR}" ]]; then
  ARTIFACT_DIR="$(bash "${ROOT}/scripts/resolve-opengrep.sh" --artifact-dir-only --target "${TARGET}")"
fi
cd "${ROOT}/lycaon"
exec env -u LYCAON_OPENGREP_CANDIDATE go run ./cmd/opengrep-artifact -mode stage -artifact-directory "${ARTIFACT_DIR}" -root "${ENGINE_ROOT}" -target "${TARGET}"
