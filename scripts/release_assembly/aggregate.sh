#!/usr/bin/env bash
# Assemble the updater manifest, public artifact set and casks from every signed platform result.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ARTIFACTS="" OUTPUT="" PUBLIC="" VERSION="" CHANNEL="" CASK_TOKEN=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --artifacts) ARTIFACTS="${2:-}"; shift 2 ;;
    --output) OUTPUT="${2:-}"; shift 2 ;;
    --public) PUBLIC="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --channel) CHANNEL="${2:-}"; shift 2 ;;
    --cask-token) CASK_TOKEN="${2:-}"; shift 2 ;;
    *) echo "error: unknown argument: $1" >&2; exit 2 ;;
  esac
done
[[ -d "${ARTIFACTS}" && -n "${OUTPUT}" && -n "${PUBLIC}" && -n "${VERSION}" && -n "${CHANNEL}" && -n "${CASK_TOKEN}" ]] || {
  echo "Usage: aggregate.sh --artifacts DIR --output DIR --public DIR --version VERSION --channel CHANNEL --cask-token TOKEN" >&2
  exit 2
}
: "${DOWNLOAD_BASE_URL:?missing DOWNLOAD_BASE_URL}"
cd "${ROOT}"
NOTES="$(mktemp "${TMPDIR:-/tmp}/release-notes.XXXXXX")"
trap 'rm -f "${NOTES}"' EXIT
awk -v heading="## [${VERSION}]" \
  'index($0, heading) == 1 {flag=1; next} /^## \[/ {flag=0} flag' CHANGELOG.md > "${NOTES}"
mkdir -p "${OUTPUT}"
python3 scripts/release-assemble-updater-manifest.py \
  --catalog packaging/release-platforms.json \
  --fragments "${ARTIFACTS}" --version "${VERSION}" --notes "${NOTES}" \
  --pub-date "$(git show -s --format=%cI HEAD)" \
  --base-url "${DOWNLOAD_BASE_URL}" --output "${OUTPUT}/latest.json"
python3 scripts/release-collect-public-artifacts.py \
  --catalog packaging/release-platforms.json --input "${ARTIFACTS}" \
  --version "${VERSION}" --output "${PUBLIC}"
bash scripts/release-validate-updater-manifest.sh --file "${OUTPUT}/latest.json" --version "${VERSION}"
bash scripts/release-validate-bridge.sh "${OUTPUT}/latest.json"
SHA="$(cat "${ARTIFACTS}/painted-wolf-code_v${VERSION}_darwin-aarch64.dmg.sha256")"
render() {
  bash scripts/release_assembly/render-cask.sh --token "$1" --channel "$2" --version "${VERSION}" --sha256 "${SHA}" \
    --base-url "${DOWNLOAD_BASE_URL}" --output "${OUTPUT}/$1.rb"
}
render "${CASK_TOKEN}" "${CHANNEL}"
if [[ "${CHANNEL}" == stable ]]; then
  render painted-wolf-code@preview preview
fi
