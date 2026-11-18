#!/usr/bin/env bash
# Render one channel's Homebrew cask from the template.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TOKEN="" CHANNEL="" VERSION="" SHA="" BASE_URL="" OUTPUT=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --token) TOKEN="${2:-}"; shift 2 ;;
    --channel) CHANNEL="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --sha256) SHA="${2:-}"; shift 2 ;;
    --base-url) BASE_URL="${2:-}"; shift 2 ;;
    --output) OUTPUT="${2:-}"; shift 2 ;;
    *) echo "error: unknown argument: $1" >&2; exit 2 ;;
  esac
done
case "${TOKEN}/${CHANNEL}" in
  painted-wolf-code/stable) CONFLICTS=painted-wolf-code@preview ;;
  painted-wolf-code@preview/preview) CONFLICTS=painted-wolf-code ;;
  *) echo "error: cask ${TOKEN} does not serve channel ${CHANNEL}" >&2; exit 2 ;;
esac
[[ "${SHA}" =~ ^[0-9a-f]{64}$ && -n "${VERSION}" && "${BASE_URL}" =~ ^https?://[^/]+$ && -n "${OUTPUT}" ]] || {
  echo "error: render-cask needs --version, a SHA-256 --sha256, an origin --base-url and --output" >&2
  exit 2
}
python3 "${ROOT}/scripts/semver-compare.py" eq "${VERSION}" "${VERSION}" >/dev/null
# Both casks install the same app, so each names the other.
sed -e "s|@@TOKEN@@|${TOKEN}|g" \
  -e "s|@@VERSION@@|${VERSION}|g" -e "s|@@SHA_ARM@@|${SHA}|g" \
  -e "s|@@BASE_URL@@|${BASE_URL}|g" \
  -e "s|@@CHANNEL@@|${CHANNEL}|g" \
  -e "s|@@CONFLICTS@@|${CONFLICTS}|g" \
  "${ROOT}/packaging/homebrew/painted-wolf-code.rb.tmpl" > "${OUTPUT}"
grep -Fq "release_channel\\\":\\\"${CHANNEL}" "${OUTPUT}" || {
  echo "error: ${TOKEN} does not write the ${CHANNEL} install marker" >&2
  exit 1
}
if grep -n "@@" "${OUTPUT}"; then
  echo "error: unrendered placeholder in ${OUTPUT}" >&2
  exit 1
fi
