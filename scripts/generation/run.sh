#!/usr/bin/env bash
# Keep compiled runtimes and their build receipts together through staging.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
source "${ROOT}/scripts/artifact-paths.sh"
source "${ROOT}/scripts/toolchain/rust-path-remap.sh"
rust_path_remap "${ROOT}"
source "${ROOT}/scripts/digest-run-lock.sh"
mkdir -p "${PW_COMPILER_CACHE_DIR}"
digest_acquire_lock "${PW_COMPILER_CACHE_DIR}/generation-build.lockdir" 900
trap digest_release_lock EXIT
ACTION="${1:?generation action required}"
shift
if [[ "${ACTION}" == stage ]]; then
  python3 "${ROOT}/scripts/stage-generation.py" "$@"
else
  python3 "${ROOT}/scripts/generation/runtime.py" "${ACTION}" "$@"
fi
