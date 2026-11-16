#!/usr/bin/env bash
# Prove the imported Developer ID identity signs and the notary service accepts the API key.
set -euo pipefail

: "${APPLE_SIGNING_IDENTITY:?missing APPLE_SIGNING_IDENTITY}"
: "${APPLE_API_ISSUER:?missing APPLE_API_ISSUER}"
: "${APPLE_API_KEY:?missing APPLE_API_KEY_ID}"
: "${APPLE_API_KEY_PATH:?run macos-signing-import.sh first}"
: "${APPLE_ENGINE_PROVISIONING_PROFILE:?run macos-signing-import.sh first}"
: "${RUNNER_TEMP:?missing RUNNER_TEMP}"

PROBE="${RUNNER_TEMP}/signing-probe"
cp /usr/bin/true "${PROBE}"
codesign --force --options runtime --sign "${APPLE_SIGNING_IDENTITY}" "${PROBE}"
codesign --verify --strict "${PROBE}"
rm -f "${PROBE}"
security cms -D -i "${APPLE_ENGINE_PROVISIONING_PROFILE}" >/dev/null
# The listing names earlier submissions, so only the service's verdict on the key is kept.
xcrun notarytool history --key "${APPLE_API_KEY_PATH}" --key-id "${APPLE_API_KEY}" \
  --issuer "${APPLE_API_ISSUER}" --output-format json >/dev/null
echo "macOS signing identity, provisioning profile and notary key accepted"
