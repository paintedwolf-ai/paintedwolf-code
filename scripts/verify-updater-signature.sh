#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
[[ $# -eq 3 ]] || {
  echo "Usage: verify-updater-signature.sh ARTIFACT SIGNATURE VERSION" >&2
  exit 2
}

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/updater-signature.XXXXXX")"
trap 'rm -rf "${WORKDIR}"' EXIT
python3 - "${ROOT}/scripts" "${WORKDIR}/signing.pub" <<'PYKEY'
import sys
sys.path.insert(0, sys.argv[1])
from update_keys import load_registry, generation
keys = load_registry()
with open(sys.argv[2], "w") as handle:
    handle.write(generation(keys, keys["signing_generation"])["public_key"] + "\n")
PYKEY
cargo run --quiet --locked \
  --manifest-path "${ROOT}/lycaon-den/src-tauri/Cargo.toml" \
  --example verify_updater_signature -- \
  "$1" "$2" "${WORKDIR}/signing.pub" "$3"
