#!/usr/bin/env bash
# Foreground Go sidecar for local Den dev — logs on stderr (terminal 1).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GO_DIR="${ROOT}/lycaon"
SCRIPTS="${ROOT}/scripts"
# shellcheck source=scripts/artifact-paths.sh
source "${SCRIPTS}/artifact-paths.sh"
LYCAON_DEV_BIN="${LYCAON_DEV_BIN:-${PW_BUILD_DIR}/lycaon-dev}"

# shellcheck source=scripts/config-dir.sh
source "${SCRIPTS}/config-dir.sh"
# shellcheck source=scripts/debug-common.sh
source "${SCRIPTS}/debug-common.sh"
# shellcheck source=scripts/listen-port.sh
source "${SCRIPTS}/listen-port.sh"

export LYCAON_DEV="${LYCAON_DEV:-1}"
export LYCAON_ADDR="${LYCAON_ADDR:-127.0.0.1:8787}"
export LYCAON_API_TOKEN="${LYCAON_API_TOKEN:-dev-token-change-me}"
export LYCAON_LLM_MOCK="${LYCAON_LLM_MOCK:-0}"
export LYCAON_LLM_DEBUG="${LYCAON_LLM_DEBUG:-0}"
export LYCAON_LOG_LEVEL="${LYCAON_LOG_LEVEL:-info}"
export LYCAON_DB_FRESH="${LYCAON_DB_FRESH:-0}"
export LYCAON_ENGINE_ROOT="${LYCAON_ENGINE_ROOT:-${ROOT}/lycaon-den/src-tauri/engine-root}"
# Validate and stage the pinned decision release before boot.
bash "${SCRIPTS}/stage-decide-heads.sh" "${LYCAON_ENGINE_ROOT}"

export LYCAON_CONFIG_DIR="${LYCAON_CONFIG_DIR:-$(lycaon_config_dir)}"
CONFIG_DIR="${LYCAON_CONFIG_DIR}"
DB_PATH="${LYCAON_DB_PATH:-${CONFIG_DIR}/store.db}"
mkdir -p "${CONFIG_DIR}"
printf '%s' "${LYCAON_API_TOKEN}" >"${CONFIG_DIR}/api.token"
chmod 600 "${CONFIG_DIR}/api.token"

llm_debug_on=false
if debug_env_on "${LYCAON_LLM_DEBUG}"; then
  llm_debug_on=true
fi

http_debug_on=false
if debug_env_on "${LYCAON_HTTP_DEBUG:-0}"; then
  http_debug_on=true
fi

sse_debug_on=false
if debug_env_on "${LYCAON_SSE_DEBUG:-0}"; then
  sse_debug_on=true
fi

tool_debug_on=false
if debug_env_on "${LYCAON_TOOL_DEBUG:-0}"; then
  tool_debug_on=true
fi

den_perf_debug_on=false
if debug_env_on "${LYCAON_DEN_PERF_DEBUG:-0}"; then
  den_perf_debug_on=true
fi

perf_debug_on=false
if debug_env_on "${LYCAON_PERF_DEBUG:-0}"; then
  perf_debug_on=true
fi

debug_session_on=false
if debug_env_on "${LYCAON_DEBUG_SESSION:-0}"; then
  debug_session_on=true
  # shellcheck source=scripts/debug-session.sh
  source "${SCRIPTS}/debug-session.sh"
fi

resolve_capture_path() {
  local kind="$1"
  local file_var="${2}"
  if [[ -n "${!file_var:-}" ]]; then
    printf '%s\n' "${!file_var}"
    return
  fi
  bash "${SCRIPTS}/debug-capture.sh" path "${kind}"
}

if debug_env_on "${LYCAON_DB_FRESH}"; then
  bash "${SCRIPTS}/db-wipe.sh"
fi

if [[ ! -x "${LYCAON_DEV_BIN}" ]]; then
  echo "error: ${LYCAON_DEV_BIN} not found — run ./task build:lycaon-dev from repo root" >&2
  exit 1
fi

_sidecar_port="${LYCAON_ADDR##*:}"
if [[ "${_sidecar_port}" != "0" ]]; then
  ensure_engine_listen_port "${LYCAON_ADDR}" is_engine_pid "den:sidecar" "set LYCAON_ADDR"
fi

echo "den:sidecar — backend logs below; Den shell: ./task den:dev (other terminal)" >&2
echo "  binary: ${LYCAON_DEV_BIN}" >&2
echo "  health: http://${LYCAON_ADDR}/health  token: ${CONFIG_DIR}/api.token" >&2
echo "  mock LLM: LYCAON_LLM_MOCK=${LYCAON_LLM_MOCK}  log level: LYCAON_LOG_LEVEL=${LYCAON_LOG_LEVEL}" >&2
if debug_env_on "${LYCAON_DB_FRESH}"; then
  echo "  sqlite fresh: LYCAON_DB_FRESH=1 (wiped ${DB_PATH}; serve re-wipes if file reappears)" >&2
else
  echo "  sqlite db:     ${DB_PATH}  (fresh: ./task den:sidecar:fresh or ./task db:wipe)" >&2
fi
if [[ "${debug_session_on}" == true ]]; then
  echo "  debug session: ${LYCAON_DEBUG_SESSION_DIR}" >&2
  echo "  sidecar log:   ${LYCAON_LOG_FILE}" >&2
  echo "  tail sidecar:  ./task debug:tail (other terminal)" >&2
  echo "  clear logs:    ./task debug:clear-all (before a fresh capture)" >&2
fi
if [[ "${llm_debug_on}" == true ]]; then
  echo "  llm debug log: $(resolve_capture_path llm LYCAON_LLM_DEBUG_FILE)" >&2
  echo "  tail llm:      ./task llm:debug:tail (other terminal)" >&2
fi
if [[ "${http_debug_on}" == true ]]; then
  echo "  http debug log: $(resolve_capture_path http LYCAON_HTTP_DEBUG_FILE)" >&2
  echo "  tail http:      ./task http:debug:tail (other terminal)" >&2
fi
if [[ "${sse_debug_on}" == true ]]; then
  echo "  sse debug log:  $(resolve_capture_path sse LYCAON_SSE_DEBUG_FILE)" >&2
  echo "  tail sse:       ./task sse:debug:tail (other terminal)" >&2
fi
if [[ "${tool_debug_on}" == true ]]; then
  echo "  tool debug log: $(resolve_capture_path tool LYCAON_TOOL_DEBUG_FILE)" >&2
  echo "  tail tool:      ./task tool:debug:tail (other terminal)" >&2
fi
if [[ "${den_perf_debug_on}" == true ]]; then
  echo "  den perf log:  $(resolve_capture_path den-perf LYCAON_DEN_PERF_DEBUG_FILE)" >&2
  echo "  tail den-perf: ./task den-perf:debug:tail (other terminal)" >&2
fi
if [[ "${perf_debug_on}" == true ]]; then
  echo "  sidecar perf:  $(resolve_capture_path perf LYCAON_PERF_DEBUG_FILE)" >&2
  echo "  tail perf:     ./task perf:debug:tail (other terminal)" >&2
fi
echo >&2

cd "${GO_DIR}"
exec "${LYCAON_DEV_BIN}" serve "$@"
