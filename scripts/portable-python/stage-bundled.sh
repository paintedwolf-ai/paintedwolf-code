#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MANIFEST="${PW_PORTABLE_PYTHON_ENGINE_MANIFEST:-${ROOT_DIR}/lycaon/config/runtime/portable-python/bundled-manifest.yaml}"
CACHE_DIR="${PW_PORTABLE_PYTHON_ENGINE_CACHE:-${HOME}/.cache/paintedwolf/portable-python-engine}"

STAGE_ROOT=""
TARGET=""
GOOS_ARG=""
GOARCH_ARG=""
IMAGE_DIR="${PW_PORTABLE_PYTHON_IMAGE_DIR:-}"
REQUIRE_ENGINE="${PW_REQUIRE_PORTABLE_PYTHON_ENGINE:-0}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --root)
      STAGE_ROOT="${2:-}"
      shift 2
      ;;
    --target)
      TARGET="${2:-}"
      shift 2
      ;;
    --goos)
      GOOS_ARG="${2:-}"
      shift 2
      ;;
    --goarch)
      GOARCH_ARG="${2:-}"
      shift 2
      ;;
    --cache-dir)
      CACHE_DIR="${2:-}"
      shift 2
      ;;
    --manifest)
      MANIFEST="${2:-}"
      shift 2
      ;;
    --image-dir)
      IMAGE_DIR="${2:-}"
      shift 2
      ;;
    --require-engine)
      REQUIRE_ENGINE=1
      shift 1
      ;;
    *)
      echo "usage: $0 --root <engine-root> [--target <triple>] [--image-dir <dir>] [--require-engine]" >&2
      exit 2
      ;;
  esac
done

if [[ -z "${STAGE_ROOT}" ]]; then
  echo "usage: $0 --root <engine-root> [--target <triple>] [--image-dir <dir>] [--require-engine]" >&2
  exit 2
fi

# 1. Stage Portable Python WASI image if available.
if [[ -z "${IMAGE_DIR}" && -f "${ROOT_DIR}/scripts/portable-python/image/image-manifest.json" ]]; then
  IMAGE_DIR="${ROOT_DIR}/scripts/portable-python/image"
fi
if [[ -n "${IMAGE_DIR}" && -f "${IMAGE_DIR}/image-manifest.json" ]]; then
  rm -rf "${STAGE_ROOT}/portable-python/image"
  mkdir -p "${STAGE_ROOT}/portable-python"
  cp -R "${IMAGE_DIR}" "${STAGE_ROOT}/portable-python/image"
fi

# 2. Stage verified native engine artifact if pinned or explicitly staged via PW_PORTABLE_PYTHON_ENGINE_DIR.
if [[ -n "${PW_PORTABLE_PYTHON_ENGINE_DIR:-}" && -d "${PW_PORTABLE_PYTHON_ENGINE_DIR}" ]]; then
  ARGS=(-mode stage -manifest "${MANIFEST}" -root "${STAGE_ROOT}" -cache-dir "${PW_PORTABLE_PYTHON_ENGINE_DIR}")
  if [[ -n "${TARGET}" ]]; then ARGS+=(-target "${TARGET}"); fi
  if [[ -n "${GOOS_ARG}" ]]; then ARGS+=(-goos "${GOOS_ARG}"); fi
  if [[ -n "${GOARCH_ARG}" ]]; then ARGS+=(-goarch "${GOARCH_ARG}"); fi
  (cd "${ROOT_DIR}/lycaon" && go run ./cmd/portable-python-artifact "${ARGS[@]}")
  exit 0
fi

# Check whether bundled-manifest.yaml has a populated sha256 for this release before attempting fetch.
if grep -qE '^\s+sha256:\s+"?[0-9a-f]{64}"?' "${MANIFEST}"; then
  FETCH_ARGS=(-mode fetch -manifest "${MANIFEST}" -cache-dir "${CACHE_DIR}")
  STAGE_ARGS=(-mode stage -manifest "${MANIFEST}" -root "${STAGE_ROOT}" -cache-dir "${CACHE_DIR}")
  if [[ -n "${TARGET}" ]]; then
    FETCH_ARGS+=(-target "${TARGET}")
    STAGE_ARGS+=(-target "${TARGET}")
  else
    if [[ -n "${GOOS_ARG}" ]]; then
      FETCH_ARGS+=(-goos "${GOOS_ARG}")
      STAGE_ARGS+=(-goos "${GOOS_ARG}")
    fi
    if [[ -n "${GOARCH_ARG}" ]]; then
      FETCH_ARGS+=(-goarch "${GOARCH_ARG}")
      STAGE_ARGS+=(-goarch "${GOARCH_ARG}")
    fi
  fi
  (
    cd "${ROOT_DIR}/lycaon"
    go run ./cmd/portable-python-artifact "${FETCH_ARGS[@]}" >/dev/null
    go run ./cmd/portable-python-artifact "${STAGE_ARGS[@]}"
  )
elif [[ "${REQUIRE_ENGINE}" == "1" ]]; then
  echo "portable python native engine manifest ${MANIFEST} does not pin a release artifact yet; run scripts/select-portable-python-engine.sh first" >&2
  exit 1
fi
