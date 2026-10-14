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

# An ownerless lock is reclaimed only once old, whichever stat dialect is on PATH.
mkdir "${TMP}/gnu" "${TMP}/bsd"
cat >"${TMP}/gnu/stat" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  -c) echo "${FAKE_MTIME}" ;;
  -f) printf '  File: "%s"\n    ID: 0 Namelen: 255 Type: ext2/ext3\n' "$3" ;;
  *) exit 1 ;;
esac
EOF
cat >"${TMP}/bsd/stat" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  -f) echo "${FAKE_MTIME}" ;;
  *) echo "stat: illegal option -- ${1#-}" >&2; exit 1 ;;
esac
EOF
chmod +x "${TMP}/gnu/stat" "${TMP}/bsd/stat"

for dialect in gnu bsd; do
  mkdir "${LOCKDIR}"
  if PATH="${TMP}/${dialect}:${PATH}" FAKE_MTIME="$(date +%s)" digest_try_reclaim_stale "${LOCKDIR}"; then
    echo "${dialect} stat: expected fresh ownerless lock to be kept" >&2
    exit 1
  fi
  [[ -d "${LOCKDIR}" ]]
  PATH="${TMP}/${dialect}:${PATH}" FAKE_MTIME="$(( $(date +%s) - 60 ))" digest_try_reclaim_stale "${LOCKDIR}" 2>/dev/null
  [[ ! -d "${LOCKDIR}" ]]
done

echo "lock smoke OK"
