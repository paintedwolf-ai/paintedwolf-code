#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if ! python3 "$ROOT/scripts/test-execution.py" holding; then
  exec python3 "$ROOT/scripts/test-execution.py" run --name "vitest-digest" -- bash "$0" "$@"
fi
SOURCE_SNAPSHOT_SH="${ROOT}/scripts/test-source-snapshot.sh"
if ! bash "$SOURCE_SNAPSHOT_SH" holding; then
  exec bash "$SOURCE_SNAPSHOT_SH" run -- bash scripts/vitest-digest.sh "$@"
fi
if ! bash "$ROOT/scripts/repo-snapshot-lock.sh" holding; then
  exec bash "$ROOT/scripts/repo-snapshot-lock.sh" read -- "$0" "$@"
fi
DEN_DIR="${ROOT}/lycaon-den"
DIGEST="${ROOT}/scripts/vitest-digest.py"
LOCK_SH="${ROOT}/scripts/digest-run-lock.sh"
ISOLATION_SH="${ROOT}/scripts/test-run-isolation.sh"
CAPACITY_SH="${ROOT}/scripts/test-host-capacity.sh"
# shellcheck source=artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
ARTIFACT_ROOT="${PW_ARTIFACT_ROOT}"
LOGDIR="${ARTIFACT_ROOT}/last-run"
mkdir -p "${LOGDIR}"
# shellcheck source=digest-run-lock.sh
source "${LOCK_SH}"
# shellcheck source=test-run-isolation.sh
source "${ISOLATION_SH}"
# shellcheck source=test-host-capacity.sh
source "${CAPACITY_SH}"

NAME="den:test"
RAW="${LOGDIR}/den-test.json"
DEFAULT_RAW="${LOGDIR}/den-test.json"
VITEST_ARGS=()
VITEST_TIMEOUT_SECONDS="${PW_VITEST_TIMEOUT_SECONDS:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)
      NAME="$2"
      shift 2
      ;;
    --raw)
      RAW="$2"
      shift 2
      ;;
    --)
      shift
      VITEST_ARGS=("$@")
      break
      ;;
    *)
      VITEST_ARGS=("$@")
      break
      ;;
  esac
done

LOCKDIR="${LOGDIR}/den-test.lockdir"
TMP_STEM=""
TMP_RAW=""
TMP_OUT=""
TMP_OUTCOME=""
RUN_DIR=""
VITEST_PGID=""
WATCHDOG_PGID=""
TIMEOUT_FLAG=""
reap_test_group() {
  if [[ -n "${VITEST_PGID}" ]]; then
    kill -TERM -- "-${VITEST_PGID}" 2>/dev/null || true
    if kill -0 -- "-${VITEST_PGID}" 2>/dev/null; then
      sleep 0.1
      kill -KILL -- "-${VITEST_PGID}" 2>/dev/null || true
    fi
  fi
}
reap_watchdog() {
  if [[ -n "${WATCHDOG_PGID}" ]]; then
    kill -TERM -- "${WATCHDOG_PGID}" 2>/dev/null || true
    wait "${WATCHDOG_PGID}" 2>/dev/null || true
    WATCHDOG_PGID=""
  fi
}
cleanup() {
  reap_watchdog
  reap_test_group
  digest_release_lock
  for tmp in "${TMP_STEM:-}" "${TMP_RAW:-}" "${TMP_OUT:-}" "${TMP_OUTCOME:-}"; do
    if [[ -n "${tmp}" && -f "${tmp}" ]]; then
      rm -f "${tmp}"
    fi
  done
  if [[ -n "${RUN_DIR}" && -d "${RUN_DIR}" ]]; then
    test_run_remove_isolation "${RUN_DIR}"
  fi
  if [[ -n "${TIMEOUT_FLAG}" ]]; then
    rm -f "${TIMEOUT_FLAG}"
  fi
  rmdir "${TEST_RUN_ROOT}" 2>/dev/null || true
}
trap cleanup EXIT
trap 'reap_test_group; exit 130' INT TERM

# The document core builds into the user cache, which the run's isolated home would hide.
# shellcheck source=document-core-env.sh
source "${ROOT}/scripts/document-core-env.sh"
test_run_create_isolation "den-test"
RUN_DIR="${TEST_RUN_DIR}"
export LAST_RUN_DIR="${RUN_DIR}"
test_run_export_isolation "${RUN_DIR}"
TMP_STEM="$(mktemp "${RUN_DIR}/capture.XXXXXX")"
TMP_RAW="${TMP_STEM}.json"
: >"${TMP_RAW}"
TMP_OUT="${TMP_RAW%.json}.out"
OUT_LOG="${RAW%.json}.out"
TMP_OUTCOME="${TMP_RAW%.json}.outcome.json"
OUTCOME_LOG="${RAW%.json}.outcome.json"

