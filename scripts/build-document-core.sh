#!/usr/bin/env bash
# Build the sandboxed text CRDT used by the host.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if ! bash "${ROOT}/scripts/repo-snapshot-lock.sh" holding; then
  exec bash "${ROOT}/scripts/repo-snapshot-lock.sh" generate -- bash "$0" "$@"
fi
# shellcheck source=artifact-paths.sh
source "$(dirname "$0")/artifact-paths.sh"
CORE="${ROOT}/lycaon/internal/documentcore"
RUST_VERSION="1.97.1"
TARGET="wasm32-wasip1"
SYSROOT="${PW_BIN_DIR}/document-core-sysroot"
export CARGO_TARGET_DIR="${PW_BUILD_DIR}/document-core-target"
export CARGO_TARGET_WASM32_WASIP1_RUSTFLAGS="--sysroot=${SYSROOT}"

if [[ "${1:-}" == "--setup" && ( ! -x "${SYSROOT}/bin/rustc" || ! -d "${SYSROOT}/lib/rustlib/${TARGET}" ) ]]; then
  scratch="$(mktemp -d "${TMPDIR:-/tmp}/document-core-toolchain.XXXXXX")"
  trap 'rm -rf "$scratch"' EXIT
  host="$(rustc -vV | sed -n 's/^host: //p')"
  for component in "rustc-${RUST_VERSION}-${host}" "rust-std-${RUST_VERSION}-${host}" "rust-std-${RUST_VERSION}-${TARGET}"; do
    if [[ "$component" == rustc-* && -x "${SYSROOT}/bin/rustc" ]]; then continue; fi
    if [[ "$component" == "rust-std-${RUST_VERSION}-${host}" && -d "${SYSROOT}/lib/rustlib/${host}" ]]; then continue; fi
    archive="${component}.tar.xz"
    curl --fail --location --silent --show-error "https://static.rust-lang.org/dist/${archive}" -o "${scratch}/${archive}"
    curl --fail --location --silent --show-error "https://static.rust-lang.org/dist/${archive}.sha256" -o "${scratch}/${archive}.sha256"
    (cd "$scratch" && shasum -a 256 -c "${archive}.sha256")
    tar -xJf "${scratch}/${archive}" -C "$scratch"
    bash "${scratch}/${component}/install.sh" --prefix="$SYSROOT" --disable-ldconfig
  done
fi
export RUSTC="${SYSROOT}/bin/rustc"
export RUSTDOC="${SYSROOT}/bin/rustdoc"

cargo build --locked --manifest-path "${CORE}/native/Cargo.toml" --target "$TARGET" --release
source "${ROOT}/scripts/snapshot-publish.sh"
staged="$(mktemp "${TMPDIR:-/tmp}/document-core.XXXXXX")"
cp "${CARGO_TARGET_DIR}/${TARGET}/release/document_core.wasm" "$staged"
chmod 0644 "$staged"
manifest="$(mktemp "${TMPDIR:-/tmp}/document-core-manifest.XXXXXX")"
python3 "${ROOT}/scripts/document-core-manifest.py" generate --binary "$staged" --output "$manifest"
chmod 0644 "$manifest"
snapshot_publish_file "$staged" "${CORE}/core.wasm"
snapshot_publish_file "$manifest" "${CORE}/core.manifest.json"
snapshot_publish_finish
