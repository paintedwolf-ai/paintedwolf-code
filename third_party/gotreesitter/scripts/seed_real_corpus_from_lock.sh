#!/usr/bin/env bash
set -euo pipefail

# Seed a real-corpus parity workspace with grammar repos pinned to the SHAs
# in grammars/languages.lock, so floor regeneration measures the same grammar
# vintage the shipped blobs were extracted from. Unpinned upstream HEADs mix
# grammar generations (a repo whose grammar.json has moved past its checked-in
# src/parser.c produces gen/ref divergence that is upstream skew, not parity),
# which is exactly what made historical floor runs irreproducible.
#
# Usage:
#   scripts/seed_real_corpus_from_lock.sh <dest-dir> [name ...]
#
# With no names, seeds every entry in languages.lock. Repos not present in
# the lock can be added by editing EXTRA_REPOS below (cloned at HEAD, and
# recorded as unpinned in the seed manifest).

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOCK="$REPO_ROOT/grammars/languages.lock"
DEST="${1:?usage: seed_real_corpus_from_lock.sh <dest-dir> [name ...]}"
shift || true
ONLY=("$@")

mkdir -p "$DEST"
MANIFEST="$DEST/SEED_MANIFEST.txt"
: > "$MANIFEST"

want() {
  local name="$1"
  if [[ ${#ONLY[@]} -eq 0 ]]; then
    return 0
  fi
  local n
  for n in "${ONLY[@]}"; do
    [[ "$n" == "$name" ]] && return 0
  done
  return 1
}

clone_pinned() {
  local name="$1" url="$2" sha="$3" dest="$DEST/$name"
  rm -rf "$dest"
  mkdir -p "$dest"
  git -C "$dest" init -q
  git -C "$dest" remote add origin "$url"
  if git -C "$dest" fetch -q --depth 1 origin "$sha" && git -C "$dest" checkout -q FETCH_HEAD; then
    echo "$name $url $sha" >> "$MANIFEST"
    echo "OK  $name @ ${sha:0:12}"
  else
    rm -rf "$dest"
    echo "FAIL $name $url $sha" >&2
    return 1
  fi
}

fail=0
while read -r name url sha _rest || [[ -n "$name" ]]; do
  [[ -z "$name" || "$name" == \#* ]] && continue
  want "$name" || continue
  clone_pinned "$name" "$url" "$sha" || fail=1
done < <(tr -d '\r' < "$LOCK")

echo "seed manifest: $MANIFEST"
exit "$fail"
