#!/usr/bin/env bash
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
exec bash "${DIR}/den-harness-test.sh" \
  --grep "shell boots and reaches sidecar|window.__harness drives a chat|new session → prompt"
