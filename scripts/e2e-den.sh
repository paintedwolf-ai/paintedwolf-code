#!/usr/bin/env bash
# Tier B browser E2E.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIR="$(cd "$(dirname "$0")" && pwd)"
E2E_DIR="${DIR}/e2e"
# shellcheck source=scripts/e2e/env.sh
source "${DIR}/e2e/env.sh"
# shellcheck source=scripts/e2e/docker-lib.sh
source "${DIR}/e2e/docker-lib.sh"

if ! e2e_docker_available; then
  echo "error: ./task e2e:den requires Docker (daemon running, docker compose available)" >&2
  exit 1
fi

if ! command -v bun >/dev/null 2>&1; then
  echo "error: bun required — https://bun.sh" >&2
  exit 1
fi

pull_e2e_images() {
  local compose_file="${ROOT}/lycaon/test/fixtures/e2e/docker-compose.e2e.yml"
  local images
  images="$(
    LYCAON_REPO_ROOT="${ROOT}" \
    LYCAON_E2E_STATE_DIR="${TMPDIR:-/tmp}" \
    LYCAON_E2E_PROJECT_DIR="${ROOT}" \
    LYCAON_E2E_CONFIG_DIR="${TMPDIR:-/tmp}" \
      docker compose -f "${compose_file}" config --images
  )"
  while IFS= read -r image; do
    [[ -n "${image}" ]] || continue
    if ! docker image inspect "${image}" >/dev/null 2>&1; then
      docker pull "${image}"
    fi
  done <<<"${images}"
}

