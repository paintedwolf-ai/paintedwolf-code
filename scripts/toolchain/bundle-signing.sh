#!/usr/bin/env bash
# Decide, before anything compiles, how a bundle is signed. A macOS bundle is
# Developer ID signed, or ad hoc only when --unsigned asks for it; a release
# missing its identity is refused rather than built ad hoc.
# Sets SIGNING_IDENTITY and STAGE_ARGS; returns 1 on a refused configuration.
bundle_signing() {
  local host="$1" unsigned="$2"
  STAGE_ARGS=(--release)
  SIGNING_IDENTITY=""
  if [[ "${host}" != darwin ]]; then
    [[ "${unsigned}" == 0 ]] || { echo "error: --unsigned applies to macOS bundles" >&2; return 1; }
    return 0
  fi
  if [[ "${unsigned}" == 1 ]]; then
    [[ -z "${APPLE_SIGNING_IDENTITY:-}" ]] || {
      echo "error: --unsigned builds an ad hoc bundle and cannot take APPLE_SIGNING_IDENTITY" >&2
      return 1
    }
    SIGNING_IDENTITY="-"
    STAGE_ARGS+=(--ad-hoc-host)
    return 0
  fi
  [[ -n "${APPLE_SIGNING_IDENTITY:-}" && "${APPLE_SIGNING_IDENTITY}" != "-" && -n "${APPLE_ENGINE_PROVISIONING_PROFILE:-}" ]] || {
    echo "error: a macOS bundle needs APPLE_SIGNING_IDENTITY and APPLE_ENGINE_PROVISIONING_PROFILE; --unsigned builds an ad hoc rehearsal bundle" >&2
    return 1
  }
  SIGNING_IDENTITY="${APPLE_SIGNING_IDENTITY}"
}
