#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=artifact-paths.sh
source "$(dirname "$0")/artifact-paths.sh"
mkdir -p "${PW_BUILD_DIR}"
(cd "${ROOT}/lycaon" && go build -o "${PW_BUILD_DIR}/lycaon-debug" ./cmd/lycaon-debug)
# Exit codes: 0 clean, 1 findings, 2 usage.
exec "${PW_BUILD_DIR}/lycaon-debug" bundle-verify "$@"
