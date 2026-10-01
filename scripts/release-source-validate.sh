#!/usr/bin/env bash
# Validate the release source.
set -euo pipefail

TAG="${1:-}"
SHA="${2:-}"
MAIN_REF="${3:-refs/remotes/origin/main}"

if [[ -z "${TAG}" || -z "${SHA}" ]]; then
  echo "usage: release-source-validate.sh <tag> <sha> [main-ref]" >&2
  exit 2
fi

ROOT="$(git rev-parse --show-toplevel)"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VERSION="$(tr -d '[:space:]' < "${ROOT}/VERSION")"
WANT="v${VERSION}"

if [[ "${TAG}" != v* ]] || ! python3 "${SCRIPT_DIR}/semver-compare.py" eq "${TAG#v}" "${TAG#v}" >/dev/null 2>&1; then
  echo "error: tag '${TAG}' must be a complete semantic version prefixed by v" >&2
  exit 1
fi
if [[ "${TAG}" != "${WANT}" ]]; then
  echo "error: tag ${TAG} does not match VERSION file (${WANT})" >&2
  exit 1
fi

MAIN_SHA="$(git rev-parse "${MAIN_REF}^{commit}")"
if [[ "${SHA}" != "${MAIN_SHA}" ]]; then
  echo "error: tag ${TAG} points at ${SHA}, but current ${MAIN_REF} is ${MAIN_SHA}" >&2
  echo "       Release only the exact reviewed main tip." >&2
  exit 1
fi

CURRENT_BUILD="$(tr -d '[:space:]' < "${ROOT}/RELEASE_BUILD")"
python3 "${SCRIPT_DIR}/release-metadata.py" --root "${ROOT}" >/dev/null
MAX_PRIOR_BUILD=0
PARENT="$(git rev-parse --verify "${SHA}^" 2>/dev/null || true)"
if [[ -n "${PARENT}" ]]; then
  while IFS= read -r prior_tag; do
    [[ -n "${prior_tag}" ]] || continue
    prior_build="$(git show "${prior_tag}:RELEASE_BUILD" 2>/dev/null || true)"
    prior_build="$(printf '%s' "${prior_build}" | tr -d '[:space:]')"
    [[ "${prior_build}" =~ ^[1-9][0-9]*$ ]] || {
      echo "error: prior release ${prior_tag} has no valid RELEASE_BUILD" >&2
      exit 1
    }
    if (( prior_build > MAX_PRIOR_BUILD )); then MAX_PRIOR_BUILD="${prior_build}"; fi
  done < <(git tag --merged "${PARENT}" --list 'v*')
fi
if (( CURRENT_BUILD <= MAX_PRIOR_BUILD )); then
  echo "error: RELEASE_BUILD=${CURRENT_BUILD} must exceed prior release build ${MAX_PRIOR_BUILD}" >&2
  exit 1
fi
