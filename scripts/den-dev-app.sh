#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEN_DIR="${ROOT}/lycaon-den"
DEFAULT_BASE="http://127.0.0.1:8787"
HEALTH_URL="${LYCAON_HEALTH_URL:-${DEFAULT_BASE}/health}"
WAIT_SECS="${LYCAON_HEALTH_WAIT_SECS:-2}"

if ! command -v bun >/dev/null 2>&1; then
  echo "error: bun required — https://bun.sh" >&2
  exit 1
fi
if ! command -v cargo >/dev/null 2>&1 || ! command -v rustc >/dev/null 2>&1; then
	echo "error: Rust with cargo is required — install rustup from https://rustup.rs; rust-toolchain.toml selects the version" >&2
	exit 1
fi
if [[ ! -d "${DEN_DIR}/node_modules" ]]; then
	echo "error: Den dependencies are missing — run ./task setup-dev" >&2
	exit 1
fi

# shellcheck source=scripts/listen-port.sh
source "${ROOT}/scripts/listen-port.sh"
# Port 1420 is reserved for the development UI.
reclaim_listen_port 1420 is_den_vite_pid "den:dev" "stop that listener"

if ! curl -sf --max-time "${WAIT_SECS}" "${HEALTH_URL}" >/dev/null 2>&1; then
  cat >&2 <<EOF
error: sidecar not reachable at ${HEALTH_URL}

Start the backend in another terminal (logs stay there):

  ./task den:sidecar

Then run this task again.
EOF
  exit 1
fi

bash "${ROOT}/scripts/den-fresh-session-state.sh" consume

# shellcheck source=scripts/config-dir.sh
source "${ROOT}/scripts/config-dir.sh"

bash "${ROOT}/scripts/ensure-tauri-binaries.sh"

export LYCAON_ATTACH_ONLY=1
export LYCAON_DEV="${LYCAON_DEV:-1}"
export LYCAON_CONFIG_DIR="${LYCAON_CONFIG_DIR:-$(lycaon_config_dir)}"
export NO_PROXY="${NO_PROXY:+$NO_PROXY,}127.0.0.1,localhost"

export VITE_DEN_SCROLL_DEBUG="${VITE_DEN_SCROLL_DEBUG:-1}"
SCROLL_DEBUG_FILE="${VITE_DEN_SCROLL_DEBUG_FILE:-${LYCAON_CONFIG_DIR}/debug/den-scroll.jsonl}"
export VITE_DEN_SCROLL_DEBUG_FILE="${SCROLL_DEBUG_FILE}"
if [[ "${VITE_DEN_SCROLL_DEBUG}" == "1" ]]; then
  mkdir -p "$(dirname "${SCROLL_DEBUG_FILE}")"
  : >"${SCROLL_DEBUG_FILE}"
  echo "den scroll debug: ${SCROLL_DEBUG_FILE}" >&2
  echo "  tail -f ${SCROLL_DEBUG_FILE}" >&2
fi

CONFIG_DIR="${LYCAON_CONFIG_DIR}"
export VITE_LYCAON_API_URL="${VITE_LYCAON_API_URL:-${DEFAULT_BASE}}"
if [[ -z "${VITE_LYCAON_API_TOKEN:-}" && -f "${CONFIG_DIR}/api.token" ]]; then
  export VITE_LYCAON_API_TOKEN="$(tr -d '\n\r' <"${CONFIG_DIR}/api.token")"
fi

cd "${DEN_DIR}"
exec bun run tauri dev "$@"
