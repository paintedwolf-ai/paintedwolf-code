#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=snapshot-publish.sh
source "$ROOT/scripts/snapshot-publish.sh"

output="$ROOT/lycaon/internal/browser/embed/semantic_driver.js"
stage="$(mktemp "${output}.XXXXXX")"
trap 'rm -f "$stage"; snapshot_publish_finish' EXIT
bun build "$ROOT/lycaon-den/src/platform/semantic-driver/inject.ts" \
  --minify --target=browser --outfile="$stage"
snapshot_publish_file "$stage" "$output"
