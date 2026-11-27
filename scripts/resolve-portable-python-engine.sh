#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST="${PW_PORTABLE_PYTHON_ENGINE_MANIFEST:-${ROOT_DIR}/lycaon/config/runtime/portable-python/bundled-manifest.yaml}"
CACHE_DIR="${PW_PORTABLE_PYTHON_ENGINE_CACHE:-${HOME}/.cache/paintedwolf/portable-python-engine}"

TARGET=""
GOOS_ARG=""
GOARCH_ARG=""

while [[ $# -gt 0 ]]; do
  case "$1" in
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
    *)
      echo "usage: $0 [--target <triple> | --goos <os> --goarch <arch>] [--cache-dir <dir>] [--manifest <path>]" >&2
      exit 2
      ;;
  esac
done

ARGS=(-mode resolve -manifest "${MANIFEST}" -cache-dir "${CACHE_DIR}")
if [[ -n "${TARGET}" ]]; then
  ARGS+=(-target "${TARGET}")
else
  if [[ -n "${GOOS_ARG}" ]]; then ARGS+=(-goos "${GOOS_ARG}"); fi
  if [[ -n "${GOARCH_ARG}" ]]; then ARGS+=(-goarch "${GOARCH_ARG}"); fi
fi

(
  cd "${ROOT_DIR}/lycaon"
  go run ./cmd/portable-python-artifact "${ARGS[@]}"
)
