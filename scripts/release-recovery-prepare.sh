#!/usr/bin/env bash
# Record reviewed provenance for a higher-version recovery release.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WITHDRAWS=""
RESTORES_FROM=""
GENERATION=""

usage() {
  echo "Usage: release-recovery-prepare.sh --withdraws BAD_VERSION --restores-from LAST_GOOD_VERSION" >&2
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --withdraws) WITHDRAWS="${2:-}"; shift 2 ;;
    --restores-from) RESTORES_FROM="${2:-}"; shift 2 ;;
    --generation) GENERATION="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "error: unknown argument: $1" >&2; usage ;;
  esac
done
[[ -n "${WITHDRAWS}" && -n "${RESTORES_FROM}" ]] || usage

[[ -n "${GENERATION}" ]] || GENERATION="$(jq -r .signing_generation "${ROOT}/packaging/update-keys.json")"
[[ "${GENERATION}" =~ ^[1-9][0-9]*$ ]] || usage
[[ "${GENERATION}" == "$(jq -r .signing_generation "${ROOT}/packaging/update-keys.json")" ]] || {
  echo "error: recovery signing generation must match the configured release" >&2; exit 1;
}
python3 "${ROOT}/scripts/release-metadata.py" --root "${ROOT}" >/dev/null
VERSION="$(tr -d '[:space:]' < "${ROOT}/VERSION")"
python3 "${ROOT}/scripts/semver-compare.py" gt "${VERSION}" "${WITHDRAWS}" >/dev/null || {
  echo "error: recovery VERSION=${VERSION} must be newer than withdrawn ${WITHDRAWS}" >&2
  exit 1
}
python3 "${ROOT}/scripts/semver-compare.py" gt "${WITHDRAWS}" "${RESTORES_FROM}" >/dev/null || {
  echo "error: withdrawn ${WITHDRAWS} must be newer than restored source ${RESTORES_FROM}" >&2
  exit 1
}

mkdir -p "${ROOT}/.release"
TMP="$(mktemp "${ROOT}/.release/recovery.json.XXXXXX")"
trap 'rm -f "${TMP}"' EXIT
jq -n --arg version "${VERSION}" --arg withdraws "${WITHDRAWS}" \
  --arg restores_from "${RESTORES_FROM}" --argjson signing_generation "${GENERATION}" \
  '{signing_generation: $signing_generation, version: $version, withdraws: $withdraws, restores_from: $restores_from}' > "${TMP}"
mv "${TMP}" "${ROOT}/.release/recovery.json"
trap - EXIT
echo "release recovery metadata prepared for ${VERSION}; review and commit .release/recovery.json" >&2
