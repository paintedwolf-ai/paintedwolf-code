#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if ! python3 "$ROOT/scripts/test-execution.py" holding; then
  exec python3 "$ROOT/scripts/test-execution.py" run --name "lint-go" -- bash "$0" "$@"
fi
if ! bash "$ROOT/scripts/repo-snapshot-lock.sh" holding; then
  exec bash "$ROOT/scripts/repo-snapshot-lock.sh" read -- "$0" "$@"
fi
# shellcheck source=artifact-paths.sh
source "$(dirname "$0")/artifact-paths.sh"
GOLANGCI_VERSION="v2.12.2"
BIN_DIR="${PW_BIN_DIR}"
GOLANGCI="${BIN_DIR}/golangci-lint"
GO_DIR="${ROOT}/lycaon"
export GOTOOLCHAIN="go$(grep '^go ' "${GO_DIR}/go.mod" | awk '{print $2}')"
# Checkout-specific caches preserve diagnostic paths outside the source tree.
CACHE_DIR="$(bash "${ROOT}/scripts/checkout-cache-dir.sh")"
export GOLANGCI_LINT_CACHE="${CACHE_DIR}/golangci-cache-${GOLANGCI_VERSION#v}"
export GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
mkdir -p "${BIN_DIR}" "${GOLANGCI_LINT_CACHE}" "${GOCACHE}"

python3 "${ROOT}/scripts/analysis_tools.py" ensure golangci-lint

cd "${GO_DIR}"
mode="${1:-fast}"
if [[ $# -gt 0 ]]; then
  shift
fi

# shellcheck source=test-host-capacity.sh
source "${ROOT}/scripts/test-host-capacity.sh"
LINT_CPUS="$(test_host_cpu_count)"
LINT_LOAD="$(test_host_load_one)"
LINT_TIMEOUT_SCALE="$(test_host_timeout_scale "${LINT_CPUS}" "${LINT_LOAD}")"
LINT_BASE_TIMEOUT="$(awk '$1 == "timeout:" { print $2; exit }' .golangci.yml)"
LINT_TIMEOUT="$(test_scale_go_duration "${LINT_BASE_TIMEOUT:?missing lint timeout}" "${LINT_TIMEOUT_SCALE}")"
LINT_FLAGS=(--timeout "${LINT_TIMEOUT}")
printf 'lint: host load %s on %s CPUs; timeout=%s\n' "${LINT_LOAD}" "${LINT_CPUS}" "${LINT_TIMEOUT}" >&2

go vet ./...

case "${mode}" in
  fast)
    "${GOLANGCI}" run --fast-only "${LINT_FLAGS[@]}" "$@" ./...
    ;;
  full)
    "${GOLANGCI}" run "${LINT_FLAGS[@]}" "$@" ./...
    bash "${ROOT}/scripts/deadcode-check.sh"
    ;;
  *)
    echo "usage: $0 [fast|full] [golangci-lint flags...]" >&2
    exit 2
    ;;
esac
