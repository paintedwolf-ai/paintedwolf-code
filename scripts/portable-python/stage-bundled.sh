#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MANIFEST="${PW_PORTABLE_PYTHON_ENGINE_MANIFEST:-${ROOT_DIR}/lycaon/config/runtime/portable-python/bundled-manifest.yaml}"
CACHE_DIR="${PW_PORTABLE_PYTHON_ENGINE_CACHE:-${HOME}/.cache/paintedwolf/portable-python-engine}"

STAGE_ROOT=""
TARGET=""
GOOS_ARG=""
GOARCH_ARG=""
IMAGE_SOURCE="${PW_PORTABLE_PYTHON_IMAGE_DIR:-}"
ARTIFACT_DIR="${PW_PORTABLE_PYTHON_ARTIFACT_DIR:-}"

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
    --image-dir|--image-source)
      IMAGE_SOURCE="${2:-}"
      shift 2
      ;;
    --artifact-dir)
      ARTIFACT_DIR="${2:-}"
      shift 2
      ;;
    --require-engine|--require-image)
      # Both checked-hash image and verified native engine are always required (fail-closed).
      shift 1
      ;;
    *)
      echo "usage: $0 --root <engine-root> [--target <triple>] [--image-source <tarball-or-dir>] [--artifact-dir <dir>]" >&2
      exit 2
      ;;
  esac
done

if [[ -z "${STAGE_ROOT}" ]]; then
  echo "usage: $0 --root <engine-root> [--target <triple>] [--image-source <tarball-or-dir>] [--artifact-dir <dir>]" >&2
  exit 2
fi

# Stage BOTH the verified checked-hash CPython 3.12 .pyc image AND the verified
# PY07 native AOT engine atomically, purging any stale files and failing closed
# if either is missing, stale, or fails digest/bytecode verification.
if [[ -n "${PW_PORTABLE_PYTHON_ENGINE_DIR:-}" && -d "${PW_PORTABLE_PYTHON_ENGINE_DIR}" ]]; then
  CACHE_DIR="${PW_PORTABLE_PYTHON_ENGINE_DIR}"
fi

ARGS=(-mode stage-bundle -manifest "${MANIFEST}" -root "${STAGE_ROOT}" -cache-dir "${CACHE_DIR}")
if [[ -n "${TARGET}" ]]; then
  ARGS+=(-target "${TARGET}")
else
  if [[ -n "${GOOS_ARG}" ]]; then ARGS+=(-goos "${GOOS_ARG}"); fi
  if [[ -n "${GOARCH_ARG}" ]]; then ARGS+=(-goarch "${GOARCH_ARG}"); fi
fi
if [[ -n "${IMAGE_SOURCE}" ]]; then
  ARGS+=(-image-source "${IMAGE_SOURCE}")
fi
if [[ -n "${ARTIFACT_DIR}" ]]; then
  ARGS+=(-artifact-dir "${ARTIFACT_DIR}")
fi

(
  cd "${ROOT_DIR}/lycaon"
  go run ./cmd/portable-python-artifact "${ARGS[@]}"
)
