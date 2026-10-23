#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
source "${ROOT}/scripts/toolchain/decide-environment.sh"
exec cargo test --locked --all-targets --manifest-path "${ROOT}/lycaon/internal/decide/native/Cargo.toml" \
  --features "$(bash "${ROOT}/scripts/decide-features.sh")" "$@"
