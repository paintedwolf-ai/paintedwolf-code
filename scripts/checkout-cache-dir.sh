#!/usr/bin/env bash
# Diagnostic caches are isolated by checkout because their paths are absolute.
# Usage: checkout-cache-dir.sh [checkout]; the default is this script's checkout.
set -euo pipefail

if [[ $# -eq 0 && -n "${PW_TEST_CHECKOUT_CACHE:-}" ]]; then
  printf '%s\n' "$PW_TEST_CHECKOUT_CACHE"
  exit 0
fi

source "$(dirname "$0")/sha256.sh"
if [[ $# -eq 0 ]]; then
  ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
else
  # A source slot need not exist yet; its parent must.
  ROOT="$(cd "$(dirname "$1")" && pwd -P)/$(basename "$1")"
fi

case "$(uname -s)" in
  Darwin) base="${HOME}/Library/Caches" ;;
  *) base="${XDG_CACHE_HOME:-${HOME}/.cache}" ;;
esac

digest="$(printf '%s' "${ROOT}" | sha256_hex | cut -c1-12)"

printf '%s/painted-wolf-dev/checkouts/%s-%s\n' "${base}" "$(basename "${ROOT}")" "${digest}"
