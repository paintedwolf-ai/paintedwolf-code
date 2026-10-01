#!/usr/bin/env bash
# Desktop end-to-end test runner.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/e2e/env.sh
source "${DIR}/e2e/env.sh"

export LYCAON_E2E_STATE_DIR="$(mktemp -d -t lycaon-e2e-state.XXXXXX)"
export PLAYWRIGHT_E2E=desktop

FIXTURE_SRC="${ROOT}/lycaon/test/fixtures/e2e/minimal-go-project"
export LYCAON_E2E_PROJECT_DIR="${LYCAON_E2E_STATE_DIR}/minimal-go-project"
mkdir -p "${LYCAON_E2E_PROJECT_DIR}"
cp -R "${FIXTURE_SRC}/." "${LYCAON_E2E_PROJECT_DIR}/"
export LYCAON_E2E_PROJECT_DIR="$(cd "${LYCAON_E2E_PROJECT_DIR}" && pwd -P)"

cleanup() {
  bash "${DIR}/e2e-sidecar-stop.sh" || true
  if [[ "${LYCAON_E2E_CLEANUP_STATE:-1}" == "1" && -n "${LYCAON_E2E_STATE_DIR:-}" ]]; then
    rm -rf "${LYCAON_E2E_STATE_DIR}"
  fi
  # CI retains failure traces for the upload-artifact step.
  if [[ "${LYCAON_E2E_KEEP_ARTIFACTS:-0}" != "1" ]]; then
    rm -rf "${ROOT}/lycaon-den/test-results" "${ROOT}/lycaon-den/playwright-report"
  fi
}
trap cleanup EXIT INT TERM

if ! command -v bun >/dev/null 2>&1; then
  echo "error: bun required — https://bun.sh" >&2
  exit 1
fi
if ! command -v cargo >/dev/null 2>&1; then
  echo "error: cargo required for Tauri desktop E2E" >&2
  exit 1
fi

FILTER="${LYCAON_E2E_GREP:-}"
EXTRA=()
if [[ -n "${FILTER}" ]]; then
  EXTRA+=(--grep "${FILTER}")
fi
if [[ $# -gt 0 ]]; then
  EXTRA+=("$@")
fi

# Stage the executables and resources required by desktop tests.
bash "${DIR}/stage-engine.sh" --minimal

cd "${ROOT}/lycaon-den"
# macOS uses playwright install without the Linux package setup.
if ! bunx playwright install chromium --with-deps; then
  echo "playwright install --with-deps failed; retrying without system deps" >&2
  bunx playwright install chromium
fi

echo "e2e-den (desktop): state=${LYCAON_E2E_STATE_DIR}" >&2
export PLAYWRIGHT_E2E=desktop
if ((${#EXTRA[@]} > 0)); then
  bunx playwright test --project=desktop "${EXTRA[@]}"
else
  bunx playwright test --project=desktop
fi