# Each shard uses a separate stack, state, ports, and artifacts.
if [[ "${LYCAON_E2E_SHARD_CHILD:-0}" != "1" ]]; then
  shard_count="${LYCAON_E2E_SHARDS:-2}"
  if ! [[ "${shard_count}" =~ ^[1-9][0-9]*$ ]]; then
    echo "error: LYCAON_E2E_SHARDS must be a positive integer" >&2
    exit 1
  fi
  if ((shard_count > 1)); then
    cd "${ROOT}/lycaon-den"
    if ! bunx playwright install chromium webkit --with-deps; then
      echo "playwright install --with-deps failed; retrying without system deps" >&2
      bunx playwright install chromium webkit
    fi
    pull_e2e_images
    rm -rf "${ROOT}/lycaon-den/test-results" "${ROOT}/lycaon-den/playwright-report"
    pids=()
    stop_shards() {
      if ((${#pids[@]} > 0)); then
        kill "${pids[@]}" 2>/dev/null || true
        wait "${pids[@]}" 2>/dev/null || true
      fi
    }
    exit_shards() {
      trap - INT TERM
      stop_shards
      exit 130
    }
    trap exit_shards INT TERM
    for ((shard = 1; shard <= shard_count; shard++)); do
      LYCAON_E2E_SHARD_CHILD=1 \
      LYCAON_E2E_PLAYWRIGHT_READY=1 \
      LYCAON_E2E_OUTPUT_DIR="${ROOT}/lycaon-den/test-results/shard-${shard}" \
        bash "$0" "$@" --shard="${shard}/${shard_count}" &
      pids+=("$!")
    done
    status=0
    for pid in "${pids[@]}"; do
      if ! wait "${pid}"; then
        status=1
      fi
    done
    trap - INT TERM
    exit "${status}"
  fi
fi

# shellcheck source=artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
mkdir -p "${PW_ARTIFACT_ROOT}/e2e-state"
export LYCAON_E2E_STATE_DIR="$(mktemp -d "${PW_ARTIFACT_ROOT}/e2e-state/run.XXXXXX")"
export PLAYWRIGHT_E2E=web
# Match den:harness:test: browser tests can supply manual model completions.
export LYCAON_LLM_MANUAL="${LYCAON_LLM_MANUAL:-1}"
if [[ "${LYCAON_LLM_MANUAL}" == "1" ]]; then
  export LYCAON_LLM_MOCK=0
fi

FIXTURE_SRC="${ROOT}/lycaon/test/fixtures/e2e/minimal-go-project"
export LYCAON_E2E_PROJECT_DIR="${LYCAON_E2E_STATE_DIR}/minimal-go-project"
mkdir -p "${LYCAON_E2E_PROJECT_DIR}"
cp -R "${FIXTURE_SRC}/." "${LYCAON_E2E_PROJECT_DIR}/"
export LYCAON_E2E_PROJECT_DIR="$(cd "${LYCAON_E2E_PROJECT_DIR}" && pwd -P)"
CONFIG_FIXTURE="${ROOT}/lycaon/test/fixtures/e2e/config"
export LYCAON_E2E_CONFIG_DIR="${LYCAON_E2E_STATE_DIR}/config"
mkdir -p "${LYCAON_E2E_CONFIG_DIR}"
cp "${CONFIG_FIXTURE}/providers.local.yaml" "${CONFIG_FIXTURE}/model-policy.yaml" "${LYCAON_E2E_CONFIG_DIR}/"

cleanup_done=0
cleanup() {
  if [[ "${cleanup_done}" == "1" ]]; then
    return
  fi
  cleanup_done=1
  bash "${E2E_DIR}/docker-stack-down.sh" || true
  if [[ "${LYCAON_E2E_CLEANUP_STATE:-1}" == "1" && -n "${LYCAON_E2E_STATE_DIR:-}" ]]; then
    rm -rf "${LYCAON_E2E_STATE_DIR}"
  fi
  if [[ "${LYCAON_E2E_KEEP_ARTIFACTS:-0}" != "1" ]]; then
    rm -rf "${LYCAON_E2E_OUTPUT_DIR:-${ROOT}/lycaon-den/test-results}"
    if [[ -z "${LYCAON_E2E_OUTPUT_DIR:-}" ]]; then
      rm -rf "${ROOT}/lycaon-den/playwright-report"
    fi
  fi
}
trap cleanup EXIT INT TERM

FILTER="${LYCAON_E2E_GREP:-}"
EXTRA=()
if [[ -n "${FILTER}" ]]; then
  EXTRA+=(--grep "${FILTER}")
fi
if [[ $# -gt 0 ]]; then
  EXTRA+=("$@")
fi

cd "${ROOT}/lycaon-den"

# Retry without system package setup.
if [[ "${LYCAON_E2E_PLAYWRIGHT_READY:-0}" != "1" ]]; then
  if ! bunx playwright install chromium webkit --with-deps; then
    echo "playwright install --with-deps failed; retrying without system deps" >&2
    bunx playwright install chromium webkit
  fi
fi

# shellcheck source=scripts/e2e/docker-stack-up.sh
source "${E2E_DIR}/docker-stack-up.sh"
project_payload="$(
  bun -e 'process.stdout.write(JSON.stringify({name: "Harness", roots: [{path: process.env.LYCAON_E2E_PROJECT_DIR}]}))'
)"
curl --fail --show-error --silent -X POST "${LYCAON_E2E_API_URL}/v1/projects" \
  -H "Authorization: Bearer ${LYCAON_E2E_TOKEN}" \
  -H "Content-Type: application/json" \
  --data-binary "${project_payload}" >/dev/null
export PLAYWRIGHT_REUSE_SERVER=1
export LYCAON_E2E_DOCKER_STACK=1
echo "e2e-den (web/docker): project=${LYCAON_E2E_DOCKER_PROJECT} base=${LYCAON_E2E_BASE_URL} state=${LYCAON_E2E_STATE_DIR}" >&2

export PLAYWRIGHT_E2E=web
if ((${#EXTRA[@]} > 0)); then
  bunx playwright test --project=web "${EXTRA[@]}"
else
  bunx playwright test --project=web
fi
