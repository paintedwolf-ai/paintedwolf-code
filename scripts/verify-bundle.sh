#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=artifact-paths.sh
source "$(dirname "$0")/artifact-paths.sh"
mkdir -p "${PW_BUILD_DIR}"
(cd "${ROOT}/lycaon" && go build -o "${PW_BUILD_DIR}/lycaon-debug" ./cmd/lycaon-debug)
VERIFY_HOME="$(mktemp -d "${TMPDIR:-/tmp}/pw-bundle-verify-home.XXXXXX")"
trap 'rm -rf "${VERIFY_HOME}"' EXIT
if [[ -n "${HOME:-}" && -d "${HOME}/Library" ]]; then
  ln -s "${HOME}/Library" "${VERIFY_HOME}/Library"
fi
# Exit codes: 0 clean, 1 findings, 2 usage.
HOME="${VERIFY_HOME}" USERPROFILE="${VERIFY_HOME}" "${PW_BUILD_DIR}/lycaon-debug" bundle-verify "$@"
