#!/usr/bin/env bash
# Builds bialy for this host with the features decide-features.sh picks. An mlx build
# also leaves mlx.metallib beside the binary.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
CRATE="${ROOT}/lycaon/internal/decide/native"
OUT="${1:-${PW_BUILD_DIR}/bialy}"

if ! command -v cargo >/dev/null 2>&1; then
  echo "error: cargo required — install rustup from https://rustup.rs; rust-toolchain.toml selects the version" >&2
  exit 1
fi

FEATURES="$(bash "${ROOT}/scripts/decide-features.sh")"
# MLX writes its compiled kernels here instead of under $HOME.
export MLX_RS_METAL_PATH="${MLX_RS_METAL_PATH:-${PW_BIN_DIR}/mlx-metal}"
if [[ "$(uname -s)" == "Darwin" ]]; then
  MACOS_FLOOR_FILE="${ROOT}/lycaon/internal/platformfloor/macos_floor.txt"
  if [[ -f "${MACOS_FLOOR_FILE}" ]]; then
    export MACOSX_DEPLOYMENT_TARGET="$(tr -d '[:space:]' < "${MACOS_FLOOR_FILE}")"
  fi
fi

export CARGO_TARGET_DIR="${CARGO_TARGET_DIR:-${PW_BUILD_DIR}/decide-target}"
ARGS=(--locked --release --manifest-path "${CRATE}/Cargo.toml" --bin bialy)
if [[ -n "${FEATURES}" ]]; then
  ARGS+=(--features "${FEATURES}")
fi
echo "build-decide — cargo build (features: ${FEATURES:-none}) → ${OUT}" >&2
cargo build "${ARGS[@]}"
mkdir -p "$(dirname "${OUT}")"
# A new inode: overwriting a signed Mach-O in place leaves the kernel's cached signature stale.
STAGED="$(mktemp "$(dirname "${OUT}")/.bialy.XXXXXX")"
cp "${CARGO_TARGET_DIR}/release/bialy" "${STAGED}"
chmod +x "${STAGED}"
mv -f "${STAGED}" "${OUT}"
if [[ ",${FEATURES}," == *",mlx,"* ]]; then
  if [[ ! -f "${MLX_RS_METAL_PATH}/mlx.metallib" ]]; then
    echo "error: ${MLX_RS_METAL_PATH}/mlx.metallib missing after an mlx build" >&2
    exit 1
  fi
  cp "${MLX_RS_METAL_PATH}/mlx.metallib" "$(dirname "${OUT}")/mlx.metallib"
fi
