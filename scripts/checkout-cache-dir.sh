#!/usr/bin/env bash
# Diagnostic caches are isolated by checkout because their paths are absolute.
set -euo pipefail

if [[ -n "${PW_TEST_CHECKOUT_CACHE:-}" ]]; then
  printf '%s\n' "$PW_TEST_CHECKOUT_CACHE"
  exit 0
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
source "${ROOT}/scripts/sha256.sh"

case "$(uname -s)" in
  Darwin) base="${HOME}/Library/Caches" ;;
  *) base="${XDG_CACHE_HOME:-${HOME}/.cache}" ;;
esac

digest="$(printf '%s' "${ROOT}" | sha256_hex | cut -c1-12)"

printf '%s/painted-wolf-dev/checkouts/%s-%s\n' "${base}" "$(basename "${ROOT}")" "${digest}"
