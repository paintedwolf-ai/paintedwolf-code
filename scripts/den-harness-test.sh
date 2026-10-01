#!/usr/bin/env bash
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
if [[ "${1:-}" != "--supervised" ]]; then
  exec python3 "${DIR}/harness/supervise.py" "$0" "$@"
fi
shift
# shellcheck source=scripts/harness/lib.sh
source "${DIR}/harness/lib.sh"

STATUS=1
cleanup() {
  if [[ "${STATUS}" == "0" ]]; then
    rm -rf "${ROOT}/lycaon-den/test-results" "${ROOT}/lycaon-den/playwright-report"
  else
    echo "den-harness-test: kept Playwright artifacts in lycaon-den/test-results" >&2
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Browser tests can supply manual model completions.
export LYCAON_LLM_MANUAL="${LYCAON_LLM_MANUAL:-1}"

# File watching would reload the page during a test.
export LYCAON_E2E_FROZEN=1

harness_up
harness_vite_bg

# Browser tests write into a disposable project.
export LYCAON_E2E_PROJECT_DIR="${LYCAON_E2E_STATE_DIR}/e2e-fixture"
mkdir -p "${LYCAON_E2E_PROJECT_DIR}"
cp -R "${ROOT}/lycaon/test/fixtures/e2e/minimal-go-project/." "${LYCAON_E2E_PROJECT_DIR}/"
export LYCAON_E2E_PROJECT_DIR="$(cd "${LYCAON_E2E_PROJECT_DIR}" && pwd -P)"

cd "${ROOT}/lycaon-den"
if ! command -v bun >/dev/null 2>&1; then
  echo "error: bun required — https://bun.sh" >&2
  exit 1
fi
bunx playwright install chromium webkit --with-deps 2>/dev/null || bunx playwright install chromium webkit

export PLAYWRIGHT_E2E=web
export PLAYWRIGHT_REUSE_SERVER=1
export LYCAON_E2E_BASE_URL
export LYCAON_E2E_API_URL
if bunx playwright test --project=web "$@"; then
  STATUS=0
else
  STATUS=$?
fi
exit "${STATUS}"
