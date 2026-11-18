#!/usr/bin/env bash
# Exercise the exact shipping selection against every supported language.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=test-run-isolation.sh
source "${ROOT}/scripts/test-run-isolation.sh"
export LANG="${LANG:-C.UTF-8}"
export PYTHONUTF8=1
OPENGREP="$(bash "${ROOT}/scripts/resolve-opengrep.sh")"
test_run_create_isolation "opengrep"
RUN_DIR="${TEST_RUN_DIR}"
trap 'test_run_remove_isolation "${RUN_DIR}"' EXIT
test_run_export_isolation "${RUN_DIR}"
cd "${ROOT}/lycaon"
go run ./cmd/opengrep-conformance -opengrep "$OPENGREP" "$@"
