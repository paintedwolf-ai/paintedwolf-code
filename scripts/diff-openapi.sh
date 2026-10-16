#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# shellcheck source=artifact-paths.sh
source "$(dirname "$0")/artifact-paths.sh"
SPEC="docs/openapi.yaml"
BASELINE="docs/openapi/baseline-bundle.yaml"
BASELINE_REF=""
REPORT_DIR="${PW_ARTIFACT_ROOT}/ci/compatibility"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --source-root) [[ $# -ge 2 && -d "$2" ]] || exit 2; ROOT="$2"; cd "$ROOT"; shift 2 ;;
    --baseline-ref) [[ $# -ge 2 && -n "$2" ]] || exit 2; BASELINE_REF="$2"; shift 2 ;;
    *) echo "error: unknown OpenAPI review argument: $1" >&2; exit 2 ;;
  esac
done
if [[ -n "${BASELINE_REF}" ]]; then
  mkdir -p "${REPORT_DIR}"
  BASELINE="${REPORT_DIR}/openapi-baseline.yaml"
  git show "${BASELINE_REF}:docs/openapi.yaml" >"${BASELINE}"
fi
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

mkdir -p "${REPORT_DIR}"
python3 - "${BASELINE}" "${SPEC}" "${REPORT_DIR}/openapi-inputs.json" "${BASELINE_REF}" <<'PYREPORT'
import hashlib
import json
import subprocess
import sys
from pathlib import Path
paths = sys.argv[1:3]
report = {"baseline_ref": sys.argv[4], "source_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip(),
          "inputs": {path: hashlib.sha256(Path(path).read_bytes()).hexdigest() for path in paths}}
Path(sys.argv[3]).write_text(json.dumps(report, indent=2) + "\n")
PYREPORT
echo "OpenAPI release review (error-level changes): ${BASELINE}..${SPEC}"
# Findings are informational; tool failures retain their exit status.
"${OASDIFF}" changelog --level ERR "${BASELINE}" "${SPEC}" | tee "${REPORT_DIR}/openapi-changelog.txt"
