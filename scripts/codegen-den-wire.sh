#!/usr/bin/env bash
# Stage and publish the complete OpenAPI client/server wire generation.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=snapshot-publish.sh
source "$(dirname "$0")/snapshot-publish.sh"
MODE="${1:-generate}"
if [[ "$MODE" != "generate" && "$MODE" != "check" ]]; then
  echo "usage: $0 [generate|check]" >&2
  exit 2
fi
stage="$(mktemp -d "${TMPDIR:-/tmp}/codegen-den-wire.XXXXXX")"
backup="$(mktemp -d "${TMPDIR:-/tmp}/codegen-den-wire-backup.XXXXXX")"
committed=0
publishing=0

outputs=(
  lycaon-den/src/api/types.ts
  lycaon-den/src/api/operations.generated.ts
  lycaon/internal/api/operations.generated.go
  lycaon/pkg/api/types.generated.go
)

output_digest() {
  (
    cd "$ROOT"
    for rel in "${outputs[@]}"; do
      if [[ -f "$rel" ]]; then
        shasum -a 256 "$rel"
      else
        printf 'missing  %s\n' "$rel"
      fi
    done
  ) | shasum -a 256 | awk '{print $1}'
}

cleanup() {
  set +e
  if [[ "$publishing" == "1" && "$committed" != "1" ]]; then
    for rel in "${outputs[@]}"; do
      if [[ -f "$backup/$rel" ]]; then
        mkdir -p "$ROOT/$(dirname "$rel")"
        cp "$backup/$rel" "$ROOT/$rel"
      else
        rm -f "$ROOT/$rel"
      fi
    done
  fi
  rm -rf "$stage" "$backup"
  # Restores happen inside the open publish window; close it last.
  snapshot_publish_finish
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

source_digest() {
  (
    cd "$ROOT"
    find docs/openapi.yaml lycaon-den/package.json lycaon/cmd/codegen-operations \
      scripts/codegen-den-types.sh \
      -type f -print | LC_ALL=C sort | while IFS= read -r file; do shasum -a 256 "$file"; done
  ) | shasum -a 256 | awk '{print $1}'
}

before="$(source_digest)"
outputs_before="$(output_digest)"
bash "$ROOT/scripts/codegen-den-types.sh" "$stage/lycaon-den/src/api/types.ts"
(
  cd "$ROOT/lycaon"
  go run ./cmd/codegen-operations --repo-root .. --output-root "$stage"
)
after="$(source_digest)"
if [[ "$before" != "$after" ]]; then
  echo "codegen inputs changed while generation was running; nothing was published" >&2
  exit 1
fi
if [[ "$outputs_before" != "$(output_digest)" ]]; then
  echo "wire outputs changed while generation was running; nothing was published" >&2
  exit 1
fi

for rel in "${outputs[@]}"; do
  if [[ "$MODE" == "check" ]]; then
    if ! diff -u "$ROOT/$rel" "$stage/$rel"; then
      echo "wire codegen drift: run ./task codegen:den-types" >&2
      exit 1
    fi
    continue
  fi
  if [[ -f "$ROOT/$rel" ]]; then
    mkdir -p "$backup/$(dirname "$rel")"
    cp "$ROOT/$rel" "$backup/$rel"
  fi
done
if [[ "$MODE" == "generate" ]]; then
  publishing=1
  for rel in "${outputs[@]}"; do
    snapshot_publish_file "$stage/$rel" "$ROOT/$rel"
  done
  committed=1
  snapshot_publish_finish
fi
if [[ "$MODE" == "check" ]]; then
  echo "codegen:den-types OK (one coherent wire generation)"
fi
