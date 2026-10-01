#!/usr/bin/env bash
# Stage and publish wire-enum codegen (OpenAPI fragments, pkg/api consts, Den
# tables) from docs/openapi/vocab/*.yaml.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=snapshot-publish.sh
source "$(dirname "$0")/snapshot-publish.sh"

case "${1:-generate}" in
  generate)
    stage="$(mktemp -d "${TMPDIR:-/tmp}/wire-enums.XXXXXX")"
    trap 'rm -rf "$stage"; snapshot_publish_finish' EXIT
    (
      cd "$ROOT/lycaon"
      go run ./cmd/codegen-wire-enums --repo-root .. --stage-dir "$stage"
    )
    while IFS= read -r rel; do
      [[ -z "$rel" ]] && continue
      snapshot_publish_file "$stage/$rel" "$ROOT/$rel"
    done <"$stage/manifest"
    snapshot_publish_finish
    ;;
  check)
    (
      cd "$ROOT/lycaon"
      go run ./cmd/codegen-wire-enums --repo-root .. --check
    )
    ;;
  *)
    echo "usage: $0 [generate|check]" >&2
    exit 2
    ;;
esac
