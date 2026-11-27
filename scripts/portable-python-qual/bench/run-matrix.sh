#!/usr/bin/env bash
set -euo pipefail

# Runs the complete 4-configuration PY06 idle-host comparison matrix on the
# current platform (macOS arm64 or Linux amd64):
#   1. wazero-control + original image (no .pyc)
#   2. wazero-control + deterministic checked-hash .pyc image
#   3. default (native-aot) + original image (no .pyc)
#   4. default (native-aot) + deterministic checked-hash .pyc image

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

ORIG_IMAGE_DIR=""
PYC_IMAGE_DIR=""
ENGINE_DIR=""
OUT_DIR=""
SIGN_HARDENED_ARG=()
COLD_RUNS=5
WARM_RUNS=5

while [[ $# -gt 0 ]]; do
  case "$1" in
    --orig-image-dir) ORIG_IMAGE_DIR="$2"; shift 2 ;;
    --pyc-image-dir) PYC_IMAGE_DIR="$2"; shift 2 ;;
    --engine-dir) ENGINE_DIR="$2"; shift 2 ;;
    --out-dir) OUT_DIR="$2"; shift 2 ;;
    --sign-hardened) SIGN_HARDENED_ARG=(--sign-hardened); shift ;;
    --cold-runs) COLD_RUNS="$2"; shift 2 ;;
    --warm-runs) WARM_RUNS="$2"; shift 2 ;;
    *) echo "Unknown arg: $1" >&2; exit 2 ;;
  esac
done

if [[ -z "${ORIG_IMAGE_DIR}" || -z "${PYC_IMAGE_DIR}" || -z "${OUT_DIR}" ]]; then
  echo "Usage: $0 --orig-image-dir <dir> --pyc-image-dir <dir> --out-dir <dir> [--engine-dir <dir>] [--sign-hardened] [--cold-runs 5] [--warm-runs 5]" >&2
  exit 2
fi

OS_TAG="$(uname -s | tr '[:upper:]' '[:lower:]')"
if [[ "${OS_TAG}" == "darwin" ]]; then
  PLAT="macos"
  WAZERO_LABEL="wazero-interpreter"
else
  PLAT="linux"
  WAZERO_LABEL="wazero-compiler"
fi

ENGINE_ARGS=()
if [[ -n "${ENGINE_DIR}" ]]; then
  ENGINE_ARGS=(--engine-dir "${ENGINE_DIR}")
fi

# 1. wazero control + original image
"${SCRIPT_DIR}/run-bench.sh" \
  --config-label "${PLAT}-${WAZERO_LABEL}-original" \
  --backend "wazero-control" \
  --image-variant "original" \
  --image-dir "${ORIG_IMAGE_DIR}" \
  --out-dir "${OUT_DIR}" \
  --cold-runs "${COLD_RUNS}" \
  --warm-runs "${WARM_RUNS}" \
  "${SIGN_HARDENED_ARG[@]}"

# 2. wazero control + deterministic .pyc image
"${SCRIPT_DIR}/run-bench.sh" \
  --config-label "${PLAT}-${WAZERO_LABEL}-pyc" \
  --backend "wazero-control" \
  --image-variant "pyc" \
  --image-dir "${PYC_IMAGE_DIR}" \
  --out-dir "${OUT_DIR}" \
  --cold-runs "${COLD_RUNS}" \
  --warm-runs "${WARM_RUNS}" \
  "${SIGN_HARDENED_ARG[@]}"

# 3. native-aot (default route) + original image
"${SCRIPT_DIR}/run-bench.sh" \
  --config-label "${PLAT}-native-aot-original" \
  --backend "" \
  --image-variant "original" \
  --image-dir "${ORIG_IMAGE_DIR}" \
  --out-dir "${OUT_DIR}" \
  --cold-runs "${COLD_RUNS}" \
  --warm-runs "${WARM_RUNS}" \
  "${ENGINE_ARGS[@]}" \
  "${SIGN_HARDENED_ARG[@]}"

# 4. native-aot (default route) + deterministic .pyc image
"${SCRIPT_DIR}/run-bench.sh" \
  --config-label "${PLAT}-native-aot-pyc" \
  --backend "" \
  --image-variant "pyc" \
  --image-dir "${PYC_IMAGE_DIR}" \
  --out-dir "${OUT_DIR}" \
  --cold-runs "${COLD_RUNS}" \
  --warm-runs "${WARM_RUNS}" \
  "${ENGINE_ARGS[@]}" \
  "${SIGN_HARDENED_ARG[@]}"
