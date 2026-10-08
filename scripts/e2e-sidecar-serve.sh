#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/e2e/env.sh
source "${DIR}/e2e/env.sh"

STATE_DIR="${LYCAON_E2E_STATE_DIR:-}"
if [[ -z "${STATE_DIR}" ]]; then
  STATE_DIR="$(mktemp -d -t lycaon-e2e-state.XXXXXX)"
  export LYCAON_E2E_STATE_DIR="${STATE_DIR}"
fi
mkdir -p "${STATE_DIR}"
DB_PATH="${STATE_DIR}/lycaon.db"

SOURCE_CONFIG="${LYCAON_CONFIG_DIR:-}"
# Scripted and mocked runs need a provider discovery can answer, not this machine's settings.
case "${LYCAON_HARNESS_MODE:-}" in
  manual|mock)
    SOURCE_CONFIG="${ROOT}/lycaon/test/fixtures/e2e/config"
    MODEL_FIXTURE=1
    ;;
  "")
    if [[ -z "${SOURCE_CONFIG}" && "${LYCAON_LLM_MOCK:-1}" == "1" ]]; then
      SOURCE_CONFIG="${ROOT}/lycaon/test/fixtures/e2e/config"
      MODEL_FIXTURE=1
    fi
    ;;
esac
if [[ -z "${SOURCE_CONFIG}" ]]; then
  # shellcheck source=scripts/config-dir.sh
  source "${DIR}/config-dir.sh"
  export LYCAON_DEV="${LYCAON_DEV:-1}"
  SOURCE_CONFIG="$(lycaon_channel_config_dir)"
fi
CONFIG_DIR="${STATE_DIR}/config"
mkdir -p "${CONFIG_DIR}"
for f in providers.local.yaml model-policy.yaml; do
  [[ -f "${SOURCE_CONFIG}/${f}" && ! -f "${CONFIG_DIR}/${f}" ]] && cp "${SOURCE_CONFIG}/${f}" "${CONFIG_DIR}/${f}"
done
# Model discovery needs a live endpoint even when completions are mocked.
if [[ "${MODEL_FIXTURE:-0}" == "1" ]]; then
  FIXTURE_PORT_FILE="${STATE_DIR}/model-fixture.port"
  FIXTURE_PID_FILE="${STATE_DIR}/model-fixture.pid"
  if ! { [[ -f "${FIXTURE_PID_FILE}" ]] && kill -0 "$(cat "${FIXTURE_PID_FILE}")" 2>/dev/null; }; then
    rm -f "${FIXTURE_PORT_FILE}"
    MODEL_FIXTURE_PORT_FILE="${FIXTURE_PORT_FILE}" bun "${DIR}/e2e/model-fixture.ts" &
    echo "$!" >"${FIXTURE_PID_FILE}"
    for _ in {1..100}; do
      [[ -s "${FIXTURE_PORT_FILE}" ]] && break
      sleep 0.1
    done
    [[ -s "${FIXTURE_PORT_FILE}" ]] || { echo "error: E2E model fixture did not start" >&2; exit 1; }
  fi
  FIXTURE_PORT="$(tr -d '[:space:]' <"${FIXTURE_PORT_FILE}")"
  if [[ -f "${CONFIG_DIR}/providers.local.yaml" ]]; then
    sed -i.bak "s#^\(    base_url:\).*#\1 http://127.0.0.1:${FIXTURE_PORT}#" "${CONFIG_DIR}/providers.local.yaml"
    rm -f "${CONFIG_DIR}/providers.local.yaml.bak"
  fi
fi
if [[ "${LYCAON_LLM_MOCK:-1}" != "1" ]]; then
  for f in credential-vault.age .credential-vault-development-identity; do
    [[ -f "${SOURCE_CONFIG}/${f}" && ! -f "${CONFIG_DIR}/${f}" ]] && cp "${SOURCE_CONFIG}/${f}" "${CONFIG_DIR}/${f}"
  done
fi
export LYCAON_CONFIG_DIR="${CONFIG_DIR}"

export LYCAON_ADDR="${LYCAON_E2E_ADDR}"
export LYCAON_API_TOKEN="${LYCAON_E2E_TOKEN}"
export LYCAON_LLM_MOCK="${LYCAON_LLM_MOCK:-1}"
export LYCAON_LLM_MANUAL="${LYCAON_LLM_MANUAL:-0}"
export LYCAON_DEV=1
export LYCAON_HARNESS=1
export LYCAON_DEV_CORS=1
# Specs seed several chats each; repeat runs would otherwise hit the 10/min product default.
export LYCAON_RATE_SESSIONS_PER_MIN="${LYCAON_RATE_SESSIONS_PER_MIN:-600}"
export LYCAON_ENGINE_ROOT="${LYCAON_ENGINE_ROOT:-${ROOT}/lycaon-den/src-tauri/engine-root}"

BIN="${STATE_DIR}/runtime/sidecar"
echo "e2e-sidecar: ${LYCAON_E2E_API_URL} db=${DB_PATH}" >&2
cd "${ROOT}"
exec "${BIN}" serve --db "${DB_PATH}"
