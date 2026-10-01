#!/usr/bin/env bash
# Prints the bialy cargo features for this host, or for the `<uname -s>/<uname -m>`
# argument. BIALY_FEATURES overrides; an empty line means CPU only.
set -euo pipefail
if [[ -n "${BIALY_FEATURES:-}" ]]; then
  printf '%s\n' "${BIALY_FEATURES}"
  exit 0
fi
case "${1:-$(uname -s)/$(uname -m)}" in
  Darwin/arm64) printf '%s\n' "metal,mlx" ;;
  *) printf '\n' ;;
esac
