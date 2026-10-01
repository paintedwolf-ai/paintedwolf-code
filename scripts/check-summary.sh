#!/usr/bin/env bash
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if ! python3 "$ROOT/scripts/test-execution.py" holding; then
  exec python3 "$ROOT/scripts/test-execution.py" run --name "check-summary" -- bash "$0" "$@"
fi
SOURCE_SNAPSHOT_SH="${ROOT}/scripts/test-source-snapshot.sh"
if ! bash "$SOURCE_SNAPSHOT_SH" holding; then
  exec bash "$SOURCE_SNAPSHOT_SH" run -- bash scripts/check-summary.sh "$@"
fi
GO_DIR="${ROOT}/lycaon"
# shellcheck source=artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
ARTIFACT_ROOT="${PW_ARTIFACT_ROOT}"
RUN_ID="$(date +%Y%m%dT%H%M%S)-$$-${RANDOM:-0}"
SUMMARY_ROOT="${ARTIFACT_ROOT}/check-summary"
LOGDIR="${SUMMARY_ROOT}/${RUN_ID}"
VITEST_DIGEST="${ROOT}/scripts/vitest-digest.sh"
LOCK_SH="${ROOT}/scripts/digest-run-lock.sh"
# shellcheck source=digest-run-lock.sh
source "${LOCK_SH}"
mkdir -p "${LOGDIR}"
printf '%s\n' "$$" >"${LOGDIR}/.active"

finish_summary() {
  digest_release_lock
  rm -f "${LOGDIR}/.active"
}
trap finish_summary EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

prune_summaries() {
  local kept=0 active pid candidate
  digest_acquire_lock "${SUMMARY_ROOT}/publish.lockdir" || return 1
  while IFS= read -r candidate; do
    active="${candidate}/.active"
    if [[ -f "${active}" ]]; then
      pid="$(sed -n '1p' "${active}")"
      if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
        continue
      fi
      rm -f "${active}"
    fi
    kept=$((kept + 1))
    if ((kept > 10)); then
      case "${candidate}" in
        "${SUMMARY_ROOT}"/20*) rm -r -- "${candidate}" ;;
      esac
    fi
  done < <(find "${SUMMARY_ROOT}" -mindepth 1 -maxdepth 1 -type d ! -name '*.lockdir' -print | sort -r)
  digest_release_lock
}

GO_TEST_TIER="test:short"
[[ "${FULL:-0}" == "1" ]] && GO_TEST_TIER="test:full"

STEP_NAMES=()
STEP_RESULTS=()

record() { STEP_NAMES+=("$1"); STEP_RESULTS+=("$2"); }

run_plain() {
  local name="$1"; shift
  [[ "$1" == "--" ]] && shift
  local log="${LOGDIR}/${name}.log"
  if "$@" >"${log}" 2>&1; then
    record "${name}" PASS
  else
    record "${name}" FAIL
    printf '\n  ✗ %s — tail of %s:\n' "${name}" "${log#${ROOT}/}"
    tail -n 15 "${log}" | sed 's/^/      /'
  fi
}

run_go_tests() {
  local name="$1"
  local log="${LOGDIR}/${name}.log"
  # The catalog owns package scopes, tags, and scanner requirements.
  if "${ROOT}/task" "${GO_TEST_TIER}" >"${log}" 2>&1; then
    record "${name}" PASS
  else
    record "${name}" FAIL
  fi
  cat "${log}"
}

run_vitest_tests() {
  local name="$1"
  if bash "${VITEST_DIGEST}" --name "${name}" --raw "${LOGDIR}/den-test.json"; then
    record "${name}" PASS
  else
    record "${name}" FAIL
  fi
}

echo "Running check suite (logs: ${LOGDIR#${ROOT}/})..."

run_plain  build       -- bash "${ROOT}/scripts/repo-snapshot-lock.sh" read -- bash -c "cd '${GO_DIR}' && go build ./..."
run_plain  lint:fast   -- bash "${ROOT}/scripts/lint-go.sh" fast
run_go_tests go-test

if command -v bun >/dev/null 2>&1; then
  run_plain den:typecheck -- bash "${ROOT}/scripts/repo-snapshot-lock.sh" read -- bash -c "cd '${DEN_DIR}' && bun run typecheck"
  LYCAON_VITEST_FAST=1 run_vitest_tests den:test:fast
  run_plain den:harness:canary -- bash "${ROOT}/scripts/den-harness-canary.sh"
else
  record den:typecheck SKIP
  record den:test:fast SKIP
  record den:harness:canary SKIP
fi

skip_is_fail=0
[[ "${CI:-}" == "true" ]] && skip_is_fail=1

echo
echo "=== check summary ==="
fails=0
for i in "${!STEP_NAMES[@]}"; do
  name="${STEP_NAMES[$i]}"; res="${STEP_RESULTS[$i]}"
  case "${res}" in
    PASS) printf '  \033[32mPASS\033[0m  %s\n' "${name}" ;;
    SKIP)
      printf '  \033[33mSKIP\033[0m  %s\n' "${name}"
      [[ "${skip_is_fail}" == "1" ]] && fails=$((fails+1))
      ;;
    *)    printf '  \033[31mFAIL\033[0m  %s\n' "${name}"; fails=$((fails+1)) ;;
  esac
done
echo
if [[ ${fails} -eq 0 ]]; then
  finish_summary
  rm -r -- "${LOGDIR}"
  rmdir "${SUMMARY_ROOT}" 2>/dev/null || true
  echo "All steps passed."
  exit 0
fi
finish_summary
prune_summaries
echo "${fails} step(s) failed. Failing detail above; full logs in ${LOGDIR#${ROOT}/}/"
echo "Re-run failed Go packages: ./task test:failed"
exit 1
