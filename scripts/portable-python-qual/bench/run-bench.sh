#!/usr/bin/env bash
set -euo pipefail

# PY06 Idle-Host Cross-Platform Benchmark Runner
# Builds the Go test binary for lycaon/internal/portablepython, optionally signs
# it with macOS hardened runtime, records host load/toolchain/revision, runs
# TestPY06NativePerfWorkbookMatrix, and emits raw logs, JSON receipts, summary
# Markdown, and SHA256SUMS.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

CONFIG_LABEL=""
IMAGE_VARIANT="original"
BACKEND=""
IMAGE_DIR=""
ENGINE_ARTIFACT=""
OUT_DIR=""
SIGN_HARDENED=0
COLD_RUNS=5
WARM_RUNS=5
KEEP_IMAGE_OVERRIDE=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --config-label) CONFIG_LABEL="$2"; shift 2 ;;
    --image-variant) IMAGE_VARIANT="$2"; shift 2 ;;
    --backend) BACKEND="$2"; shift 2 ;;
    --image-dir) IMAGE_DIR="$2"; shift 2 ;;
    --engine-artifact) ENGINE_ARTIFACT="$2"; shift 2 ;;
    --out-dir) OUT_DIR="$2"; shift 2 ;;
    --sign-hardened) SIGN_HARDENED=1; shift ;;
    --cold-runs) COLD_RUNS="$2"; shift 2 ;;
    --warm-runs) WARM_RUNS="$2"; shift 2 ;;
    --keep-image-override) KEEP_IMAGE_OVERRIDE=1; shift ;;
    *) echo "Unknown arg: $1" >&2; exit 2 ;;
  esac
done

if [[ -z "${CONFIG_LABEL}" || -z "${OUT_DIR}" ]]; then
  echo "Usage: $0 --config-label <label> --out-dir <dir> [--image-variant original|pyc] [--backend <name>] [--image-dir <dir>] [--engine-artifact <path>] [--sign-hardened] [--cold-runs 5] [--warm-runs 5]" >&2
  exit 2
fi

mkdir -p "${OUT_DIR}"
OUT_DIR="$(cd "${OUT_DIR}" && pwd)"

OS="$(uname -s)"
HOST_LOAD="$(uptime | sed 's/^[[:space:]]*//')"
if [[ "${OS}" == "Darwin" ]]; then
  CPU_BRAND="$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)"
  CPU_COUNT="$(sysctl -n hw.ncpu 2>/dev/null || echo unknown)"
  OS_VER="$(sw_vers -productVersion 2>/dev/null || uname -r)"
  CPU_INFO="${CPU_BRAND} (${CPU_COUNT} cores, macOS ${OS_VER})"
else
  CPU_BRAND="$(awk -F: '/model name/{print $2; exit}' /proc/cpuinfo 2>/dev/null | sed 's/^[[:space:]]*//' || echo unknown)"
  CPU_COUNT="$(nproc 2>/dev/null || echo unknown)"
  CPU_INFO="${CPU_BRAND} (${CPU_COUNT} cores, $(uname -r))"
fi

GO_VER="$(go version)"
CC_VER="$(cc --version 2>/dev/null | head -n 1 || echo none)"
TOOLCHAIN="${GO_VER}; ${CC_VER}"
REVISION="$(git -C "${REPO_ROOT}" rev-parse HEAD 2>/dev/null || echo unknown)"

BIN_PATH="${OUT_DIR}/${CONFIG_LABEL}.test"
LOG_PATH="${OUT_DIR}/${CONFIG_LABEL}.log"
JSON_PATH="${OUT_DIR}/${CONFIG_LABEL}.json"

echo "=== PY06 Benchmark Build ==="
echo "config_label:  ${CONFIG_LABEL}"
echo "image_variant: ${IMAGE_VARIANT}"
echo "revision:      ${REVISION}"
echo "toolchain:     ${TOOLCHAIN}"
echo "cpu_info:      ${CPU_INFO}"
echo "host_load:     ${HOST_LOAD}"

(
  cd "${REPO_ROOT}/lycaon"
  go test -c -o "${BIN_PATH}" ./internal/portablepython
)

SIGNING_FLAGS="unsigned"
if [[ "${OS}" == "Darwin" && "${SIGN_HARDENED}" == "1" ]]; then
  codesign --force --sign - --options runtime "${BIN_PATH}"
  SIGNING_FLAGS="$(codesign -dvv "${BIN_PATH}" 2>&1 | grep -E 'Flags=|Signature=|Authority=' | tr '\n' '; ' | sed 's/; $//')"
  echo "signing:       ${SIGNING_FLAGS}"
fi

# Capture host load immediately before the timed matrix begins
HOST_LOAD_BEFORE="$(uptime | sed 's/^[[:space:]]*//')"

# By default unset PW_PORTABLE_PYTHON_IMAGE_DIR so normal discovery runs unless overridden.
if [[ "${KEEP_IMAGE_OVERRIDE}" != "1" ]]; then
  unset PW_PORTABLE_PYTHON_IMAGE_DIR
fi

export PW_PERF_RUN=1
export PW_PERF_CONFIG_LABEL="${CONFIG_LABEL}"
export PW_PERF_IMAGE_VARIANT="${IMAGE_VARIANT}"
export PW_PERF_BACKEND="${BACKEND}"
export PW_PERF_IMAGE_DIR="${IMAGE_DIR}"
export PW_PERF_COLD_RUNS="${COLD_RUNS}"
export PW_PERF_WARM_RUNS="${WARM_RUNS}"
export PW_PERF_SCRIPT_PATH="${SCRIPT_DIR}/fixtures/workbook.py"
export PW_PERF_YAML_PATH="${SCRIPT_DIR}/fixtures/repos.yaml"
export PW_PERF_OUT_JSON="${JSON_PATH}"
export PW_PERF_REVISION="${REVISION}"
export PW_PERF_TOOLCHAIN="${TOOLCHAIN}"
export PW_PERF_SIGNING_FLAGS="${SIGNING_FLAGS}"
export PW_PERF_HOST_LOAD="${HOST_LOAD_BEFORE}"
export PW_PERF_CPU_INFO="${CPU_INFO}"
export PW_PERF_BINARY_PATH="${BIN_PATH}"
export PW_PERF_ENGINE_ARTIFACT_PATH="${ENGINE_ARTIFACT}"

echo "=== Running PY06 Timed Matrix (${COLD_RUNS} cold + ${WARM_RUNS} warm) ==="
echo "host_load_before: ${HOST_LOAD_BEFORE}"

"${BIN_PATH}" -test.v -test.timeout=60m -test.run '^TestPY06NativePerfWorkbookMatrix$' 2>&1 | tee "${LOG_PATH}"

# Remove the ephemeral compiled test binary from results directory after recording its digest/size
rm -f "${BIN_PATH}"

python3 "${SCRIPT_DIR}/summarize.py" "${OUT_DIR}" --out "${OUT_DIR}/summary.md"

(
  cd "${OUT_DIR}"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 *.json *.log summary.md > SHA256SUMS
  else
    sha256sum *.json *.log summary.md > SHA256SUMS
  fi
)

echo "=== PY06 Benchmark Complete ==="
echo "json:    ${JSON_PATH}"
echo "log:     ${LOG_PATH}"
echo "summary: ${OUT_DIR}/summary.md"
