#!/usr/bin/env bash
# Replay the latest failed Go test run.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
LOGDIR="${PW_ARTIFACT_ROOT}/last-run"
LATEST_FAILURE="${LOGDIR}/latest-go-failure"
LOCK_SH="${ROOT}/scripts/digest-run-lock.sh"
LOCKDIR="${LOGDIR}/go-test.lockdir"
MANIFEST_TOOL="${ROOT}/scripts/test-run-manifest.py"
SNAPSHOT_TOOL="${ROOT}/scripts/test-source-snapshot.sh"
# shellcheck source=digest-run-lock.sh
source "${LOCK_SH}"

MANIFEST_SNAPSHOT="$(mktemp "${TMPDIR:-/tmp}/paintedwolf-go-replay.XXXXXX")"
REPLAY_REF=""
cleanup() {
  digest_release_lock
  if [[ -n "${REPLAY_REF}" ]]; then
    git -C "${ROOT}" update-ref -d "${REPLAY_REF}" 2>/dev/null || true
  fi
  if [[ -f "${MANIFEST_SNAPSHOT}" ]]; then
    unlink "${MANIFEST_SNAPSHOT}"
  fi
}

digest_acquire_lock "${LOCKDIR}" || exit 1
trap cleanup EXIT

if [[ ! -s "${LATEST_FAILURE}" ]]; then
  digest_release_lock
  echo "No failed Go test run recorded." >&2
  echo "Run a Go test digest first." >&2
  exit 1
fi

FAILURE_REL="$(sed -n '1p' "${LATEST_FAILURE}")"
case "${FAILURE_REL}" in
  failures/go-test-*) ;;
  *)
    digest_release_lock
    echo "Invalid Go failure pointer: ${FAILURE_REL}" >&2
    exit 1
    ;;
esac
MANIFEST="${LOGDIR}/${FAILURE_REL}/manifest.json"
if [[ ! -f "${MANIFEST}" ]]; then
  digest_release_lock
  echo "Go failure manifest is missing: ${MANIFEST#${ROOT}/}" >&2
  exit 1
fi
cp -f "${MANIFEST}" "${MANIFEST_SNAPSHOT}"
SOURCE_COMMIT="$(python3 "${MANIFEST_TOOL}" source-commit --manifest "${MANIFEST_SNAPSHOT}")"
REPLAY_REF="refs/painted-wolf/test-replays/$$-${RANDOM:-0}"
git -C "${ROOT}" update-ref "${REPLAY_REF}" "${SOURCE_COMMIT}"
digest_release_lock

if ! git -C "${ROOT}" cat-file -e "${SOURCE_COMMIT}^{commit}" 2>/dev/null; then
  echo "Recorded test source is no longer available: ${SOURCE_COMMIT}" >&2
  exit 1
fi

echo "Replaying ${FAILURE_REL} at source ${SOURCE_COMMIT:0:12}..."
set +e
bash "${SNAPSHOT_TOOL}" run-commit "${SOURCE_COMMIT}" -- \
  python3 "${MANIFEST_TOOL}" replay \
    --manifest "${MANIFEST_SNAPSHOT}" \
    --runner scripts/go-test-digest.sh \
    --current-source-root .
rc=$?
set -e
exit "$rc"
