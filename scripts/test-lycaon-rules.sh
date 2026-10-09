#!/usr/bin/env bash
# Exercise the exact shipping selection against every supported language.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
source "${ROOT}/scripts/test-host-capacity.sh"
# shellcheck source=test-run-isolation.sh
source "${ROOT}/scripts/test-run-isolation.sh"
HOST_CPUS="$(test_host_cpu_count)"
HOST_LOAD="$(test_host_load_one)"
PW_TEST_TIMEOUT_SCALE="$(test_host_timeout_scale "${HOST_CPUS}" "${HOST_LOAD}")"
export PW_TEST_TIMEOUT_SCALE
printf 'opengrep-conformance: host load %s on %s CPUs; workers=1, timeout scale=%sx\n' \
  "${HOST_LOAD}" "${HOST_CPUS}" "${PW_TEST_TIMEOUT_SCALE}" >&2
export LANG="${LANG:-C.UTF-8}"
export PYTHONUTF8=1
OPENGREP="$(bash "${ROOT}/scripts/resolve-opengrep.sh")"
test_run_create_isolation "opengrep"
RUN_DIR="${TEST_RUN_DIR}"
trap 'test_run_remove_isolation "${RUN_DIR}"' EXIT
test_run_export_isolation "${RUN_DIR}"
cd "${ROOT}/lycaon"
go run ./cmd/opengrep-conformance -opengrep "$OPENGREP" "$@"
