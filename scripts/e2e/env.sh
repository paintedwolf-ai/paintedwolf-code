# Shared constants for Den/browser E2E (sourced by scripts/e2e-*.sh).

# LYCAON_E2E_ADDR is host:port (the sidecar listen address). A copied API URL
# (http://127.0.0.1:8843) is accepted and stripped before export.
_e2e_addr="${LYCAON_E2E_ADDR:-127.0.0.1:8787}"
_e2e_addr="${_e2e_addr#http://}"
_e2e_addr="${_e2e_addr#https://}"
_e2e_addr="${_e2e_addr%%/*}"
export LYCAON_E2E_ADDR="${_e2e_addr}"
export LYCAON_E2E_TOKEN="${LYCAON_E2E_TOKEN:-e2e-playwright-token}"
export LYCAON_E2E_VITE_PORT="${LYCAON_E2E_VITE_PORT:-1420}"
export LYCAON_E2E_VITE_HOST="${LYCAON_E2E_VITE_HOST:-127.0.0.1}"
export LYCAON_E2E_BASE_URL="http://${LYCAON_E2E_VITE_HOST}:${LYCAON_E2E_VITE_PORT}"
export LYCAON_E2E_API_URL="http://${LYCAON_E2E_ADDR}"
export LYCAON_E2E_HEALTH_URL="${LYCAON_E2E_API_URL}/health"
_e2e_env_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../artifact-paths.sh
source "${_e2e_env_root}/scripts/artifact-paths.sh"
export LYCAON_E2E_GO_CACHE_DIR="${LYCAON_E2E_GO_CACHE_DIR:-${PW_ARTIFACT_ROOT}/e2e-go-cache}"
