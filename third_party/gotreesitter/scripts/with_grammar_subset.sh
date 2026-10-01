#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

LANGS=""
OVERRIDE_DIR=""
BLOB_DIR=""
JOBS="1"
PROCS="1"
SHOW_ENV="0"

usage() {
  cat <<'EOF'
Usage: with_grammar_subset.sh --langs LANG[,LANG...] [options] [-- command...]

Run a command with a focused grammar_subset build and serial host settings to
reduce WSL memory pressure while iterating on a small grammar set.

Options:
  --langs LIST         Comma-separated built-in grammar names to compile
  --override-dir DIR   Set GOTREESITTER_GRAMMARGEN_BLOB_DIR to a local blob dir
  --blob-dir DIR       Set GOTREESITTER_GRAMMAR_BLOB_DIR (default: grammars/grammar_blobs)
  --jobs N             Go package parallelism via -p=N (default: 1)
  --procs N            GOMAXPROCS value (default: 1)
  --show-env           Print effective settings before exec
  -h, --help           Show this help

Examples:
  scripts/with_grammar_subset.sh --langs fortran
  scripts/with_grammar_subset.sh --langs fortran --override-dir .parity_seed/blobs -- \
    go test ./grammars -run TestGrammarSubsetCanParseSmokeSampleForCompiledLanguage -count=1
EOF
}

abs_dir() {
  local dir="$1"
  dir="${dir/#\~/$HOME}"
  if [[ -d "$dir" ]]; then
    (cd "$dir" && pwd)
  else
    local parent
    parent="$(dirname "$dir")"
    if [[ -d "$parent" ]]; then
      printf '%s/%s\n' "$(cd "$parent" && pwd)" "$(basename "$dir")"
    else
      printf '%s\n' "$dir"
    fi
  fi
}

canonical_lang() {
  case "$1" in
    c_lang) echo "c" ;;
    go_lang) echo "go" ;;
    gitcommit_gbprod) echo "gitcommit" ;;
    *) echo "$1" ;;
  esac
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --langs)
      LANGS="$2"
      shift 2
      ;;
    --override-dir)
      OVERRIDE_DIR="$2"
      shift 2
      ;;
    --blob-dir)
      BLOB_DIR="$2"
      shift 2
      ;;
    --jobs)
      JOBS="$2"
      shift 2
      ;;
    --procs)
      PROCS="$2"
      shift 2
      ;;
    --show-env)
      SHOW_ENV="1"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      break
      ;;
    *)
      break
      ;;
  esac
done

if [[ -z "$LANGS" ]]; then
  echo "--langs is required" >&2
  usage >&2
  exit 2
fi

if [[ -z "$BLOB_DIR" && -d "$REPO_ROOT/grammars/grammar_blobs" ]]; then
  BLOB_DIR="$REPO_ROOT/grammars/grammar_blobs"
fi
if [[ -z "$OVERRIDE_DIR" && -d "$REPO_ROOT/.parity_seed/blobs" ]]; then
  OVERRIDE_DIR="$REPO_ROOT/.parity_seed/blobs"
fi

LANG_SET=""
TAG_LIST="grammar_subset,grammar_blobs_external"
LANG_COUNT=0
declare -A seen_langs=()

IFS=',' read -r -a raw_langs <<< "$LANGS"
for raw in "${raw_langs[@]}"; do
  lang="$(printf '%s' "$raw" | tr '[:upper:]' '[:lower:]')"
  lang="${lang//[[:space:]]/}"
  lang="$(canonical_lang "$lang")"
  if [[ -z "$lang" ]]; then
    continue
  fi
  if [[ -n "${seen_langs[$lang]:-}" ]]; then
    continue
  fi
  seen_langs[$lang]=1
  if [[ -n "$LANG_SET" ]]; then
    LANG_SET+=","
  fi
  LANG_SET+="$lang"
  tag_lang="$(printf '%s' "$lang" | sed 's/[^a-z0-9]/_/g')"
  TAG_LIST+=",grammar_subset_${tag_lang}"
  LANG_COUNT=$((LANG_COUNT + 1))
done

if [[ -z "$LANG_SET" ]]; then
  echo "no valid languages parsed from --langs" >&2
  exit 2
fi

if [[ -n "$BLOB_DIR" ]]; then
  export GOTREESITTER_GRAMMAR_BLOB_DIR
  GOTREESITTER_GRAMMAR_BLOB_DIR="$(abs_dir "$BLOB_DIR")"
fi
if [[ -n "$OVERRIDE_DIR" ]]; then
  export GOTREESITTER_GRAMMARGEN_BLOB_DIR
  GOTREESITTER_GRAMMARGEN_BLOB_DIR="$(abs_dir "$OVERRIDE_DIR")"
fi

export GOTREESITTER_GRAMMAR_SET="$LANG_SET"
export GOTREESITTER_GRAMMAR_CACHE_LIMIT="${GOTREESITTER_GRAMMAR_CACHE_LIMIT:-$LANG_COUNT}"
export GOMAXPROCS="${GOMAXPROCS:-$PROCS}"

GOFLAGS_STR="${GOFLAGS:-}"
if [[ -n "$GOFLAGS_STR" ]]; then
  GOFLAGS_STR+=" "
fi
GOFLAGS_STR+="-tags=$TAG_LIST -p=$JOBS"
export GOFLAGS="$GOFLAGS_STR"

if [[ "$SHOW_ENV" == "1" ]]; then
  echo "GOTREESITTER_GRAMMAR_SET=$GOTREESITTER_GRAMMAR_SET"
  if [[ -n "${GOTREESITTER_GRAMMAR_BLOB_DIR:-}" ]]; then
    echo "GOTREESITTER_GRAMMAR_BLOB_DIR=$GOTREESITTER_GRAMMAR_BLOB_DIR"
  fi
  if [[ -n "${GOTREESITTER_GRAMMARGEN_BLOB_DIR:-}" ]]; then
    echo "GOTREESITTER_GRAMMARGEN_BLOB_DIR=$GOTREESITTER_GRAMMARGEN_BLOB_DIR"
  fi
  echo "GOTREESITTER_GRAMMAR_CACHE_LIMIT=$GOTREESITTER_GRAMMAR_CACHE_LIMIT"
  echo "GOMAXPROCS=$GOMAXPROCS"
  echo "GOFLAGS=$GOFLAGS"
fi

if [[ $# -eq 0 ]]; then
  set -- go test ./grammars -count=1
fi

cd "$REPO_ROOT"
exec "$@"
