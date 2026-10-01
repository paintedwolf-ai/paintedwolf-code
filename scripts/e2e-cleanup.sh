#!/usr/bin/env bash
# Remove leftover end-to-end test artifacts.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/e2e/docker-lib.sh
source "${DIR}/e2e/docker-lib.sh"

if [[ -n "${LYCAON_E2E_STATE_DIR:-}" ]]; then
  bash "${DIR}/e2e/docker-stack-down.sh" || true
  if [[ "${LYCAON_E2E_CLEANUP_STATE:-1}" == "1" ]]; then
    rm -rf "${LYCAON_E2E_STATE_DIR}"
  fi
fi

e2e_docker_purge_orphan_projects "${ROOT}"

# Preserve failure traces when the caller requests artifacts.
if [[ "${LYCAON_E2E_KEEP_ARTIFACTS:-0}" != "1" ]]; then
  rm -rf "${ROOT}/lycaon-den/test-results" "${ROOT}/lycaon-den/playwright-report"
fi

echo "e2e-cleanup: done" >&2
