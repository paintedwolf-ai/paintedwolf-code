#!/usr/bin/env bash
# Go tests that need the native Nix client or the Nix-built scanner. Each runs
# even when the other fails.
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
status=0

PW_NIX_REGISTRAR_REQUIRED=1 bash "${ROOT}/scripts/go-test-digest.sh" --full --name nix:registrar -- ./internal/configlayout -run '^TestNixRegistrar' -count=1 || status=1

system="$(uname -m)-linux"
flake="git+file://${ROOT}"
if scanner="$(nix --extra-experimental-features 'nix-command flakes' build --no-link --print-out-paths --no-write-lock-file \
    "${flake}#packages.${system}.scanner-original" "${flake}#packages.${system}.scanner-finalized")"; then
  original="$(sed -n 1p <<<"${scanner}")"
  finalized="$(sed -n 2p <<<"${scanner}")"
  PW_SCANNER_PRODUCER_DIRECTORY="${original}/artifact" PW_SCANNER_CONSUMER_RECIPE="${finalized}/consumer-recipe.json" bash "${ROOT}/scripts/go-test-digest.sh" --full --tags integration,consumer_scanners --name nix:scanner-consumer -- ./cmd/opengrep-artifact -run '^TestConsumerFinalizationNativeScanner$' -count=1 || status=1
else
  echo "native-tests: building the scanner failed" >&2
  status=1
fi
exit "${status}"
