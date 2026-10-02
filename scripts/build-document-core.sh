#!/usr/bin/env bash
# Builds the native text CRDT process for this host and prints its path.
# Builds are addressed by their inputs in the user cache, so every checkout
# and verification slot reuses one binary per source state. --output also
# stages a copy there.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CACHE_ROOT="$(python3 "${ROOT}/scripts/artifact_paths.py" cache)/document-core"
CRATE="${ROOT}/lycaon/internal/documentcore/native"
NAME="pw-document-core"

OUTPUT=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) OUTPUT="${2:?--output requires a path}"; shift 2 ;;
    *) echo "usage: build-document-core.sh [--output PATH]" >&2; exit 2 ;;
  esac
done

if ! command -v cargo >/dev/null 2>&1; then
  echo "error: cargo required — install rustup from https://rustup.rs; rust-toolchain.toml selects the version" >&2
  exit 1
fi

EXE_SUFFIX=""
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) EXE_SUFFIX=".exe" ;;
esac

if [[ "$(uname -s)" == "Darwin" ]]; then
  export MACOSX_DEPLOYMENT_TARGET="$(tr -d '[:space:]' < "${ROOT}/lycaon/internal/platformfloor/macos_floor.txt")"
fi

identity() {
  {
    printf 'target %s\n' "$(rustc --print host-tuple)"
    printf 'macos-floor %s\n' "${MACOSX_DEPLOYMENT_TARGET:-}"
    (cd "${ROOT}" && LC_ALL=C find rust-toolchain.toml scripts/build-document-core.sh \
      lycaon/internal/documentcore/native/Cargo.toml lycaon/internal/documentcore/native/Cargo.lock \
      lycaon/internal/documentcore/native/src -type f -print | LC_ALL=C sort | while IFS= read -r file; do
        printf '%s %s\n' "$(shasum -a 256 "${file}" | cut -d' ' -f1)" "${file}"
      done)
  } | shasum -a 256 | cut -c1-24
}

DIGEST="$(identity)"
IDENTITY_DIR="${CACHE_ROOT}/${DIGEST}"
BUILT="${IDENTITY_DIR}/${NAME}${EXE_SUFFIX}"
build() {
  # One target per identity: builds of other sources never overwrite the
  # artifact between cargo finishing and the copy below.
  export CARGO_TARGET_DIR="${IDENTITY_DIR}/target"
  echo "build-document-core — cargo build → ${BUILT}" >&2
  cargo build --locked --release --manifest-path "${CRATE}/Cargo.toml" --bin "${NAME}" >&2
  # Publish by rename so concurrent builds of one identity never expose a partial file.
  local staged
  staged="$(mktemp "${IDENTITY_DIR}/.${NAME}.XXXXXX")"
  cp "${CARGO_TARGET_DIR}/release/${NAME}${EXE_SUFFIX}" "${staged}"
  chmod 0755 "${staged}"
  mv -f "${staged}" "${BUILT}"
  rm -rf "${CARGO_TARGET_DIR}"
}

if [[ ! -x "${BUILT}" ]]; then
  mkdir -p "${IDENTITY_DIR}"
  # A concurrent build of the same identity may publish first and remove the
  # shared target under this one; its binary comes from the same sources.
  if ! (build) && [[ ! -x "${BUILT}" ]]; then
    exit 1
  fi
fi

if [[ -n "${OUTPUT}" ]]; then
  mkdir -p "$(dirname "${OUTPUT}")"
  # A new inode: overwriting a signed Mach-O in place leaves the kernel's cached signature stale.
  STAGED="$(mktemp "$(dirname "${OUTPUT}")/.${NAME}.XXXXXX")"
  cp "${BUILT}" "${STAGED}"
  chmod 0755 "${STAGED}"
  mv -f "${STAGED}" "${OUTPUT}"
  printf '%s\n' "${OUTPUT}"
else
  printf '%s\n' "${BUILT}"
fi
