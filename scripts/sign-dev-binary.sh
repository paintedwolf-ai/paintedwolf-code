#!/usr/bin/env bash
# Signs a development binary when a stable identity is available.
# Missing identities and signing failures leave the build usable.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TARGET="${1:?usage: sign-dev-binary.sh <path-to-binary>}"
# shellcheck source=dev-signing-config.sh
source "${ROOT}/scripts/dev-signing-config.sh"

if [[ "$(uname -s)" != "Darwin" || ! -f "${TARGET}" ]]; then
  exit 0
fi

if ! security find-identity -v -p codesigning 2>/dev/null | grep -qF "${DEV_SIGNING_IDENTITY}"; then
  exit 0
fi

if ! codesign --force --sign "${DEV_SIGNING_IDENTITY}" --identifier "${DEV_SIGNING_IDENTIFIER}" \
  --timestamp=none "${TARGET}" 2>/dev/null; then
  echo "warning: codesign could not use ${DEV_SIGNING_IDENTITY} — ${TARGET##*/} stays ad-hoc" >&2
  echo "         signed. Re-provision with: ./task dev:signing-identity" >&2
  exit 0
fi

echo "signed ${TARGET#"${ROOT}"/} with ${DEV_SIGNING_IDENTITY}" >&2
