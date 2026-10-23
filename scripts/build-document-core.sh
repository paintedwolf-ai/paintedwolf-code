#!/usr/bin/env bash
# Compile the native document process and stage an isolated executable.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
# shellcheck source=toolchain/rust-path-remap.sh
source "${ROOT}/scripts/toolchain/rust-path-remap.sh"
rust_path_remap "${ROOT}"
CRATE="${ROOT}/lycaon/internal/documentcore/native"
NAME="pw-document-core"
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) NAME="${NAME}.exe" ;;
esac
OUTPUT="${PW_BUILD_DIR}/${NAME}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) OUTPUT="${2:?--output requires a path}"; shift 2 ;;
    *) echo "usage: build-document-core.sh [--output PATH]" >&2; exit 2 ;;
  esac
done
command -v cargo >/dev/null || { echo "error: cargo required" >&2; exit 1; }
if [[ "$(uname -s)" == Darwin ]]; then
  export MACOSX_DEPLOYMENT_TARGET="$(tr -d '[:space:]' < "${ROOT}/lycaon/internal/platformfloor/macos_floor.txt")"
fi
export CARGO_TARGET_DIR="${PW_COMPILER_CACHE_DIR}/document-core"
# Keep the compiler output stable until its executable has been copied.
# shellcheck source=digest-run-lock.sh
source "${ROOT}/scripts/digest-run-lock.sh"
mkdir -p "${PW_COMPILER_CACHE_DIR}"
digest_acquire_lock "${PW_COMPILER_CACHE_DIR}/document-core-build.lockdir" 900
trap digest_release_lock EXIT
cargo build --locked --release --manifest-path "${CRATE}/Cargo.toml" --bin pw-document-core >&2
mkdir -p "$(dirname "${OUTPUT}")"
# A fresh inode preserves macOS signature validation when replacing a binary.
STAGED="$(mktemp "$(dirname "${OUTPUT}")/.${NAME}.XXXXXX")"
cp "${CARGO_TARGET_DIR}/release/${NAME}" "${STAGED}"
chmod 0755 "${STAGED}"
mv -f "${STAGED}" "${OUTPUT}"
printf '%s\n' "${OUTPUT}"
