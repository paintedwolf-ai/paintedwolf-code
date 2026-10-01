#!/usr/bin/env bash
# Tear down the Tier B end-to-end stack and its resources.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "${DIR}/../.." && pwd)"

PROJECT="${LYCAON_E2E_DOCKER_PROJECT:-}"
COMPOSE_FILE="${LYCAON_E2E_COMPOSE_FILE:-${ROOT}/lycaon/test/fixtures/e2e/docker-compose.e2e.yml}"

# shellcheck source=scripts/e2e/docker-lib.sh
source "${DIR}/docker-lib.sh"

if [[ -z "${PROJECT}" ]]; then
  PROJECT="$(e2e_docker_read_persisted_project || true)"
fi

if [[ -z "${PROJECT}" ]]; then
  exit 0
fi

e2e_docker_teardown_project "${COMPOSE_FILE}" "${PROJECT}"
