#!/usr/bin/env bash
# Developer ID custody for one job: `import` loads the identity, notary key and
# engine provisioning profile; `probe` proves each works without signing a release.
set -euo pipefail

import_identity() {
  : "${APPLE_CERTIFICATE:?missing APPLE_CERTIFICATE}"
  : "${APPLE_CERTIFICATE_PASSWORD:?missing APPLE_CERTIFICATE_PASSWORD}"
  : "${APPLE_API_KEY_P8:?missing APPLE_API_KEY_P8}"
  : "${APPLE_ENGINE_PROVISIONING_PROFILE_BASE64:?missing APPLE_ENGINE_PROVISIONING_PROFILE_BASE64}"
  : "${APPLE_SIGNING_IDENTITY:?missing APPLE_SIGNING_IDENTITY}"
  : "${RUNNER_TEMP:?missing RUNNER_TEMP}"
  : "${GITHUB_ENV:?missing GITHUB_ENV}"

  local KEYCHAIN KEYCHAIN_PW
  KEYCHAIN="${RUNNER_TEMP}/app-signing.keychain-db"
  KEYCHAIN_PW="$(openssl rand -base64 24)"
  echo "${APPLE_CERTIFICATE}" | openssl base64 -d -A -out "${RUNNER_TEMP}/cert.p12"
  security create-keychain -p "${KEYCHAIN_PW}" "${KEYCHAIN}"
  security set-keychain-settings -lut 21600 "${KEYCHAIN}"
  security unlock-keychain -p "${KEYCHAIN_PW}" "${KEYCHAIN}"
  security import "${RUNNER_TEMP}/cert.p12" -P "${APPLE_CERTIFICATE_PASSWORD}" \
    -A -t cert -f pkcs12 -k "${KEYCHAIN}"
  security set-key-partition-list -S apple-tool:,apple:,codesign: \
    -s -k "${KEYCHAIN_PW}" "${KEYCHAIN}" >/dev/null
  security list-keychain -d user -s "${KEYCHAIN}" login.keychain-db
  rm -f "${RUNNER_TEMP}/cert.p12"
  echo "${APPLE_API_KEY_P8}" | openssl base64 -d -A -out "${RUNNER_TEMP}/AuthKey.p8"
  echo "APPLE_API_KEY_PATH=${RUNNER_TEMP}/AuthKey.p8" >> "${GITHUB_ENV}"
  echo "${APPLE_ENGINE_PROVISIONING_PROFILE_BASE64}" | openssl base64 -d -A -out "${RUNNER_TEMP}/engine.provisionprofile"
  echo "APPLE_ENGINE_PROVISIONING_PROFILE=${RUNNER_TEMP}/engine.provisionprofile" >> "${GITHUB_ENV}"
}

probe() {
  : "${APPLE_SIGNING_IDENTITY:?missing APPLE_SIGNING_IDENTITY}"
  : "${APPLE_API_ISSUER:?missing APPLE_API_ISSUER}"
  : "${APPLE_API_KEY:?missing APPLE_API_KEY_ID}"
  : "${APPLE_API_KEY_PATH:?run signing.sh import first}"
  : "${APPLE_ENGINE_PROVISIONING_PROFILE:?run signing.sh import first}"
  : "${RUNNER_TEMP:?missing RUNNER_TEMP}"

  local PROBE="${RUNNER_TEMP}/signing-probe"
  cp /usr/bin/true "${PROBE}"
  codesign --force --options runtime --sign "${APPLE_SIGNING_IDENTITY}" "${PROBE}"
  codesign --verify --strict "${PROBE}"
  rm -f "${PROBE}"
  security cms -D -i "${APPLE_ENGINE_PROVISIONING_PROFILE}" >/dev/null
  # The listing names earlier submissions, so only the service's verdict on the key is kept.
  xcrun notarytool history --key "${APPLE_API_KEY_PATH}" --key-id "${APPLE_API_KEY}" \
    --issuer "${APPLE_API_ISSUER}" --output-format json >/dev/null
  echo "macOS signing identity, provisioning profile and notary key accepted"
}

case "${1:-}" in
  import) import_identity ;;
  probe) probe ;;
  *) echo "usage: signing.sh import|probe" >&2; exit 2 ;;
esac
