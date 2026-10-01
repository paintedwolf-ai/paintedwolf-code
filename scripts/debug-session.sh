#!/usr/bin/env bash
# Create a timestamped dev capture session dir and export log file paths.
# Sourced by den-dev-sidecar.sh when LYCAON_DEBUG_SESSION=1.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/debug-common.sh
source "${ROOT}/scripts/debug-common.sh"

export LYCAON_DEV="${LYCAON_DEV:-1}"
CONFIG_DIR="$(lycaon_config_dir)"
DEBUG_ROOT="${CONFIG_DIR}/debug"
SESSIONS_DIR="${DEBUG_ROOT}/sessions"

if [[ -n "${LYCAON_DEBUG_SESSION_DIR:-}" ]]; then
  SESSION_DIR="${LYCAON_DEBUG_SESSION_DIR}"
else
  STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
  SESSION_DIR="${SESSIONS_DIR}/${STAMP}"
fi

mkdir -p "${SESSION_DIR}"
ln -sfn "${SESSION_DIR}" "${DEBUG_ROOT}/latest"

export LYCAON_DEBUG_SESSION_DIR="${SESSION_DIR}"
export LYCAON_LOG_FILE="${LYCAON_LOG_FILE:-${SESSION_DIR}/sidecar.log}"

if debug_env_on "${LYCAON_LLM_DEBUG:-0}"; then
  export LYCAON_LLM_DEBUG_FILE="${LYCAON_LLM_DEBUG_FILE:-${SESSION_DIR}/llm-requests.jsonl}"
fi

if debug_env_on "${LYCAON_HTTP_DEBUG:-0}"; then
  export LYCAON_HTTP_DEBUG_FILE="${LYCAON_HTTP_DEBUG_FILE:-${SESSION_DIR}/http-requests.jsonl}"
fi

if debug_env_on "${LYCAON_SSE_DEBUG:-0}"; then
  export LYCAON_SSE_DEBUG_FILE="${LYCAON_SSE_DEBUG_FILE:-${SESSION_DIR}/sse-events.jsonl}"
fi

if debug_env_on "${LYCAON_TOOL_DEBUG:-0}"; then
  export LYCAON_TOOL_DEBUG_FILE="${LYCAON_TOOL_DEBUG_FILE:-${SESSION_DIR}/tool-invocations.jsonl}"
fi

if debug_env_on "${LYCAON_DEN_PERF_DEBUG:-0}"; then
  export LYCAON_DEN_PERF_DEBUG_FILE="${LYCAON_DEN_PERF_DEBUG_FILE:-${SESSION_DIR}/den-perf.jsonl}"
fi

if debug_env_on "${LYCAON_PERF_DEBUG:-0}"; then
  export LYCAON_PERF_DEBUG_FILE="${LYCAON_PERF_DEBUG_FILE:-${SESSION_DIR}/performance.jsonl}"
fi

for file in "${LYCAON_LLM_DEBUG_FILE:-}" "${LYCAON_HTTP_DEBUG_FILE:-}" "${LYCAON_SSE_DEBUG_FILE:-}" "${LYCAON_TOOL_DEBUG_FILE:-}" "${LYCAON_DEN_PERF_DEBUG_FILE:-}" "${LYCAON_PERF_DEBUG_FILE:-}"; do
  if [[ -n "${file}" ]]; then
    mkdir -p "$(dirname "${file}")"
    touch "${file}"
  fi
done

{
  printf 'LYCAON_DEBUG_SESSION_DIR=%q\n' "${LYCAON_DEBUG_SESSION_DIR}"
  printf 'LYCAON_LOG_FILE=%q\n' "${LYCAON_LOG_FILE}"
  if [[ -n "${LYCAON_LLM_DEBUG_FILE:-}" ]]; then
    printf 'LYCAON_LLM_DEBUG_FILE=%q\n' "${LYCAON_LLM_DEBUG_FILE}"
  fi
  if [[ -n "${LYCAON_HTTP_DEBUG_FILE:-}" ]]; then
    printf 'LYCAON_HTTP_DEBUG_FILE=%q\n' "${LYCAON_HTTP_DEBUG_FILE}"
  fi
  if [[ -n "${LYCAON_SSE_DEBUG_FILE:-}" ]]; then
    printf 'LYCAON_SSE_DEBUG_FILE=%q\n' "${LYCAON_SSE_DEBUG_FILE}"
  fi
  if [[ -n "${LYCAON_TOOL_DEBUG_FILE:-}" ]]; then
    printf 'LYCAON_TOOL_DEBUG_FILE=%q\n' "${LYCAON_TOOL_DEBUG_FILE}"
  fi
  if [[ -n "${LYCAON_DEN_PERF_DEBUG_FILE:-}" ]]; then
    printf 'LYCAON_DEN_PERF_DEBUG_FILE=%q\n' "${LYCAON_DEN_PERF_DEBUG_FILE}"
  fi
  if [[ -n "${LYCAON_PERF_DEBUG_FILE:-}" ]]; then
    printf 'LYCAON_PERF_DEBUG_FILE=%q\n' "${LYCAON_PERF_DEBUG_FILE}"
  fi
} >"${SESSION_DIR}/session.env"
