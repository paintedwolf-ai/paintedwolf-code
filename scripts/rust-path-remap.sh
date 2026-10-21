#!/usr/bin/env bash
# Shipped Rust binaries must not carry the builder's home, toolchain or checkout
# paths in panic locations and debug strings. Sourced by every Rust build entry
# point; it extends CARGO_ENCODED_RUSTFLAGS, which cargo prefers over RUSTFLAGS,
# so paths containing spaces stay single arguments. C and C++ that build scripts
# compile through cc receive the same maps as -ffile-prefix-map.
#
#   rust_path_remap ROOT
rust_path_remap() {
  local root="$1"
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
  for mapping in "${home}=/build-home" "${rustup%/}=/rustup" "${cargo%/}=/cargo" "${root%/}=/source"; do
    [[ "${mapping%%=*}" == /?* ]] || continue
    case "${separator}${flags}${separator}" in
      *"${separator}--remap-path-prefix=${mapping}${separator}"*) continue ;;
    esac
    flags="${flags:+${flags}${separator}}--remap-path-prefix=${mapping}"
    # CFLAGS splits on whitespace, so a mapping with spaces cannot be expressed there.
    if [[ "${mapping}" != *[[:space:]]* && " ${CFLAGS:-} " != *" -ffile-prefix-map=${mapping} "* ]]; then
      CFLAGS="${CFLAGS:+${CFLAGS} }-ffile-prefix-map=${mapping}"
      CXXFLAGS="${CXXFLAGS:+${CXXFLAGS} }-ffile-prefix-map=${mapping}"
    fi
  done
  export CARGO_ENCODED_RUSTFLAGS="${flags}" CFLAGS CXXFLAGS
  unset RUSTFLAGS
}