cd "${DEN_DIR}"
NCPU="$(test_host_cpu_count)"
HOST_LOAD="$(test_host_load_one)"
TEST_TIMEOUT_SCALE="$(test_host_timeout_scale "${NCPU}" "${HOST_LOAD}")"
export PW_TEST_TIMEOUT_SCALE="${TEST_TIMEOUT_SCALE}"
: "${PW_VITEST_MAX_WORKERS:?verification admission must set PW_VITEST_MAX_WORKERS}"
if [[ -z "${VITEST_TIMEOUT_SECONDS}" ]]; then
  VITEST_TIMEOUT_SECONDS="$(test_scale_integer 1200 "${TEST_TIMEOUT_SCALE}")"
fi
python3 "$ROOT/scripts/process-group-watchdog.py" --validate "${VITEST_TIMEOUT_SECONDS}"
if ((TEST_TIMEOUT_SCALE > 1)); then
  printf 'vitest-digest: host load %s on %s CPUs; workers=%s, timeout scale=%sx\n' \
    "${HOST_LOAD}" "${NCPU}" "${PW_VITEST_MAX_WORKERS}" "${TEST_TIMEOUT_SCALE}" >&2
fi
REPORTERS=(
  --reporter=json --outputFile="${TMP_RAW}"
  --reporter=./vitest-run-outcome-reporter.ts
  --reporter=default
)
export VITEST_OUTCOME_FILE="${TMP_OUTCOME}"
set +e
set -m
if ((${#VITEST_ARGS[@]})); then
  bun run vitest run "${REPORTERS[@]}" "${VITEST_ARGS[@]}" >"${TMP_OUT}" 2>&1 &
else
  bun run vitest run "${REPORTERS[@]}" >"${TMP_OUT}" 2>&1 &
fi
VITEST_PGID=$!
TIMEOUT_FLAG="${RUN_DIR}/vitest-timeout"
python3 "${ROOT}/scripts/process-group-watchdog.py" \
  "${VITEST_TIMEOUT_SECONDS}" "${VITEST_PGID}" "${TIMEOUT_FLAG}" "${NAME}" &
WATCHDOG_PGID=$!
wait "${VITEST_PGID}"
VITEST_RC=$?
reap_watchdog
if [[ -f "${TIMEOUT_FLAG}" ]]; then
  VITEST_RC=124
fi
reap_test_group
VITEST_PGID=""
set +m
set -e

set +e
python3 "${DIGEST}" --name "${NAME}" --raw-log "${RAW#${ARTIFACT_ROOT}/}" --input "${TMP_RAW}" \
  --vitest-rc "${VITEST_RC}" --stdout-log "${TMP_OUT}" --stdout-log-path "${OUT_LOG#${ARTIFACT_ROOT}/}" \
  --outcome "${TMP_OUTCOME}"
DIGEST_RC=$?
set -e

publish_captures() {
  mv -f "${TMP_RAW}" "${RAW}"
  TMP_RAW=""
  mv -f "${TMP_OUT}" "${OUT_LOG}"
  TMP_OUT=""
  # A hard crash may prevent the outcome report.
  if [[ -f "${TMP_OUTCOME}" ]]; then
    mv -f "${TMP_OUTCOME}" "${OUTCOME_LOG}"
    TMP_OUTCOME=""
  else
    rm -f "${OUTCOME_LOG}"
  fi
  if [[ "${RAW}" == "${DEFAULT_RAW}" ]]; then
    if [[ -s "${RUN_DIR}/failed-den-files.txt" ]]; then
      mv -f "${RUN_DIR}/failed-den-files.txt" "${LOGDIR}/failed-den-files.txt"
    else
      rm -f "${LOGDIR}/failed-den-files.txt"
    fi
  fi
}

FAILED=0
if [[ ${VITEST_RC} -ne 0 ]] || [[ ${DIGEST_RC} -ne 0 ]]; then
  FAILED=1
  FAIL_DIR="${LOGDIR}/failures"
  mkdir -p "${FAIL_DIR}"
  STAMP="$(date +%Y%m%dT%H%M%S)-$$"
  for src_dst in \
    "${TMP_OUT}:${FAIL_DIR}/den-test-${STAMP}.out" \
    "${TMP_RAW}:${FAIL_DIR}/den-test-${STAMP}.json" \
    "${TMP_OUTCOME}:${FAIL_DIR}/den-test-${STAMP}.outcome.json"; do
    src="${src_dst%%:*}"
    dst="${src_dst##*:}"
    if [[ -f "${src}" ]]; then
      cp -f "${src}" "${dst}"
    fi
  done
  printf '    retained: %s\n' "${FAIL_DIR#${ARTIFACT_ROOT}/}/den-test-${STAMP}.{out,json,outcome.json}"
  ls -1t "${FAIL_DIR}"/den-test-*.out 2>/dev/null | tail -n +11 | while read -r stale; do
    rm -f "${stale}" "${stale%.out}.json" "${stale%.out}.outcome.json"
  done
fi

if [[ "${RAW}" == "${DEFAULT_RAW}" ]]; then
  digest_acquire_lock "${LOCKDIR}" || exit 1
  publish_captures
  digest_release_lock
else
  publish_captures
fi

if [[ ${FAILED} -ne 0 ]]; then
  exit 1
fi
exit 0
