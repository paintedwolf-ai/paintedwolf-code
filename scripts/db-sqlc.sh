#!/usr/bin/env bash
# Generate / check / vet sqlc queries for lycaon/internal/db (schema.sql SSOT).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DB_DIR="$ROOT/lycaon/internal/db"
# shellcheck source=artifact-paths.sh
source "$(dirname "$0")/artifact-paths.sh"
# shellcheck source=snapshot-publish.sh
source "$(dirname "$0")/snapshot-publish.sh"

python3 "$ROOT/scripts/analysis_tools.py" ensure sqlc

sqlc_run() {
  "${PW_BIN_DIR}/sqlc" "$@"
}

run_generate() {
  local stage before after existing generated rel
  stage="$(mktemp -d "${TMPDIR:-/tmp}/db-sqlc.XXXXXX")"
  trap "rm -rf '$stage'; snapshot_publish_finish" EXIT
  before="$(db_source_digest)"
  cp -R "$DB_DIR" "$stage/db"
  (
    cd "$stage/db"
    sqlc_run generate
  )
  after="$(db_source_digest)"
  if [[ "$before" != "$after" ]]; then
    echo "sqlc inputs changed while generation was running; nothing was published" >&2
    exit 1
  fi
  existing="$(generated_files "$DB_DIR")"
  generated="$(generated_files "$stage/db")"
  while IFS= read -r rel; do
    [[ -z "$rel" ]] && continue
    if [[ ! -f "$stage/db/$rel" ]]; then
      snapshot_remove_file "$DB_DIR/$rel"
    fi
  done <<< "$existing"
  while IFS= read -r rel; do
    [[ -z "$rel" ]] && continue
    snapshot_publish_file "$stage/db/$rel" "$DB_DIR/$rel"
  done <<< "$generated"
  snapshot_publish_finish
}

db_source_digest() {
  (
    cd "$ROOT"
    find lycaon/internal/db/schema.sql lycaon/internal/db/sqlc.yaml \
      lycaon/internal/db/queries scripts/db-sqlc.sh scripts/sqlc-version.sh \
      -type f -print | LC_ALL=C sort | while IFS= read -r file; do shasum -a 256 "$file"; done
  ) | shasum -a 256 | awk '{print $1}'
}

generated_files() {
  local dir="$1"
  (
    cd "$dir"
    for f in db.go models.go; do
      [[ -f "$f" ]] && echo "$f"
    done
    for f in *.sql.go; do
      [[ -e "$f" ]] && echo "$f"
    done
  )
}

case "${1:-generate}" in
  generate)
    run_generate
    ;;
  check)
    tmp="$(mktemp -d "${TMPDIR:-/tmp}/db-sqlc-check-XXXXXX")"
    trap 'rm -rf "$tmp"' EXIT
    cp -R "$DB_DIR" "$tmp/db"
    (
      cd "$tmp/db"
      sqlc_run generate
    )
    # Compare the union to detect added and removed files.
    rels="$( { generated_files "$DB_DIR"; generated_files "$tmp/db"; } | sort -u )"
    while IFS= read -r rel; do
      [[ -z "$rel" ]] && continue
      committed="$DB_DIR/$rel"
      fresh="$tmp/db/$rel"
      if [[ ! -f "$fresh" ]]; then
        echo "db:sqlc drift: $rel is committed but sqlc no longer generates it — run ./task db:sqlc" >&2
        exit 1
      fi
      if [[ ! -f "$committed" ]]; then
        echo "db:sqlc drift: $rel is generated but not committed — run ./task db:sqlc and commit" >&2
        exit 1
      fi
      if ! diff -u "$committed" "$fresh"; then
        echo "db:sqlc drift in $rel — run ./task db:sqlc and commit generated files" >&2
        exit 1
      fi
    done <<< "$rels"
    echo "db:sqlc OK (no drift)"
    ;;
  vet)
    (
      cd "$DB_DIR"
      sqlc_run vet
    )
    echo "db:sqlc vet OK"
    ;;
  *)
    echo "usage: $0 [generate|check|vet]" >&2
    exit 2
    ;;
esac
