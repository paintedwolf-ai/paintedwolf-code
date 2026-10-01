#!/usr/bin/env bash
# Stage the same decision release the host embeds.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
ENGINE_ROOT="${1:?usage: stage-decide-heads.sh <engine-root>}"
python3 "${ROOT}/scripts/stage-decide-heads.py" \
  --manifest "${ROOT}/lycaon/config/packs/painted-wolf/platform/host/decision-release.json" \
  --source "${BIALY_HEADS_DIR:-${PW_BIN_DIR}/decide-heads}" \
  --destination "${ENGINE_ROOT}/decide/heads"
