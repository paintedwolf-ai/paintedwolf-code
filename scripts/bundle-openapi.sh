#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
# shellcheck source=redocly-cli.sh
source "$(dirname "$0")/redocly-cli.sh"
# shellcheck source=snapshot-publish.sh
source "$(dirname "$0")/snapshot-publish.sh"

ENTRY="docs/openapi/root.yaml"
OUT="docs/openapi.yaml"

run_bundle() {
  local dest="$1"
  redocly bundle "$ENTRY" -o "$dest"
}

case "${1:-bundle}" in
  bundle)
    tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/openapi-bundle.XXXXXX")"
    tmp="$tmp_dir/openapi.yaml"
    trap 'rm -rf "$tmp_dir"; snapshot_publish_finish' EXIT
    before="$(find docs/openapi -type f -print | LC_ALL=C sort | while IFS= read -r file; do shasum -a 256 "$file"; done | shasum -a 256 | awk '{print $1}')"
    if [[ -f "$OUT" ]]; then
      output_before="$(shasum -a 256 "$OUT" | awk '{print $1}')"
    else
      output_before="missing"
    fi
    run_bundle "$tmp"
    after="$(find docs/openapi -type f -print | LC_ALL=C sort | while IFS= read -r file; do shasum -a 256 "$file"; done | shasum -a 256 | awk '{print $1}')"
    if [[ "$before" != "$after" ]]; then
      echo "OpenAPI sources changed while bundling; nothing was published" >&2
      exit 1
    fi
    if [[ -f "$OUT" ]]; then
      output_after="$(shasum -a 256 "$OUT" | awk '{print $1}')"
    else
      output_after="missing"
    fi
    if [[ "$output_before" != "$output_after" ]]; then
      echo "OpenAPI output changed while bundling; nothing was published" >&2
      exit 1
    fi
    snapshot_publish_file "$tmp" "$OUT"
    snapshot_publish_finish
    ;;
  check)
    tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/openapi-bundle.XXXXXX")"
    tmp="$tmp_dir/openapi.yaml"
    trap 'rm -rf "$tmp_dir"' EXIT
    run_bundle "$tmp"
    if ! diff -u "$OUT" "$tmp"; then
      echo "openapi bundle drift: run ./task openapi:bundle and commit docs/openapi.yaml" >&2
      exit 1
    fi
    echo "openapi bundle OK (no drift)"
    ;;
  *)
    echo "usage: $0 [bundle|check]" >&2
    exit 2
    ;;
esac
