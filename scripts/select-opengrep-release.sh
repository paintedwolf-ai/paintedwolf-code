#!/usr/bin/env bash
# Select an exact qualified engine release for application builds.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TAG=""
EXPECTED_COMMIT=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag|--expected-commit)
      [[ $# -ge 2 ]] || { echo "selection identity option requires a value" >&2; exit 2; }
      if [[ "$1" == --tag ]]; then TAG="$2"; else EXPECTED_COMMIT="$2"; fi
      shift 2
      ;;
    *) break ;;
  esac
done
if [[ $# -ne 1 || -z "${TAG}" || -z "${EXPECTED_COMMIT}" ]]; then
  echo "usage: select-opengrep-release.sh --tag <tag> --expected-commit <reviewed SHA> <release.json path or HTTPS URL>" >&2
  exit 2
fi
RELEASE="$1"
case "${RELEASE}" in
  https://*|/*|[A-Za-z]:/*|[A-Za-z]:\\*) ;;
  *) RELEASE="${PWD}/${RELEASE}" ;;
esac
# shellcheck source=artifact-paths.sh
source "$(dirname "$0")/artifact-paths.sh"
CACHE_DIR="${OPENGREP_CACHE_DIR:-${PW_BIN_DIR}/opengrep-artifacts}"
case "${CACHE_DIR}" in
  /*|[A-Za-z]:/*|[A-Za-z]:\\*) ;;
  *) CACHE_DIR="${PWD}/${CACHE_DIR}" ;;
esac
cd "${ROOT}/lycaon"
exec env -u LYCAON_OPENGREP_CANDIDATE go run ./cmd/opengrep-artifact \
  -mode select -release-manifest "${RELEASE}" \
  -tag "${TAG}" -expected-commit "${EXPECTED_COMMIT}" \
  -manifest-output "${ROOT}/lycaon/config/runtime/scanners/bundled-manifest.yaml" \
  -cache-root "${CACHE_DIR}"
