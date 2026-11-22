#!/usr/bin/env bash
# Warms the development browser cache that `LYCAON_DEV=1 <engine> browser ensure` resolves from,
# for a lane that later builds its sidecar offline. The engine is built directly: such lanes
# carry no decide-heads bundle, which ./task browser:ensure's development build stages.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
(cd "${ROOT}/lycaon" && go build -o "${WORK}/lycaon" ./cmd/lycaon)
LYCAON_DEV=1 "${WORK}/lycaon" browser ensure
