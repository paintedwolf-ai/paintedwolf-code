#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if ! python3 "$ROOT/scripts/test-execution.py" holding; then
  exec python3 "$ROOT/scripts/test-execution.py" run --name "go-test-digest" -- bash "$0" "$@"
fi
SOURCE_SNAPSHOT_SH="${ROOT}/scripts/test-source-snapshot.sh"
if ! bash "$SOURCE_SNAPSHOT_SH" holding; then
  exec bash "$SOURCE_SNAPSHOT_SH" run -- bash scripts/go-test-digest.sh "$@"
fi
if ! bash "$ROOT/scripts/repo-snapshot-lock.sh" holding; then
  exec bash "$ROOT/scripts/repo-snapshot-lock.sh" read -- "$0" "$@"
fi

unset GIT_DIR GIT_INDEX_FILE GIT_WORK_TREE GIT_PREFIX GIT_OBJECT_DIRECTORY

ORIGINAL_ARGS=("$@")

GO_DIR="${ROOT}/lycaon"
DIGEST="${ROOT}/scripts/test-digest.py"
MANIFEST_TOOL="${ROOT}/scripts/test-run-manifest.py"
LOCK_SH="${ROOT}/scripts/digest-run-lock.sh"
ISOLATION_SH="${ROOT}/scripts/test-run-isolation.sh"
CAPACITY_SH="${ROOT}/scripts/test-host-capacity.sh"
# shellcheck source=artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
ARTIFACT_ROOT="${PW_ARTIFACT_ROOT}"
SOURCE_REPO="${PW_SOURCE_ROOT_ORIGINAL:-${ROOT}}"
LOGDIR="${ARTIFACT_ROOT}/last-run"
mkdir -p "${LOGDIR}"
# shellcheck source=digest-run-lock.sh
source "${LOCK_SH}"
# shellcheck source=test-run-isolation.sh
source "${ISOLATION_SH}"
# shellcheck source=test-host-capacity.sh
source "${CAPACITY_SH}"

NAME="go-test"
SHORT_FLAG=(-short)
RACE_FLAG=()
BUILD_TAGS=""
EXCLUDE_PKG=""
RAW="${LOGDIR}/go-test.json"
GO_TEST_TIMEOUT_SECONDS="${PW_GO_TEST_TIMEOUT_SECONDS:-}"
PACKAGE_TIMEOUT="10m"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)
      NAME="$2"
      shift 2
      ;;
    --exclude-pkg)
      # Extended regex matched against package import paths.
      EXCLUDE_PKG="$2"
      shift 2
      ;;
    --tags)
      BUILD_TAGS="$2"
      shift 2
      ;;
    --full)
      SHORT_FLAG=()
      shift
      ;;
    --race)
      RACE_FLAG=(-race)
      shift
      ;;
    --timeout)
      # Per-package wall-clock budget.
      PACKAGE_TIMEOUT="$2"
      shift 2
      ;;
    --raw)
      RAW="$2"
      shift 2
      ;;
    --)
      shift
      break
      ;;
    *)
      break
      ;;
  esac
done

