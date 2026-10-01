#!/usr/bin/env bash
# Resolve a pinned engine release or an explicit development candidate.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=artifact-paths.sh
source "$(dirname "$0")/artifact-paths.sh"
STAGE_DIR="${OPENGREP_STAGE_DIR:-${PW_BUILD_DIR}/opengrep-bundle}"
case "${STAGE_DIR}" in
  /*|[A-Za-z]:/*|[A-Za-z]:\\*) ;;
  *) STAGE_DIR="${PWD}/${STAGE_DIR}" ;;
esac
OUTPUT=""
TARGET=""
CACHE_DIR="${OPENGREP_CACHE_DIR:-${PW_BIN_DIR}/opengrep-artifacts}"
case "${CACHE_DIR}" in
  /*|[A-Za-z]:/*|[A-Za-z]:\\*) ;;
  *) CACHE_DIR="${PWD}/${CACHE_DIR}" ;;
esac
FETCH_ARGS=(-mode fetch -cache-root "${CACHE_DIR}")
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir-only|--identity-only|--artifact-dir-only) OUTPUT="$1" ;;
    --offline) FETCH_ARGS+=(-offline) ;;
    --target)
      [[ $# -ge 2 ]] || { echo "resolve-opengrep: --target requires a platform triple" >&2; exit 2; }
      TARGET="$2"
      FETCH_ARGS+=(-target "${TARGET}")
      shift
      ;;
    --archive)
      [[ $# -ge 2 ]] || { echo "resolve-opengrep: --archive requires a release archive" >&2; exit 2; }
      ARCHIVE="$2"
      case "${ARCHIVE}" in
        /*|[A-Za-z]:/*|[A-Za-z]:\\*) ;;
        *) ARCHIVE="${PWD}/${ARCHIVE}" ;;
      esac
      FETCH_ARGS+=(-archive "${ARCHIVE}")
      shift
      ;;
    *) echo "resolve-opengrep: unsupported argument: $1" >&2; exit 2 ;;
  esac
  shift
done
if [[ -n "${OPENGREP:-}" || -n "${OPENGREP_SHA256:-}" ]]; then
  if [[ -n "${LYCAON_OPENGREP_CANDIDATE:-}" || "${OUTPUT}" == --identity-only || "${OUTPUT}" == --artifact-dir-only ]]; then
    echo "resolve-opengrep: executable overrides cannot supply a maintained build identity or combine with a candidate" >&2
    exit 1
  fi
  : "${OPENGREP:?OPENGREP_SHA256 requires OPENGREP}"
  : "${OPENGREP_SHA256:?explicit OPENGREP requires OPENGREP_SHA256}"
  case "${OPENGREP}" in
    /*|[A-Za-z]:/*|[A-Za-z]:\\*) ;;
    *) OPENGREP="${PWD}/${OPENGREP}" ;;
  esac
  ARGS=(-mode resolve -executable "${OPENGREP}" -sha256 "${OPENGREP_SHA256}")
elif [[ -n "${LYCAON_OPENGREP_CANDIDATE:-}" && "${OUTPUT}" != --identity-only && "${OUTPUT}" != --artifact-dir-only ]]; then
  ARGS=(-mode resolve -root "${STAGE_DIR}")
else
  ARTIFACT_DIR="$(cd "${ROOT}/lycaon" && env -u LYCAON_OPENGREP_CANDIDATE go run ./cmd/opengrep-artifact "${FETCH_ARGS[@]}")"
  if [[ "${OUTPUT}" == --artifact-dir-only ]]; then
    printf '%s\n' "${ARTIFACT_DIR}"
    exit 0
  fi
  unset LYCAON_OPENGREP_CANDIDATE
  ARGS=(-mode resolve -root "${STAGE_DIR}" -artifact-directory "${ARTIFACT_DIR}")
  if [[ "${OUTPUT}" == --identity-only ]]; then
    ARGS=(-mode identity -artifact-directory "${ARTIFACT_DIR}")
  fi
fi
if [[ "${OUTPUT}" == --dir-only ]]; then ARGS+=(-dir-only); fi
if [[ -n "${TARGET}" ]]; then ARGS+=(-target "${TARGET}"); fi
cd "${ROOT}/lycaon"
exec go run ./cmd/opengrep-artifact "${ARGS[@]}"
