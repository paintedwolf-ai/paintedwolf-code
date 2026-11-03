#!/usr/bin/env bash
# Verify the public restore path on a new installation, including restart readers.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
source "${ROOT}/scripts/upgrade-store-contract.sh"
[[ $# -eq 2 ]] || { echo 'usage: upgrade-archive-roundtrip.sh SIDECAR FIXTURE' >&2; exit 2; }
ENGINE="$1"
FIXTURE="$2"
SCRATCH="$(mktemp -d "${TMPDIR:-/tmp}/upgrade-archive-roundtrip.XXXXXX")"
upgrade_install_child_cleanup
cleanup() {
  local code=$?
  upgrade_stop_owned_child || exit 1
  if (( code != 0 )); then cat "${SCRATCH}/sidecar.log" >&2 || true; fi
  rm -rf "${SCRATCH}"
  exit "${code}"
}
trap cleanup EXIT
CONFIG="${SCRATCH}/config"
mkdir -p "${CONFIG}"
TOKEN="upgrade-archive-roundtrip-token"
printf '%s' "${TOKEN}" >"${CONFIG}/api.token"
chmod 600 "${CONFIG}/api.token"
BASE=""

start_engine() {
  rm -f "${CONFIG}/daemon.json"
  (
    cd "${ROOT}/lycaon"
    LYCAON_CONFIG_DIR="${CONFIG}" LYCAON_LLM_MOCK=1 LYCAON_DEV=1 \
      LYCAON_ADDR="127.0.0.1:0" LYCAON_API_TOKEN="${TOKEN}" \
      LYCAON_LOG_LEVEL=warn exec "${ENGINE}" serve
  ) >"${SCRATCH}/sidecar.log" 2>&1 &
  UPGRADE_CHILD_PID=$!
  local deadline=$((SECONDS + 60)) body status
  while (( SECONDS < deadline )); do
    if BASE="$(upgrade_owned_base "${CONFIG}" "${UPGRADE_CHILD_PID}")" &&
       body="$(curl -sf --max-time 2 "${BASE}/health")"; then
      status="$(jq -r '.status // empty' <<<"${body}")"
      [[ "${status}" == "ok" || "${status}" == "degraded" ]] && return 0
      [[ "${status}" == "recovery" ]] && return 1
    fi
    kill -0 "${UPGRADE_CHILD_PID}" 2>/dev/null || return 1
    sleep 0.25
  done
  return 1
}

start_engine
if ! curl --fail-with-body -sS --max-time 120 -X POST -H "Authorization: Bearer ${TOKEN}" \
  -H 'Content-Type: application/zip' --data-binary "@${FIXTURE}/backup.zip" \
  "${BASE}/v1/backup/restore" >"${SCRATCH}/restore.json"; then
  cat "${SCRATCH}/restore.json" >&2
  exit 1
fi
jq -e '.restart_required == true' "${SCRATCH}/restore.json" >/dev/null
upgrade_stop_owned_child
# Startup installs the staged archive before fixture paths can be relocated.
start_engine
upgrade_stop_owned_child
# Restore fixture files before readers compare retained state with the project.
source "${ROOT}/scripts/config-dir.sh"
PROJECT="${SCRATCH}/project"
cp -a "${FIXTURE}/project-root" "${PROJECT}"
mv "${PROJECT}/${OVERLAY_FIXTURE_DIR}" "${PROJECT}/$(LYCAON_DEV=1 lycaon_overlay_dir)"
PROJECT_ID="$(jq -r '.project_id' "${FIXTURE}/MANIFEST.json")"
sqlite3 "${CONFIG}/store.db" "UPDATE project_roots SET path = '${PROJECT//\'/\'\'}' WHERE project_id = '${PROJECT_ID//\'/\'\'}';"
start_engine
python3 "${ROOT}/scripts/upgrade-corpus-semantics.py" verify --base "${BASE}" \
  --token-file "${CONFIG}/api.token" --fixture "${FIXTURE}" --config-dir "${CONFIG}"
upgrade_stop_owned_child
"${ENGINE}" diagnostics upgrade-fixture-verify "${CONFIG}" "${FIXTURE}/MANIFEST.json"
# Replays must not publish subsequent typing.
start_engine
node "${ROOT}/scripts/upgrade-editor-fixture.mjs" replay "${BASE}" "${CONFIG}" "${FIXTURE}/MANIFEST.json" "${PROJECT}"
upgrade_stop_owned_child
echo 'archive roundtrip: retained history, reconstruction, and editor delivery passed' >&2
