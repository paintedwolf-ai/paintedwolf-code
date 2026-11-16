#!/usr/bin/env bash
# Load the Developer ID identity, notary key and engine provisioning profile for one job.
set -euo pipefail

: "${APPLE_CERTIFICATE:?missing APPLE_CERTIFICATE}"
: "${APPLE_CERTIFICATE_PASSWORD:?missing APPLE_CERTIFICATE_PASSWORD}"
: "${APPLE_API_KEY_P8:?missing APPLE_API_KEY_P8}"
: "${APPLE_ENGINE_PROVISIONING_PROFILE_BASE64:?missing APPLE_ENGINE_PROVISIONING_PROFILE_BASE64}"
: "${APPLE_SIGNING_IDENTITY:?missing APPLE_SIGNING_IDENTITY}"
: "${RUNNER_TEMP:?missing RUNNER_TEMP}"
: "${GITHUB_ENV:?missing GITHUB_ENV}"

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
