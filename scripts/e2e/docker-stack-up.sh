#!/usr/bin/env bash
# Start an isolated Tier B end-to-end stack.
set -euo pipefail

_e2e_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_e2e_root="$(cd "${_e2e_dir}/../.." && pwd)"
# shellcheck source=scripts/e2e/env.sh
source "${_e2e_dir}/env.sh"
# shellcheck source=scripts/e2e/docker-lib.sh
source "${_e2e_dir}/docker-lib.sh"

if ! e2e_docker_available; then
  echo "error: docker-stack-up requires a running Docker engine" >&2
  exit 1
fi

if [[ -z "${LYCAON_E2E_STATE_DIR:-}" || -z "${LYCAON_E2E_PROJECT_DIR:-}" ]]; then
  echo "error: LYCAON_E2E_STATE_DIR and LYCAON_E2E_PROJECT_DIR must be set" >&2
  exit 1
fi

export LYCAON_REPO_ROOT="${_e2e_root}"
export LYCAON_E2E_COMPOSE_FILE="${_e2e_root}/lycaon/test/fixtures/e2e/docker-compose.e2e.yml"
mkdir -p "${LYCAON_E2E_GO_CACHE_DIR}/mod" "${LYCAON_E2E_GO_CACHE_DIR}/build" \
  "${LYCAON_E2E_GO_CACHE_DIR}/cargo-registry" "${LYCAON_E2E_GO_CACHE_DIR}/document-core-target"
export LYCAON_E2E_DOCKER_PROJECT="${LYCAON_E2E_DOCKER_PROJECT:-$(e2e_docker_random_project lycae2e)}"
export LYCAON_E2E_DOCKER_STACK=1
e2e_docker_persist_project "${LYCAON_E2E_DOCKER_PROJECT}"

echo "e2e-docker-stack-up: project=${LYCAON_E2E_DOCKER_PROJECT}" >&2

docker compose -f "${LYCAON_E2E_COMPOSE_FILE}" -p "${LYCAON_E2E_DOCKER_PROJECT}" up -d sidecar

SIDECAR_PORT="$(e2e_docker_compose_port sidecar 8787)"
export LYCAON_E2E_ADDR="127.0.0.1:${SIDECAR_PORT}"
e2e_refresh_derived_urls

e2e_wait_http "${LYCAON_E2E_HEALTH_URL}" "sidecar /health" 600

export VITE_LYCAON_API_URL="${LYCAON_E2E_API_URL}"
docker compose -f "${LYCAON_E2E_COMPOSE_FILE}" -p "${LYCAON_E2E_DOCKER_PROJECT}" up -d vite

VITE_PORT="$(e2e_docker_compose_port vite 1420)"
export LYCAON_E2E_VITE_PORT="${VITE_PORT}"
e2e_refresh_derived_urls

e2e_wait_http "${LYCAON_E2E_BASE_URL}" "vite dev server" 300

echo "e2e-docker-stack-up: sidecar=${LYCAON_E2E_API_URL} vite=${LYCAON_E2E_BASE_URL}" >&2
