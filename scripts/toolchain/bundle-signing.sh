#!/usr/bin/env bash
# Decide, before anything compiles, how a bundle is signed. Each macOS mode is
# chosen explicitly; a release missing its Developer ID is refused, never built ad hoc:
#   developer-id  the default and --phase package: identity and engine profile required
#   compile       --phase compile: nothing is signed and no signing credential is held;
#                 the package phase stages the host and signs
#   ad-hoc        --unsigned: the rehearsal's complete bundle, signed ad hoc
# Sets SIGNING_MODE, SIGNING_IDENTITY and HOST_SIGNING; returns 1 on a refused configuration.
bundle_signing() {
  local host="$1" unsigned="$2" phase="$3"
  SIGNING_MODE="none"
  SIGNING_IDENTITY=""
  HOST_SIGNING=()
  if [[ "${host}" != darwin ]]; then
    [[ "${unsigned}" == 0 ]] || { echo "error: --unsigned applies to macOS bundles" >&2; return 1; }
    return 0
  fi
  if [[ "${phase}" == compile ]]; then
    [[ "${unsigned}" == 0 && -z "${APPLE_SIGNING_IDENTITY:-}" ]] || {
      echo "error: --phase compile is unsigned by design and takes neither --unsigned nor APPLE_SIGNING_IDENTITY" >&2
      return 1
    }
    SIGNING_MODE="compile"
    return 0
  fi
  if [[ "${unsigned}" == 1 ]]; then
    [[ "${phase}" == all && -z "${APPLE_SIGNING_IDENTITY:-}" ]] || {
      echo "error: --unsigned builds a complete ad hoc bundle; it takes no --phase and no APPLE_SIGNING_IDENTITY" >&2
      return 1
    }
    SIGNING_MODE="ad-hoc"
    SIGNING_IDENTITY="-"
    return 0
  fi
  [[ -n "${APPLE_SIGNING_IDENTITY:-}" && "${APPLE_SIGNING_IDENTITY}" != "-" && -n "${APPLE_ENGINE_PROVISIONING_PROFILE:-}" ]] || {
    echo "error: a macOS bundle needs APPLE_SIGNING_IDENTITY and APPLE_ENGINE_PROVISIONING_PROFILE; --phase compile builds unsigned and --unsigned builds an ad hoc rehearsal bundle" >&2
    return 1
  }
  SIGNING_MODE="developer-id"
  SIGNING_IDENTITY="${APPLE_SIGNING_IDENTITY}"
  HOST_SIGNING=(--release)
}
