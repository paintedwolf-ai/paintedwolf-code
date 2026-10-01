#!/usr/bin/env bash
# Shared helpers for dev capture scripts.
set -euo pipefail

_DEBUG_COMMON_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/config-dir.sh
source "${_DEBUG_COMMON_ROOT}/scripts/config-dir.sh"

debug_env_on() {
  local v="${1:-}"
  [[ "${v}" == "1" || "${v}" == "true" || "${v}" == "TRUE" ]]
}

load_debug_session_env() {
  export LYCAON_DEV="${LYCAON_DEV:-1}"
  local session_env
  session_env="$(lycaon_config_dir)/debug/latest/session.env"
  if [[ -f "${session_env}" ]]; then
    # shellcheck disable=SC1090
    source "${session_env}"
  fi
}
