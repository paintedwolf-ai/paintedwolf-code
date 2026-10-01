#!/usr/bin/env bash
# Dev capture log helpers — path | tail | clear | clear-all <log|llm|http|sse>
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/debug-common.sh
source "${ROOT}/scripts/debug-common.sh"

usage() {
  echo "usage: debug-capture.sh path|tail|clear|clear-all <log|llm|http|sse|tool|den-perf|perf>" >&2
  exit 1
}

capture_file_var() {
  case "$1" in
    log) printf '%s\n' LYCAON_LOG_FILE ;;
    llm) printf '%s\n' LYCAON_LLM_DEBUG_FILE ;;
    http) printf '%s\n' LYCAON_HTTP_DEBUG_FILE ;;
    sse) printf '%s\n' LYCAON_SSE_DEBUG_FILE ;;
    tool) printf '%s\n' LYCAON_TOOL_DEBUG_FILE ;;
    den-perf) printf '%s\n' LYCAON_DEN_PERF_DEBUG_FILE ;;
    perf) printf '%s\n' LYCAON_PERF_DEBUG_FILE ;;
    *) usage ;;
  esac
}

capture_default_path() {
  export LYCAON_DEV="${LYCAON_DEV:-1}"
  local base
  base="$(lycaon_config_dir)"
  case "$1" in
    log) printf '%s\n' "${base}/debug/latest/sidecar.log" ;;
    llm) printf '%s\n' "${base}/llm-requests.jsonl" ;;
    http) printf '%s\n' "${base}/http-requests.jsonl" ;;
    sse) printf '%s\n' "${base}/sse-events.jsonl" ;;
    tool) printf '%s\n' "${base}/tool-invocations.jsonl" ;;
    den-perf) printf '%s\n' "${base}/den-perf.jsonl" ;;
    perf) printf '%s\n' "${base}/performance.jsonl" ;;
    *) usage ;;
  esac
}

capture_path() {
  local kind="$1"
  local file_var
  file_var="$(capture_file_var "${kind}")"
  local file_path="${!file_var:-}"

  if [[ -n "${file_path}" ]]; then
    printf '%s\n' "${file_path}"
    return 0
  fi

  load_debug_session_env
  file_path="${!file_var:-}"
  if [[ -n "${file_path}" ]]; then
    printf '%s\n' "${file_path}"
    return 0
  fi

  if [[ "${kind}" == "log" ]]; then
    echo "sidecar log not found — start ./task den:sidecar:full-debug first" >&2
    exit 1
  fi

  capture_default_path "${kind}"
}

cmd_path() {
  capture_path "$1"
}

cmd_tail() {
  local kind="$1"
  load_debug_session_env
  local log
  log="$(capture_path "${kind}")"
  mkdir -p "$(dirname "${log}")"
  touch "${log}"
  echo "Following ${log} (Ctrl-C to stop)" >&2
  exec tail -f "${log}"
}

cmd_clear() {
  local kind="$1"
  load_debug_session_env
  local log
  log="$(capture_path "${kind}")"
  mkdir -p "$(dirname "${log}")"
  : >"${log}"
  echo "Cleared ${log}" >&2
}

cmd_clear_all() {
  local kind log
  for kind in log llm http sse tool den-perf perf; do
    load_debug_session_env
    if ! log="$(capture_path "${kind}" 2>/dev/null)"; then
      continue
    fi
    mkdir -p "$(dirname "${log}")"
    : >"${log}"
    echo "Cleared ${log}" >&2
  done
}

main() {
  local cmd="${1:-}"
  local kind="${2:-}"

  case "${cmd}" in
    path)
      [[ -n "${kind}" ]] || usage
      cmd_path "${kind}"
      ;;
    tail)
      [[ -n "${kind}" ]] || usage
      cmd_tail "${kind}"
      ;;
    clear)
      [[ -n "${kind}" ]] || usage
      cmd_clear "${kind}"
      ;;
    clear-all)
      cmd_clear_all
      ;;
    *)
      usage
      ;;
  esac
}

main "$@"
