#!/usr/bin/env bash
# Foreground desktop development shell for end-to-end tests.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/e2e/env.sh
source "${DIR}/e2e/env.sh"
# shellcheck source=scripts/listen-port.sh
source "${DIR}/listen-port.sh"

child_pid=""
stop_child() {
  if [[ -n "${child_pid}" ]]; then
    kill -TERM -- "-${child_pid}" 2>/dev/null || true
    for _ in {1..40}; do
      if ! kill -0 -- "-${child_pid}" 2>/dev/null; then
        break
      fi
      sleep 0.05
    done
    if kill -0 -- "-${child_pid}" 2>/dev/null; then
      kill -KILL -- "-${child_pid}" 2>/dev/null || true
    fi
    wait "${child_pid}" 2>/dev/null || true
    child_pid=""
  fi
}
cleanup() {
  stop_child
  bash "${DIR}/e2e-sidecar-stop.sh" || true
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM

bash "${DIR}/e2e-sidecar-bg.sh"

export LYCAON_ATTACH_ONLY=1
export LYCAON_DEV=1
export VITE_LYCAON_API_URL="${LYCAON_E2E_API_URL}"
export VITE_LYCAON_API_TOKEN="${LYCAON_E2E_TOKEN}"
export NO_PROXY="${NO_PROXY:+$NO_PROXY,}127.0.0.1,localhost"

echo "e2e-tauri-dev: attach ${LYCAON_E2E_API_URL} ui=${LYCAON_E2E_BASE_URL}" >&2

reclaim_listen_port 1420 is_den_vite_pid "e2e-tauri-dev" "stop that listener"

cd "${ROOT}/lycaon-den"
set -m
bun run tauri dev &
child_pid=$!
set +m
set +e
wait "${child_pid}"
rc=$?
set -e
child_pid=""
exit "${rc}"
