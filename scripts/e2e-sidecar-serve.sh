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
case "${LYCAON_HARNESS_MODE:-}" in
  mock|manual)
    SOURCE_CONFIG="${DIR}/harness/device-config"
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
export LYCAON_ENGINE_ROOT="${LYCAON_ENGINE_ROOT:-${ROOT}/lycaon-den/src-tauri/engine-root}"

BIN="${STATE_DIR}/runtime/sidecar"
echo "e2e-sidecar: ${LYCAON_E2E_API_URL} db=${DB_PATH}" >&2
cd "${ROOT}"
exec "${BIN}" serve --db "${DB_PATH}"
