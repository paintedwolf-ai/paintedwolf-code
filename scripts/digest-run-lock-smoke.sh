#!/usr/bin/env bash
# Exercise stale recovery and live-holder exclusion.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=digest-run-lock.sh
source "${ROOT}/scripts/digest-run-lock.sh"

TMP="$(mktemp -d "${TMPDIR:-/tmp}/digest-lock-XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT
LOCKDIR="${TMP}/go-test.lockdir"

mkdir "${LOCKDIR}"
echo "99999999" >"${LOCKDIR}/pid"
digest_acquire_lock "${LOCKDIR}" 5
[[ -f "${LOCKDIR}/pid" ]]
[[ "$(cat "${LOCKDIR}/pid")" == "$$" ]]
digest_release_lock
[[ ! -d "${LOCKDIR}" ]]

mkdir "${LOCKDIR}"
echo $$ >"${LOCKDIR}/pid"
if (
  # shellcheck source=digest-run-lock.sh
  source "${ROOT}/scripts/digest-run-lock.sh"
  DIGEST_LOCK_TIMEOUT=2
  digest_acquire_lock "${LOCKDIR}" 2
); then
  echo "expected live-holder acquire to fail" >&2
  exit 1
fi
rm -f "${LOCKDIR}/pid"
rmdir "${LOCKDIR}"

echo "lock smoke OK"
