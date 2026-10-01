#!/usr/bin/env bash
# Seed retained history through the development harness and application owners.
# Usage: ./task upgrade:corpus:seed -- [--out DIR] [--sidecar PATH]
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/config-dir.sh
source "${ROOT}/scripts/config-dir.sh"
# shellcheck source=scripts/upgrade-store-contract.sh
source "${ROOT}/scripts/upgrade-store-contract.sh"
# The seed sidecar uses the development channel.
SEED_LYCAON_DEV=1
OVERLAY_DIR="$(lycaon_overlay_dir)"
GO_DIR="${ROOT}/lycaon"
FIXTURE_VERSION="$(tr -d '[:space:]' < "${ROOT}/VERSION")"
DEFAULT_OUT="${ROOT}/lycaon/testdata/upgrade-corpus/${FIXTURE_VERSION}"
# shellcheck source=scripts/artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
DEFAULT_SIDECAR="${PW_BUILD_DIR}/lycaon-dev"

OUT="${DEFAULT_OUT}"
SIDECAR="${DEFAULT_SIDECAR}"

usage() {
  cat >&2 <<'EOF'
Usage: upgrade-corpus-seed.sh [--out DIR] [--sidecar PATH]

  --out DIR       Destination fixture directory (must not exist).
                  Default: lycaon/testdata/upgrade-corpus/<VERSION>
  --sidecar PATH  Sidecar binary. Default: lycaon-dev in the checkout build directory
EOF
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --out)
      [[ $# -ge 2 ]] || usage
      OUT="$2"
      shift 2
      ;;
    --sidecar)
      [[ $# -ge 2 ]] || usage
      SIDECAR="$2"
      shift 2
      ;;
    -h|--help)
      usage
      ;;
    *)
      echo "error: unknown argument: $1" >&2
      usage
      ;;
  esac
done

if [[ -e "${OUT}" ]]; then
  echo "error: output already exists: ${OUT}" >&2
  exit 1
fi
if [[ ! -x "${SIDECAR}" ]]; then
  echo "error: sidecar not executable: ${SIDECAR} — run ./task build:lycaon-dev" >&2
  exit 1
fi
for need in curl jq python3 sqlite3 node; do
  if ! command -v "${need}" >/dev/null 2>&1; then
    echo "error: ${need} required" >&2
    exit 1
  fi
done

# Durable credential relative paths (internal/localdata.credentialRelPaths).
CREDENTIAL_RELS=(
  credential-vault.age
  credential-vault-identity.age
  .credential-vault-development-identity
  api.token
  host-identity.pem
)

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/upgrade-corpus-seed.XXXXXX")"
cleanup() {
  local code=$?
  upgrade_stop_owned_child || exit 1
  rm -rf "${WORKDIR}"
  if [[ ${code} -ne 0 && -e "${OUT}" ]]; then
    rm -rf "${OUT}"
  fi
  exit "${code}"
}
upgrade_install_child_cleanup
trap cleanup EXIT

CONFIG_DIR="${WORKDIR}/config"
SEED_PROJECT_ROOT="${WORKDIR}/seed-project"
STAGE_DIR="${WORKDIR}/stage"
mkdir -p "${CONFIG_DIR}" "${SEED_PROJECT_ROOT}/${OVERLAY_DIR}" "${STAGE_DIR}"
# Canonicalize the project root before rewriting stored paths.
SEED_PROJECT_ROOT="$(cd "${SEED_PROJECT_ROOT}" && pwd -P)"
mkdir -p "${SEED_PROJECT_ROOT}/${OVERLAY_DIR}"

printf 'overlay_format: 1\n' >"${SEED_PROJECT_ROOT}/${OVERLAY_DIR}/overlay.yaml"
printf 'format: 1\ndisabled: []\n' >"${SEED_PROJECT_ROOT}/${OVERLAY_DIR}/extensions.yaml"
# Target for the locked mock tool call (pattern "corpus seed" → read README.md).
printf '# corpus seed\n' >"${SEED_PROJECT_ROOT}/README.md"

PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
ADDR="127.0.0.1:${PORT}"
TOKEN="upgrade-corpus-seed-token"

export LYCAON_CONFIG_DIR="${CONFIG_DIR}"
export LYCAON_LLM_MOCK=1
export LYCAON_HARNESS=1
export LYCAON_DEV="${SEED_LYCAON_DEV}"
export LYCAON_ADDR="${ADDR}"
export LYCAON_API_TOKEN="${TOKEN}"
export LYCAON_LOG_LEVEL="${LYCAON_LOG_LEVEL:-warn}"

printf '%s' "${TOKEN}" >"${CONFIG_DIR}/api.token"
chmod 600 "${CONFIG_DIR}/api.token"

SIDECAR_LOG="${WORKDIR}/sidecar.log"
(
  cd "${GO_DIR}"
  exec "${SIDECAR}" serve
) >"${SIDECAR_LOG}" 2>&1 &
UPGRADE_CHILD_PID=$!

BASE="http://${ADDR}"
AUTH_HDR=( -H "Authorization: Bearer ${TOKEN}" -H "Content-Type: application/json" )

wait_health() {
  local deadline=$((SECONDS + 60))
  local body status
  while (( SECONDS < deadline )); do
    if body="$(curl -sf --max-time 2 "${BASE}/health" 2>/dev/null)"; then
      status="$(jq -r '.status // empty' <<<"${body}")"
      if [[ "${status}" == "ok" ]]; then
        echo "${body}"
        return 0
      fi
    fi
    if ! kill -0 "${UPGRADE_CHILD_PID}" 2>/dev/null; then
      echo "error: sidecar exited before healthy; log:" >&2
      cat "${SIDECAR_LOG}" >&2 || true
      return 1
    fi
    sleep 0.25
  done
  echo "error: health timeout (60s); log:" >&2
  cat "${SIDECAR_LOG}" >&2 || true
  return 1
}

HEALTH_JSON="$(wait_health)"
APP_VERSION="$(jq -r '.version // empty' <<<"${HEALTH_JSON}")"
SCHEMA_VERSION="$(jq -r '.schema_version // empty' <<<"${HEALTH_JSON}")"
if [[ -z "${APP_VERSION}" || -z "${SCHEMA_VERSION}" ]]; then
  echo "error: health missing version/schema_version: ${HEALTH_JSON}" >&2
  exit 1
fi

PROJECT_JSON="$(curl -sf "${AUTH_HDR[@]}" \
  -d "$(jq -n --arg path "${SEED_PROJECT_ROOT}" '{name:"corpus-project",roots:[{path:$path}]}')" \
  "${BASE}/v1/projects")"
PROJECT_ID="$(jq -r '.id // empty' <<<"${PROJECT_JSON}")"
if [[ -z "${PROJECT_ID}" ]]; then
  echo "error: create project failed: ${PROJECT_JSON}" >&2
  exit 1
fi

SESSION_JSON="$(curl -sf "${AUTH_HDR[@]}" \
  -d "$(jq -n --arg pid "${PROJECT_ID}" '{project_id:$pid,posture:"build"}')" \
  "${BASE}/v1/sessions")"
SESSION_ID="$(jq -r '.id // empty' <<<"${SESSION_JSON}")"
if [[ -z "${SESSION_ID}" ]]; then
  echo "error: create session failed: ${SESSION_JSON}" >&2
  exit 1
fi

# A separate parent keeps the completed worker out of the mock transcript turn.
WORKER_SESSION_ID="$(curl -fsS "${AUTH_HDR[@]}" \
  -d "$(jq -n --arg pid "${PROJECT_ID}" '{project_id:$pid,posture:"build"}')" \
  "${BASE}/v1/sessions" | jq -er '.id')"
WORKER_JSON="$(curl -fsS "${AUTH_HDR[@]}" \
  -d "$(jq -n --arg id "${WORKER_SESSION_ID}" '{session_id:$id,setup:{overlays:[{label:"Retained worker",files:{"README.md":"# retained worker overlay\n","worker-note.txt":"Retained worker addition.\n"}}]}}')" \
  "${BASE}/harness/overlays")"
WORKER_ID="$(jq -er '.overlays[0].job_id' <<<"${WORKER_JSON}")"

# Retry session_preparing while the workspace initializes.
PROMPT_OPERATION_ID="$(python3 -c 'import uuid; print(uuid.uuid4())')"
PROMPT_RESPONSE="${WORKDIR}/prompt-response.json"
prompt_deadline=$((SECONDS + 60))
while :; do
  prompt_status="$(curl -s --output "${PROMPT_RESPONSE}" --write-out '%{http_code}' \
    "${AUTH_HDR[@]}" \
    -d "$(jq -n --arg op "${PROMPT_OPERATION_ID}" '{operation_id:$op, text:"corpus seed"}')" \
    "${BASE}/v1/sessions/${SESSION_ID}/prompts")"
  [[ "${prompt_status}" == 202 ]] && break
  prompt_code="$(jq -r '.code // empty' "${PROMPT_RESPONSE}" 2>/dev/null || true)"
  if [[ "${prompt_status}" == 409 && "${prompt_code}" == "session_preparing" ]] \
    && (( SECONDS < prompt_deadline )); then
    sleep 0.5
    continue
  fi
  echo "error: prompt submission failed (HTTP ${prompt_status}): $(cat "${PROMPT_RESPONSE}")" >&2
  exit 1
done

TOOL_RESULT_ID=""
poll_deadline=$((SECONDS + 120))
while (( SECONDS < poll_deadline )); do
  MSGS="$(curl -sf "${AUTH_HDR[@]}" "${BASE}/v1/sessions/${SESSION_ID}/messages")"
  TOOL_RESULT_ID="$(jq -r '.messages[]? | select(.role=="tool") | .id' <<<"${MSGS}" | head -n1)"
  if [[ -n "${TOOL_RESULT_ID}" && "${TOOL_RESULT_ID}" != "null" ]]; then
    break
  fi
  sleep 0.5
done
if [[ -z "${TOOL_RESULT_ID}" || "${TOOL_RESULT_ID}" == "null" ]]; then
  echo "error: no tool-role result within 120s — seed recipe incomplete" >&2
  echo "last messages: ${MSGS:-}" >&2
  exit 1
fi

SOURCE_JSON="$(curl -fsS "${AUTH_HDR[@]}" \
  -d "$(jq -n --arg id "${SESSION_ID}" '{session_id:$id}')" \
  "${BASE}/harness/upgrade-history")"
ARTIFACT_JSON="$(curl -fsS "${AUTH_HDR[@]}" \
  -d "$(jq -n --arg id "${SESSION_ID}" '{session_id:$id,caption:"Retained upgrade fixture"}')" \
  "${BASE}/harness/visual_fixture")"
ARTIFACT_ID="$(jq -er '.artifact_id' <<<"${ARTIFACT_JSON}")"
GRANT_JSON="$(curl -fsS "${AUTH_HDR[@]}" \
  -d '{"scope":"device","category":"host","host_pattern":"fixture.example.com","expires_at":"2099-01-01T00:00:00Z"}' "${BASE}/v1/approval-grants")"

# A nondefault preference makes lost application state detectable.
APP_STATE_PATH="${CONFIG_DIR}/app-state-v1"
mkdir -p "${APP_STATE_PATH}"
printf '%s\n' '{"key":"debug","value":{"verboseMode":true}}' >"${APP_STATE_PATH}/6465627567.json"
chmod 700 "${APP_STATE_PATH}"
chmod 600 "${APP_STATE_PATH}/6465627567.json"


# Capture real reader output and a complete durable archive while the source version runs.
jq -n --arg project_id "${PROJECT_ID}" --arg session_id "${SESSION_ID}" \
  --arg tool_result_message_id "${TOOL_RESULT_ID}" --arg artifact_id "${ARTIFACT_ID}" \
  --argjson source_history "${SOURCE_JSON}" --argjson grant "${GRANT_JSON}" --arg worker_id "${WORKER_ID}" \
  --arg worker_parent "${WORKER_SESSION_ID}" --argjson worker "${WORKER_JSON}" \
  '{project_id:$project_id,session_id:$session_id,tool_result_message_id:$tool_result_message_id,artifact_id:$artifact_id,source_history:$source_history,grant_id:$grant.id,worker_history:{job_id:$worker_id,parent_session_id:$worker_parent,child_session_id:$worker.overlays[0].child_session_id}}' >"${STAGE_DIR}/MANIFEST.json"
python3 - "${STAGE_DIR}/MANIFEST.json" <<'PYCODE'
import hashlib, json, sys
from pathlib import Path
path = Path(sys.argv[1])
manifest = json.loads(path.read_text())
sha = lambda text: hashlib.sha256(text.encode()).hexdigest()
manifest['worker_history']['baseline'] = {'README.md': sha('# corpus seed\n')}
manifest['worker_history']['overlay'] = {'README.md': sha('# retained worker overlay\n'), 'worker-note.txt': sha('Retained worker addition.\n')}
path.write_text(json.dumps(manifest, indent=2) + '\n')
PYCODE
node "${ROOT}/scripts/upgrade-editor-fixture.mjs" seed "${BASE}" "${CONFIG_DIR}" "${STAGE_DIR}/MANIFEST.json" "${SEED_PROJECT_ROOT}"
curl -fsS "${AUTH_HDR[@]}" -X POST "${BASE}/v1/projects/${PROJECT_ID}/trust/review" \
  | jq -e '.review.changes | length > 0' >/dev/null
printf '# Retained configuration change.\nformat: 1\ndisabled: []\n' >"${SEED_PROJECT_ROOT}/${OVERLAY_DIR}/extensions.yaml"
curl -fsS "${AUTH_HDR[@]}" -X POST "${BASE}/v1/projects/${PROJECT_ID}/trust/review" \
  | jq -e '.review.changes | any(.kind == "modified" and (.before | length > 0))' >/dev/null
python3 "${ROOT}/scripts/upgrade-corpus-semantics.py" capture --base "${BASE}" \
  --token-file "${CONFIG_DIR}/api.token" --fixture "${STAGE_DIR}" --config-dir "${CONFIG_DIR}"
curl -fsS --retry 5 --retry-all-errors --retry-delay 1 "${AUTH_HDR[@]}" \
  "${BASE}/v1/backup" -o "${STAGE_DIR}/backup.zip"
upgrade_stop_owned_child
"${SIDECAR}" diagnostics upgrade-fixture-verify "${CONFIG_DIR}" "${STAGE_DIR}/MANIFEST.json"

DB_PATH="${CONFIG_DIR}/store.db"
if [[ ! -f "${DB_PATH}" ]]; then
  echo "error: store.db missing after seed" >&2
  exit 1
fi

# The fixture database contains committed pages without external journals.
sqlite3 "${DB_PATH}" 'PRAGMA wal_checkpoint(TRUNCATE); PRAGMA journal_mode=DELETE;' >/dev/null
rm -f "${DB_PATH}-wal" "${DB_PATH}-shm"

# Fixtures exclude device credentials.
for rel in "${CREDENTIAL_RELS[@]}"; do
  rm -f "${CONFIG_DIR}/${rel}"
done

# Text fixtures are checked for secret material.
if find "${CONFIG_DIR}" -type f ! -name 'store.db' ! -name 'store.db-*' -print0 \
  | xargs -0 grep -E -I -q \
    -e 'sk-[A-Za-z0-9]{10,}' \
    -e 'AKIA[0-9A-Z]{16}' \
    -e 'ghp_[A-Za-z0-9]{20,}' \
    -e 'BEGIN (RSA |OPENSSH )?PRIVATE KEY' \
    -e 'OPENAI_API_KEY[[:space:]]*[:=]' \
    2>/dev/null; then
  echo "error: secret pattern remains after credential scrub — refusing to write corpus" >&2
  exit 1
fi

# Rewrite absolute seed project path → relative project-root/ layout.
sqlite3 "${DB_PATH}" "UPDATE project_roots SET path = 'project-root' WHERE path = '${SEED_PROJECT_ROOT//\'/\'\'}';"
if sqlite3 "${DB_PATH}" "SELECT path FROM project_roots;" | grep -F "${SEED_PROJECT_ROOT}" >/dev/null; then
  echo "error: project_roots still reference seed absolute path" >&2
  exit 1
fi

mkdir -p "${STAGE_DIR}/project-root"
python3 - "${STAGE_DIR}" <<'PYCODE'
from pathlib import Path
import shutil
import sys
import zipfile
stage = Path(sys.argv[1])
with zipfile.ZipFile(stage / 'backup.zip') as archive, archive.open('store.db') as source, (stage / 'store.db').open('wb') as output:
    shutil.copyfileobj(source, output)
PYCODE
# A library connection closes without leaving the persistent WAL the sqlite3 CLI keeps beside the store.
python3 - "${STAGE_DIR}/store.db" "${SEED_PROJECT_ROOT}" <<'PYCODE'
import sqlite3
import sys
connection = sqlite3.connect(sys.argv[1])
connection.execute("UPDATE project_roots SET path = 'project-root' WHERE path = ?", (sys.argv[2],))
connection.commit()
connection.close()
PYCODE
cp -a "${APP_STATE_PATH}" "${STAGE_DIR}/app-state-v1"
cp -a "${SEED_PROJECT_ROOT}/." "${STAGE_DIR}/project-root/"
# Boot restores the overlay directory name from this fixture placeholder.
mv "${STAGE_DIR}/project-root/${OVERLAY_DIR}" "${STAGE_DIR}/project-root/${OVERLAY_FIXTURE_DIR}"

if [[ -d "${CONFIG_DIR}/extensions-cache" ]] && [[ -n "$(ls -A "${CONFIG_DIR}/extensions-cache" 2>/dev/null || true)" ]]; then
  cp -a "${CONFIG_DIR}/extensions-cache" "${STAGE_DIR}/extensions-cache"
fi

CREATED_AT="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
jq -n \
  --arg recipe "retained-history-v1" \
  --slurpfile evidence "${STAGE_DIR}/MANIFEST.json" \
  --arg app_version "${APP_VERSION}" \
  --argjson schema_version "${SCHEMA_VERSION}" \
  --arg project_id "${PROJECT_ID}" \
  --arg session_id "${SESSION_ID}" \
  --arg tool_result_message_id "${TOOL_RESULT_ID}" \
  --arg seeded_secondary "tool_result" \
  --arg created_at "${CREATED_AT}" \
  '{
    recipe: $recipe,
    app_version: $app_version,
    schema_version: $schema_version,
    project_id: $project_id,
    session_id: $session_id,
    tool_result_message_id: $tool_result_message_id,
    seeded_secondary: $seeded_secondary,
    created_at: $created_at
  } + $evidence[0]' >"${STAGE_DIR}/manifest-final.json"
