#!/usr/bin/env bash
# Prepare the dependency and test-binary builds restored by verification lanes.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export CARGO_TARGET_DIR="${ROOT}/lycaon-den/target"
if [[ ! -f "${ROOT}/lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md" ]]; then
  bash "${ROOT}/scripts/licenses-notices.sh"
fi
bash "${ROOT}/scripts/ensure-tauri-binaries.sh"
cd "${ROOT}/lycaon-den/src-tauri"
cargo test --locked --workspace --all-targets --no-run
