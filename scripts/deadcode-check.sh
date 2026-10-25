#!/usr/bin/env bash
# Fail on unreachable functions in lycaon production packages reachable from main/tests.
# False positives (interface/sealed-type markers) live in lycaon/.deadcode-exclude.
# Diagnostics under test/ are ignored (interface stubs and test-only helpers).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=artifact-paths.sh
source "$(dirname "$0")/artifact-paths.sh"
BIN_DIR="${PW_BIN_DIR}"
DEADCODE="${BIN_DIR}/deadcode"
GO_DIR="${ROOT}/lycaon"
EXCLUDE_FILE="${GO_DIR}/.deadcode-exclude"
DEADCODE_VERSION="v0.33.0"
# Include command and test roots so their helpers remain reachable.
DEADCODE_PACKAGES=(./cmd/... ./internal/... ./pkg/... ./test/contract/... ./test/security/... ./test/wiring/... ./test/openapi/... ./test/integration/...)
export GOTOOLCHAIN="go$(grep '^go ' "${GO_DIR}/go.mod" | awk '{print $2}')"
export GOMEMLIMIT="${GOMEMLIMIT:-10GiB}"
CACHE_DIR="$(bash "${ROOT}/scripts/checkout-cache-dir.sh")"
PASS_CACHE="${CACHE_DIR}/deadcode-pass"
mkdir -p "${BIN_DIR}" "${CACHE_DIR}"

deadcode_key=""
if [[ -z "$(git -C "${ROOT}" status --porcelain --untracked-files=all -- lycaon scripts/deadcode-check.sh 2>/dev/null || true)" ]]; then
  tree_hash="$(git -C "${ROOT}" rev-parse HEAD:lycaon 2>/dev/null || true)"
  script_hash="$(git -C "${ROOT}" rev-parse HEAD:scripts/deadcode-check.sh 2>/dev/null || true)"
  if [[ -n "${tree_hash}" && -n "${script_hash}" ]]; then
    deadcode_key="${GOTOOLCHAIN}:${DEADCODE_VERSION}:${script_hash}:${tree_hash}"
    if [[ -f "${PASS_CACHE}" && "$(cat "${PASS_CACHE}")" == "${deadcode_key}" ]]; then
      exit 0
    fi
  fi
fi

python3 "${ROOT}/scripts/analysis_tools.py" ensure deadcode

cd "${GO_DIR}"

# Analyzer failures remain fatal; an empty findings filter is valid.
# Integration-tagged tests contribute to the call graph.
deadcode_out="$("${DEADCODE}" -test -tags integration \
  -filter='^github.com/lycaon/lycaon/(cmd|internal|pkg|test)' \
  "${DEADCODE_PACKAGES[@]}")"
current="$(printf '%s\n' "${deadcode_out}" | grep -E ': unreachable func:' | sort -u || true)"

exclude_patterns=()
if [[ -f "${EXCLUDE_FILE}" ]]; then
  while IFS= read -r pattern; do
    [[ -z "${pattern}" || "${pattern}" == \#* ]] && continue
    exclude_patterns+=(-e "${pattern}")
  done <"${EXCLUDE_FILE}"
fi

violations=()
while IFS= read -r line; do
  [[ -z "${line}" ]] && continue
  [[ "${line}" == test/* ]] && continue
  if ((${#exclude_patterns[@]} > 0)) && grep -Eq "${exclude_patterns[@]}" <<<"${line}"; then
    continue
  fi
  violations+=("${line}")
done <<<"${current}"

if ((${#violations[@]} > 0)); then
  printf '%s\n' "${violations[@]}" >&2
  echo "deadcode: unreachable functions (add to ${EXCLUDE_FILE} only for interface false positives)" >&2
  exit 1
fi

if [[ -n "${deadcode_key}" ]]; then
  printf '%s\n' "${deadcode_key}" >"${PASS_CACHE}"
fi
