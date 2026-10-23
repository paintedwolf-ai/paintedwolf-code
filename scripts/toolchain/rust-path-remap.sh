#!/usr/bin/env bash
# Keep builder paths out of Rust and native dependency binaries.
rust_path_remap() {
  local root="${PW_SOURCE_ROOT_ORIGINAL:-$1}"
  local snapshots="${PW_TEST_SNAPSHOT_ROOT:-/tmp/paintedwolf-source-snapshots-${UID}}"
  local separator=$'\x1f'
  local flags="${CARGO_ENCODED_RUSTFLAGS:-}"
  if [[ -z "${flags}" && -n "${RUSTFLAGS:-}" ]]; then
    local word words
    read -r -a words <<<"${RUSTFLAGS}"
    for word in "${words[@]}"; do
      flags="${flags:+${flags}${separator}}${word}"
    done
  fi
  local home="${HOME:-}" rustup="${RUSTUP_HOME:-}" cargo="${CARGO_HOME:-}"
  home="${home%/}"
  if [[ -n "${home}" ]]; then
    rustup="${rustup:-${home}/.rustup}"
    cargo="${cargo:-${home}/.cargo}"
  fi
  # rustc applies the last matching prefix, so broader directories come first.
  local mapping
  for mapping in "${home}=/build-home" "${rustup%/}=/rustup" "${cargo%/}=/cargo" "${snapshots%/}=/source-snapshots" "${root%/}=/source"; do
    [[ "${mapping%%=*}" == /?* ]] || continue
    case "${separator}${flags}${separator}" in
      *"${separator}--remap-path-prefix=${mapping}${separator}"*) continue ;;
    esac
    flags="${flags:+${flags}${separator}}--remap-path-prefix=${mapping}"
    # CFLAGS splits on whitespace, so a mapping with spaces cannot be expressed there.
    if [[ "${mapping}" != *[[:space:]]* && " ${CFLAGS:-} " != *" -ffile-prefix-map=${mapping} "* ]]; then
      CFLAGS="${CFLAGS:-}${CFLAGS:+ }-ffile-prefix-map=${mapping}"
      CXXFLAGS="${CXXFLAGS:-}${CXXFLAGS:+ }-ffile-prefix-map=${mapping}"
    fi
  done
  export CARGO_ENCODED_RUSTFLAGS="${flags}" CFLAGS CXXFLAGS
  unset RUSTFLAGS
}