mv "${STAGE_DIR}/manifest-final.json" "${STAGE_DIR}/MANIFEST.json"
python3 - "${STAGE_DIR}" "${SIDECAR}" <<'PYCODE'
import hashlib
import json
from pathlib import Path
import subprocess
import sys
stage = Path(sys.argv[1])
path = stage / 'MANIFEST.json'
manifest = json.loads(path.read_text())
manifest['schema_identity'] = json.loads(subprocess.check_output([sys.argv[2], 'diagnostics', 'schema-baseline']))
for filename, key in [('store.db', 'store_sha256'), ('backup.zip', 'backup_sha256'), ('SEMANTICS.json', 'semantics_sha256')]:
    with (stage / filename).open('rb') as source:
        value = hashlib.sha256()
        for block in iter(lambda: source.read(1024 * 1024), b''):
            value.update(block)
        manifest[key] = value.hexdigest()
path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + '\n')
PYCODE

[[ -f "${STAGE_DIR}/MANIFEST.json" ]]
[[ -f "${STAGE_DIR}/store.db" ]]
[[ -d "${STAGE_DIR}/app-state-v1" ]]
[[ -d "${STAGE_DIR}/project-root/${OVERLAY_FIXTURE_DIR}" ]]
[[ ! -e "${STAGE_DIR}/store.db-wal" ]]
[[ ! -e "${STAGE_DIR}/store.db-shm" ]]
for rel in "${CREDENTIAL_RELS[@]}"; do
  [[ ! -e "${STAGE_DIR}/${rel}" ]]
done

mkdir -p "$(dirname "${OUT}")"
mv "${STAGE_DIR}" "${OUT}"
echo "upgrade-corpus:seed wrote ${OUT} (schema=${SCHEMA_VERSION} app=${APP_VERSION})" >&2
