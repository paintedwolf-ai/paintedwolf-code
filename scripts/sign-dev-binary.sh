#!/usr/bin/env bash
# Signs a development binary with the hardened runtime releases ship under.
# The stable identity keeps Keychain grants across rebuilds; without it the
# signature is ad hoc. Signing failures leave the build usable.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TARGET="${1:?usage: sign-dev-binary.sh <path-to-binary>}"
# shellcheck source=dev-signing-config.sh
source "${ROOT}/scripts/dev-signing-config.sh"

if [[ "$(uname -s)" != "Darwin" || ! -f "${TARGET}" ]]; then
  exit 0
fi

HARDENED=(--force --options runtime --entitlements "${ROOT}/scripts/dev-entitlements.plist" --timestamp=none)
if security find-identity -v -p codesigning 2>/dev/null | grep -qF "${DEV_SIGNING_IDENTITY}"; then
  if codesign "${HARDENED[@]}" --sign "${DEV_SIGNING_IDENTITY}" --identifier "${DEV_SIGNING_IDENTIFIER}" \
    "${TARGET}" 2>/dev/null; then
    echo "signed ${TARGET#"${ROOT}"/} with ${DEV_SIGNING_IDENTITY} (hardened runtime)" >&2
    exit 0
  fi
  echo "warning: codesign could not use ${DEV_SIGNING_IDENTITY} — signing ${TARGET##*/} ad hoc." >&2
  echo "         Re-provision with: ./task dev:signing-identity" >&2
fi

if ! codesign "${HARDENED[@]}" --sign - "${TARGET}" 2>/dev/null; then
  echo "warning: codesign could not sign ${TARGET##*/} with the hardened runtime" >&2
fi
