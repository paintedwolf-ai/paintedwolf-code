#!/usr/bin/env bash
# Prepare dependency builds restored by verification lanes.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export CARGO_TARGET_DIR="${CARGO_TARGET_DIR:-${ROOT}/lycaon-den/target}"
if ! bash "${ROOT}/scripts/test-source-snapshot.sh" holding; then
  exec bash "${ROOT}/scripts/test-source-snapshot.sh" run -- bash scripts/warm-workspace-build.sh
fi
if [[ ! -f "${ROOT}/lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md" ]]; then
  bash "${ROOT}/scripts/licenses-notices.sh"
fi
bash "${ROOT}/scripts/ensure-tauri-binaries.sh"
bash "${ROOT}/scripts/toolchain/decide-test.sh" --no-run
bash "${ROOT}/scripts/build-decide.sh"
cd "${ROOT}/lycaon-den/src-tauri"
cargo test --locked --workspace --all-targets --no-run

# Workspace outputs change with source; dependencies survive package-scoped cleanup.
PYTHONPATH="${ROOT}/scripts" python3 -m ci_policy.cache_payload workspace "${ROOT}/lycaon-den/src-tauri/Cargo.toml"

CARGO_TARGET_DIR="$(python3 "${ROOT}/scripts/artifact_paths.py" compiler-cache "${ROOT}")/decide" \
  PYTHONPATH="${ROOT}/scripts" python3 -m ci_policy.cache_payload workspace "${ROOT}/lycaon/internal/decide/native/Cargo.toml"
