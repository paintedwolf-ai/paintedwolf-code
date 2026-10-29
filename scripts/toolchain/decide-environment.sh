#!/usr/bin/env bash
# Build and test modes share the dependency compiler environment.
source "${ROOT}/scripts/artifact-paths.sh"
source "${ROOT}/scripts/toolchain/rust-path-remap.sh"
rust_path_remap "${ROOT}"
export CARGO_TARGET_DIR="${PW_COMPILER_CACHE_DIR}/decide"
export MLX_RS_METAL_PATH="${MLX_RS_METAL_PATH:-${PW_BIN_DIR}/mlx-metal}"
if [[ "$(uname -s)" == Darwin ]]; then
  export MACOSX_DEPLOYMENT_TARGET="$(tr -d '[:space:]' < "${ROOT}/lycaon/internal/platformfloor/macos_floor.txt")"
fi
