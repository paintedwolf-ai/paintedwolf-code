#!/usr/bin/env bash
set -euo pipefail

# The browser journeys the merge queue's browser-smoke lane and the local check summary run.
DIR="$(cd "$(dirname "$0")" && pwd)"
exec bash "${DIR}/den-harness-test.sh" \
  e2e/harness-smoke.spec.ts e2e/prompt.spec.ts e2e/boot.spec.ts e2e/first-run-golden-path.spec.ts \
  e2e/project-switch.spec.ts e2e/checkpoint-tool-approval.spec.ts e2e/queue-send.spec.ts e2e/sidecar-cors.spec.ts