if [[ $# -eq 0 ]]; then
  PKGS=(./...)
else
  PKGS=("$@")
fi

LOCKDIR="${LOGDIR}/go-test.lockdir"
LATEST_FAILURE="${LOGDIR}/latest-go-failure"
TMP_STEM=""
TMP_RAW=""
RUN_DIR=""
MANIFEST=""
GO_PGID=""
GO_WATCHDOG_PID=""
TIMEOUT_FLAG=""
reap_test_group() {
  if [[ -n "${GO_PGID}" ]]; then
    kill -TERM -- "-${GO_PGID}" 2>/dev/null || true
    if kill -0 -- "-${GO_PGID}" 2>/dev/null; then
      sleep 0.1
      kill -KILL -- "-${GO_PGID}" 2>/dev/null || true
    fi
  fi
}
reap_watchdog() {
  if [[ -n "${GO_WATCHDOG_PID}" ]]; then
    kill -TERM -- "${GO_WATCHDOG_PID}" 2>/dev/null || true
    wait "${GO_WATCHDOG_PID}" 2>/dev/null || true
    GO_WATCHDOG_PID=""
  fi
}
cleanup() {
  reap_watchdog
  reap_test_group
  digest_release_lock
  for tmp in "${TMP_STEM:-}" "${TMP_RAW:-}"; do
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
trap 'reap_test_group; reap_watchdog; exit 130' INT TERM

export GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
export GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}"
export GOPATH="${GOPATH:-$(go env GOPATH)}"
test_run_create_isolation "go-test"
RUN_DIR="${TEST_RUN_DIR}"
export LAST_RUN_DIR="${RUN_DIR}"
RUN_FAILED_PKGS="${RUN_DIR}/failed-go-pkgs.txt"
RUNNER_ARGV_FILE="${RUN_DIR}/runner.argv"
GO_ARGV_FILE="${RUN_DIR}/go.argv"
MANIFEST="${RUN_DIR}/manifest.json"
test_run_export_isolation "${RUN_DIR}"
printf '%s\0' "${ORIGINAL_ARGS[@]}" >"${RUNNER_ARGV_FILE}"

# Reserving the stem prevents report filename collisions.
TMP_STEM="$(mktemp "${RUN_DIR}/capture.XXXXXX")"
TMP_RAW="${TMP_STEM}.json"
GO_RAW="${TMP_RAW}"
if [[ -n "${PW_TEST_STAGE_RESULT:-}" ]]; then
  GO_RAW="${PW_TEST_STAGE_RAW:?batch raw report path is required}"
fi

cd "${GO_DIR}"

REPLAY_MANIFEST="${PW_TEST_REPLAY_MANIFEST:-}"
unset PW_TEST_REPLAY_MANIFEST
if [[ -n "${REPLAY_MANIFEST}" ]]; then
  REPLAY_GO_ARGV_FILE="${RUN_DIR}/replay-go.argv"
  python3 "${MANIFEST_TOOL}" go-argv --manifest "${REPLAY_MANIFEST}" >"${REPLAY_GO_ARGV_FILE}"
  TEST_ARGS=()
  while IFS= read -r -d '' arg; do
    TEST_ARGS+=("${arg}")
  done <"${REPLAY_GO_ARGV_FILE}"
  TEST_TIMEOUT_SCALE="${PW_TEST_TIMEOUT_SCALE:?replay manifest has no timeout scale}"
  P_PKGS="${GO_TEST_P:?replay manifest has no package concurrency}"
  P_INTRA="${GO_TEST_PARALLEL:?replay manifest has no test concurrency}"
  GO_TEST_TIMEOUT_SECONDS="${PW_GO_TEST_TIMEOUT_SECONDS:?replay manifest has no watchdog timeout}"
else
  if [[ ${#PKGS[@]} -eq 1 && "${PKGS[0]}" == "./..." && -n "${EXCLUDE_PKG}" ]]; then
    GO_LIST_OUT="$(go list ./...)"
    FILTERED=()
    while IFS= read -r pkg; do
      FILTERED+=("$pkg")
    done < <(printf '%s\n' "${GO_LIST_OUT}" | grep -Ev "${EXCLUDE_PKG}" || true)
    if ((${#FILTERED[@]} == 0)); then
      echo "go-test-digest: no packages left after --exclude-pkg ${EXCLUDE_PKG}" >&2
      exit 1
    fi
    PKGS=("${FILTERED[@]}")
  fi

  NCPU="$(test_host_cpu_count)"
  HOST_LOAD="$(test_host_load_one)"
  TEST_TIMEOUT_SCALE="$(test_host_timeout_scale "${NCPU}" "${HOST_LOAD}")"
  export PW_TEST_TIMEOUT_SCALE="${TEST_TIMEOUT_SCALE}"
  P_PKGS="${GO_TEST_P:?verification admission must set GO_TEST_P}"
  P_INTRA="${GO_TEST_PARALLEL:?verification admission must set GO_TEST_PARALLEL}"
  PACKAGE_TIMEOUT="$(test_scale_go_duration "${PACKAGE_TIMEOUT}" "${TEST_TIMEOUT_SCALE}")"
  if [[ -z "${GO_TEST_TIMEOUT_SECONDS}" ]]; then
    GO_TEST_TIMEOUT_SECONDS="$(test_scale_integer 2700 "${TEST_TIMEOUT_SCALE}")"
  fi
  TEST_ARGS=(-json -p "${P_PKGS}" -parallel "${P_INTRA}" -timeout "${PACKAGE_TIMEOUT}")
  if ((${#SHORT_FLAG[@]})); then
    TEST_ARGS+=("${SHORT_FLAG[@]}")
  else
    # Filesystem-heavy runs avoid test-result input hashing.
    TEST_ARGS+=(-count=1)
  fi
  if ((${#RACE_FLAG[@]})); then TEST_ARGS+=("${RACE_FLAG[@]}"); fi
  if [[ -n "${BUILD_TAGS}" ]]; then TEST_ARGS+=("-tags" "${BUILD_TAGS}"); fi
  TEST_ARGS+=("${PKGS[@]}")
fi

python3 "$ROOT/scripts/process-group-watchdog.py" --validate "${GO_TEST_TIMEOUT_SECONDS}"
python3 "$ROOT/scripts/test-execution.py" go-arguments -- "${TEST_ARGS[@]}" >"${RUN_DIR}/bounded-go.argv"
BOUNDED_GO=()
while IFS= read -r -d '' arg; do
  BOUNDED_GO+=("$arg")
done <"${RUN_DIR}/bounded-go.argv"
P_PKGS="${BOUNDED_GO[0]}"
P_INTRA="${BOUNDED_GO[1]}"
EFFECTIVE_GOMAXPROCS="${BOUNDED_GO[2]}"
TEST_ARGS=("${BOUNDED_GO[@]:3}")
if [[ -z "${REPLAY_MANIFEST}" ]] && ((TEST_TIMEOUT_SCALE > 1)); then
  printf 'go-test-digest: host load %s on %s CPUs; package limit=%s, parallel test limit=%s, GOMAXPROCS=%s, timeout scale=%sx\n' \
    "${HOST_LOAD}" "${NCPU}" "${P_PKGS}" "${P_INTRA}" "${EFFECTIVE_GOMAXPROCS}" "${TEST_TIMEOUT_SCALE}" >&2
fi
if [[ -n "${REPLAY_MANIFEST}" ]]; then
  printf 'go-test-digest: replaying with package limit=%s, parallel test limit=%s, GOMAXPROCS=%s, timeout scale=%sx\n' \
    "${P_PKGS}" "${P_INTRA}" "${EFFECTIVE_GOMAXPROCS}" "${TEST_TIMEOUT_SCALE}" >&2
fi
printf '%s\0' "${TEST_ARGS[@]}" >"${GO_ARGV_FILE}"
RUN_ID="$(date +%Y%m%dT%H%M%S)-$$-${RANDOM:-0}"
SOURCE_COMMIT="${PW_SOURCE_SNAPSHOT_COMMIT}"
SOURCE_REF="refs/painted-wolf/test-failures/${RUN_ID}"
EFFECTIVE_LLM_MOCK="${LYCAON_LLM_MOCK:-1}"
python3 "${MANIFEST_TOOL}" create \
  --output "${MANIFEST}" \
  --id "${RUN_ID}" \
  --name "${NAME}" \
  --source-commit "${SOURCE_COMMIT}" \
  --source-ref "${SOURCE_REF}" \
  --source-root "${ROOT}" \
  --runner-argv "${RUNNER_ARGV_FILE}" \
  --go-argv "${GO_ARGV_FILE}" \
  --env "PW_TEST_TIMEOUT_SCALE=${TEST_TIMEOUT_SCALE}" \
  --env "GO_TEST_P=${P_PKGS}" \
  --env "GO_TEST_PARALLEL=${P_INTRA}" \
  --env "PW_GO_TEST_TIMEOUT_SECONDS=${GO_TEST_TIMEOUT_SECONDS}" \
  --env "GOMAXPROCS=${EFFECTIVE_GOMAXPROCS}" \
  --env "LYCAON_LLM_MOCK=${EFFECTIVE_LLM_MOCK}"

# Shared stages run each test binary through the reuse and early-outcome program; the replay
# manifest keeps the plain invocation, which does not depend on this snapshot's scripts.
EXEC_ARGS=()
if [[ -n "${PW_TEST_STAGE_EVENTS:-}" ]]; then
  EXEC_ARGS=(-exec "${ROOT}/scripts/go-test-exec.py")
  if [[ -n "${PW_TEST_REUSE_STORE:-}" ]]; then
    # Recorded results replace Go's result cache, which a fresh snapshot's file times defeat anyway.
    if ((${#SHORT_FLAG[@]})); then
      EXEC_ARGS+=(-count=1)
    fi
    export PW_TEST_PACKAGE_IDENTITIES="${RUN_DIR}/package-identities.json"
    if ! python3 "${ROOT}/scripts/verification_reuse.py" identities "${PW_TEST_PACKAGE_IDENTITIES}" -- "${TEST_ARGS[@]}" \
      2>"${RUN_DIR}/package-identities.log"; then
      echo "go-test-digest: package build identities unavailable; every package runs (${RUN_DIR}/package-identities.log)" >&2
    fi
  fi
  export PW_TEST_SCRATCH_ROOT="${RUN_DIR}" PW_TEST_SOURCE_ROOT="${ROOT}" PW_TEST_MODULE_ROOT="${GO_DIR}"
fi

set +e
set -m
GO_RC=0
LYCAON_LLM_MOCK="${EFFECTIVE_LLM_MOCK}" GOMAXPROCS="${EFFECTIVE_GOMAXPROCS}" go test ${EXEC_ARGS[@]+"${EXEC_ARGS[@]}"} "${TEST_ARGS[@]}" >"${GO_RAW}" 2>&1 &
GO_PGID=$!
TIMEOUT_FLAG="${RUN_DIR}/go-test-timeout"
python3 "${ROOT}/scripts/process-group-watchdog.py" \
  "${GO_TEST_TIMEOUT_SECONDS}" "${GO_PGID}" "${TIMEOUT_FLAG}" "${NAME}" &
GO_WATCHDOG_PID=$!
wait "${GO_PGID}"
GO_RC=$?
reap_watchdog
if [[ -f "${TIMEOUT_FLAG}" ]]; then
  GO_RC=124
fi
reap_test_group
set +m
GO_PGID=""
set -e

if [[ "${GO_RAW}" != "${TMP_RAW}" ]]; then
  cp "${GO_RAW}" "${TMP_RAW}"
fi

set +e
python3 "${DIGEST}" --name "${NAME}" --process-rc "${GO_RC}" --raw-log "${RAW#${ARTIFACT_ROOT}/}" <"${TMP_RAW}"
DIGEST_RC=$?
set -e

FAILED=0
FAILURE_REL=""
if [[ ${GO_RC} -ne 0 ]] || [[ ${DIGEST_RC} -ne 0 ]]; then
  FAILED=1
  python3 "${MANIFEST_TOOL}" finalize --manifest "${MANIFEST}" --failed-packages "${RUN_FAILED_PKGS}"
  FAILURE_REL="failures/go-test-${RUN_ID}"
  FAILURE_DIR="${LOGDIR}/${FAILURE_REL}"
  FAILURE_STAGE="${RUN_DIR}/failure"
  mkdir -p "${FAILURE_STAGE}"
  cp -f "${TMP_RAW}" "${FAILURE_STAGE}/go-test.json"
  cp -f "${MANIFEST}" "${FAILURE_STAGE}/manifest.json"
  if [[ -s "${RUN_FAILED_PKGS}" ]]; then
    cp -f "${RUN_FAILED_PKGS}" "${FAILURE_STAGE}/failed-packages.txt"
  fi
fi

if [[ -n "${PW_TEST_STAGE_RESULT:-}" ]]; then
  python3 "$ROOT/scripts/verification_results.py" "${PW_TEST_STAGE_RESULT}" "${TMP_RAW}" \
    "${GO_RC}" "${DIGEST_RC}" "${MANIFEST}"
fi

digest_acquire_lock "${LOCKDIR}" || exit 1
mv -f "${TMP_RAW}" "${RAW}"
TMP_RAW=""
if [[ -n "${FAILURE_REL}" ]]; then
  mkdir -p "${LOGDIR}/failures"
  git -C "${SOURCE_REPO}" update-ref "${SOURCE_REF}" "${SOURCE_COMMIT}"
  mv "${FAILURE_STAGE}" "${FAILURE_DIR}"
  POINTER_TMP="${LATEST_FAILURE}.$$"
  printf '%s\n' "${FAILURE_REL}" >"${POINTER_TMP}"
  mv -f "${POINTER_TMP}" "${LATEST_FAILURE}"
  python3 "${MANIFEST_TOOL}" prune \
    --failures-root "${LOGDIR}/failures" --repo "${SOURCE_REPO}" --keep 10
fi
digest_release_lock
if [[ -n "${FAILURE_REL}" ]]; then
  printf '    retained: %s\n' "${FAILURE_DIR#${ARTIFACT_ROOT}/}"
fi

if [[ ${FAILED} -ne 0 ]]; then
  exit 1
fi
exit 0
