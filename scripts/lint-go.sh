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

PYTHONPATH="${ROOT}/scripts${PYTHONPATH:+:${PYTHONPATH}}" python3 -m workflow_lint
python3 "${ROOT}/scripts/analysis_tools.py" ensure golangci-lint

cd "${GO_DIR}"
mode="${1:-fast}"
if [[ $# -gt 0 ]]; then
  shift
fi

PYTHONPATH="${ROOT}/scripts${PYTHONPATH:+:${PYTHONPATH}}" \
  python3 -m ci_policy.lint_scope "${mode}" "${GOLANGCI}" "$@"
