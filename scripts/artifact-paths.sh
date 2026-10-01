#!/usr/bin/env bash
# Sourceable helper to resolve out-of-tree paths for Painted Wolf Code.

_RESOLVER_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export PW_ARTIFACT_ROOT="${PW_ARTIFACT_ROOT:-$(python3 "${_RESOLVER_ROOT}/scripts/artifact_paths.py" artifacts "${_RESOLVER_ROOT}")}"
export PW_BIN_DIR="${PW_BIN_DIR:-$(python3 "${_RESOLVER_ROOT}/scripts/artifact_paths.py" bin "${_RESOLVER_ROOT}")}"
export PW_BUILD_DIR="${PW_BUILD_DIR:-$(python3 "${_RESOLVER_ROOT}/scripts/artifact_paths.py" build "${_RESOLVER_ROOT}")}"
export PW_LOCK_ROOT="${PW_LOCK_ROOT:-$(python3 "${_RESOLVER_ROOT}/scripts/artifact_paths.py" locks "${_RESOLVER_ROOT}")}"
