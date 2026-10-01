#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# shellcheck source=artifact-paths.sh
source "$(dirname "$0")/artifact-paths.sh"
SPEC="docs/openapi.yaml"
BASELINE="docs/openapi/baseline-bundle.yaml"
BIN_DIR="${PW_BIN_DIR}"
OASDIFF="${BIN_DIR}/oasdiff"
OASDIFF_VERSION="v1.26.1"

if [[ ! -f "${BASELINE}" ]]; then
  echo "error: ${BASELINE} is missing — restore the committed baseline or establish one as described in docs/dev-tasks.md#openapi-release-review" >&2
  exit 1
fi
if [[ ! -f "${SPEC}" ]]; then
  echo "error: ${SPEC} is missing — run ./task openapi:bundle" >&2
  exit 1
fi

export GOTOOLCHAIN="go$(grep '^go ' "${ROOT}/lycaon/go.mod" | awk '{print $2}')"
OASDIFF_STAMP="${BIN_DIR}/oasdiff.version"
if [[ ! -x "${OASDIFF}" || "$(cat "${OASDIFF_STAMP}" 2>/dev/null)" != "${OASDIFF_VERSION}" ]]; then
  echo "Installing oasdiff ${OASDIFF_VERSION} to ${BIN_DIR} (toolchain ${GOTOOLCHAIN})..." >&2
  mkdir -p "${BIN_DIR}"
  (cd "${ROOT}/lycaon" && GOBIN="${BIN_DIR}" go install "github.com/oasdiff/oasdiff@${OASDIFF_VERSION}")
  printf '%s\n' "${OASDIFF_VERSION}" >"${OASDIFF_STAMP}"
fi

echo "OpenAPI release review (error-level changes): ${BASELINE}..${SPEC}"
# Findings are informational; tool failures retain their exit status.
exec "${OASDIFF}" changelog --level ERR "${BASELINE}" "${SPEC}"
