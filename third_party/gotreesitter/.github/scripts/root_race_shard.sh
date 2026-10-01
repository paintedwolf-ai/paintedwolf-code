#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 validate <shard-count> | names <shard-index> <shard-count> | regex <shard-index> <shard-count> | isolated-names | isolated-json" >&2
  exit 2
}

require_uint() {
  local value="$1"
  [[ "$value" =~ ^[0-9]+$ ]] || usage
}

load_isolated_targets() {
  local isolated_file="${ROOT_RACE_ISOLATED_TARGETS_FILE:-.github/scripts/root_race_isolated_targets.txt}"
  mapfile -t isolated_targets < <(
    awk 'NF && $1 !~ /^#/ { print $1 }' "$isolated_file" |
      LC_ALL=C sort
  )
  if [ "${#isolated_targets[@]}" -eq 0 ]; then
    echo "root race isolated target manifest is empty: $isolated_file" >&2
    exit 1
  fi

  isolated_lookup=()
  local name
  for name in "${isolated_targets[@]}"; do
    if [[ ! "$name" =~ ^(Test|Example|Fuzz)[A-Za-z0-9_]+$ ]]; then
      echo "root race isolated target is not a valid Go target name: $name" >&2
      exit 1
    fi
    if [[ -n "${isolated_lookup[$name]:-}" ]]; then
      echo "root race isolated target is duplicated: $name" >&2
      exit 1
    fi
    isolated_lookup["$name"]=1
  done
}

load_targets() {
  mapfile -t root_targets < <(
    go test -race . -list '^(Test|Example|Fuzz)' |
      awk '/^(Test|Example|Fuzz)/ { print }' |
      LC_ALL=C sort -u
  )
  if [ "${#root_targets[@]}" -eq 0 ]; then
    echo "root race shard selector found no top-level tests, examples, or fuzz targets" >&2
    exit 1
  fi
  load_isolated_targets
}

validate_partition() {
  local shard_count="$1"
  local -a counts=()
  local -A seen=()
  local position regular_position=0 shard name

  for ((shard = 0; shard < shard_count; shard++)); do
    counts[shard]=0
  done
  for position in "${!root_targets[@]}"; do
    name="${root_targets[position]}"
    if [[ -n "${isolated_lookup[$name]:-}" ]]; then
      seen["$name"]=$(( ${seen["$name"]:-0} + 1 ))
      continue
    fi
    shard=$((regular_position % shard_count))
    counts[shard]=$((counts[shard] + 1))
    seen["$name"]=$(( ${seen["$name"]:-0} + 1 ))
    regular_position=$((regular_position + 1))
  done

  for name in "${isolated_targets[@]}"; do
    if [[ -z "${seen[$name]:-}" ]]; then
      echo "root race isolated target is not a top-level test, example, or fuzz target: $name" >&2
      exit 1
    fi
  done

  for name in "${root_targets[@]}"; do
    if [ "${seen["$name"]}" -ne 1 ]; then
      echo "root race partition assigned $name ${seen["$name"]} times" >&2
      exit 1
    fi
  done

  local min="${counts[0]}"
  local max="${counts[0]}"
  for count in "${counts[@]}"; do
    (( count < min )) && min="$count"
    (( count > max )) && max="$count"
  done
  if (( max - min > 1 )); then
    echo "root race partition is not count-balanced: ${counts[*]}" >&2
    exit 1
  fi
  echo "root race partition valid: ${#root_targets[@]} tests/examples/fuzz targets across ${shard_count} regular shards (${counts[*]}) and ${#isolated_targets[@]} isolated lanes" >&2
}

selected_names() {
  local shard_index="$1"
  local shard_count="$2"
  local position regular_position=0
  for position in "${!root_targets[@]}"; do
    if [[ -n "${isolated_lookup[${root_targets[position]}]:-}" ]]; then
      continue
    fi
    if (( regular_position % shard_count == shard_index )); then
      printf '%s\n' "${root_targets[position]}"
    fi
    regular_position=$((regular_position + 1))
  done
}

print_regex() {
  local -a names=("$@")
  local pattern
  pattern="$(IFS='|'; echo "${names[*]}")"
  printf '^(%s)$\n' "$pattern"
}

print_json() {
  local separator="" name
  printf '['
  for name in "$@"; do
    printf '%s"%s"' "$separator" "$name"
    separator=','
  done
  printf ']\n'
}

declare -a root_targets isolated_targets
declare -A isolated_lookup

mode="${1:-}"
case "$mode" in
  validate)
    [ "$#" -eq 2 ] || usage
    require_uint "$2"
    (( $2 > 0 )) || usage
    load_targets
    validate_partition "$2"
    ;;
  names|regex)
    [ "$#" -eq 3 ] || usage
    require_uint "$2"
    require_uint "$3"
    (( $3 > 0 && $2 < $3 )) || usage
    load_targets
    validate_partition "$3"
    mapfile -t selected < <(selected_names "$2" "$3")
    if [ "${#selected[@]}" -eq 0 ]; then
      echo "root race shard $2/$3 is empty" >&2
      exit 1
    fi
    if [ "$mode" = "names" ]; then
      printf '%s\n' "${selected[@]}"
      exit 0
    fi
    print_regex "${selected[@]}"
    ;;
  isolated-names)
    [ "$#" -eq 1 ] || usage
    load_targets
    validate_partition 1
    printf '%s\n' "${isolated_targets[@]}"
    ;;
  isolated-json)
    [ "$#" -eq 1 ] || usage
    load_isolated_targets
    print_json "${isolated_targets[@]}"
    ;;
  *)
    usage
    ;;
esac
