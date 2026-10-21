#!/usr/bin/env bash
# Prepare the dependency and test-binary builds restored by verification lanes.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export CARGO_TARGET_DIR="${ROOT}/lycaon-den/target"
if [[ ! -f "${ROOT}/lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md" ]]; then
  bash "${ROOT}/scripts/licenses-notices.sh"
fi
bash "${ROOT}/scripts/ensure-tauri-binaries.sh"
if ! bash "${ROOT}/scripts/test-source-snapshot.sh" holding; then
  exec bash "${ROOT}/scripts/test-source-snapshot.sh" run -- bash "$0"
fi
# Warm the same source slot and target directories that verification uses.
source "${ROOT}/scripts/artifact-paths.sh"
CARGO_TARGET_DIR="${PW_COMPILER_CACHE_DIR}/decide" cargo test --locked --all-targets --no-run \
  --manifest-path "${ROOT}/lycaon/internal/decide/native/Cargo.toml" \
  --features "$(bash "${ROOT}/scripts/decide-features.sh")"
bash "${ROOT}/scripts/build-decide.sh"
cd "${ROOT}/lycaon-den/src-tauri"
cargo test --locked --workspace --all-targets --no-run
