#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cache_dir="$repo_root/.canopy"
cache_path="$cache_dir/index.json"
cache_head_path="$cache_dir/index.git-head"
canopy_bin="${CANOPY_BIN:-$HOME/.local/bin/canopy}"

if [[ ! -x "$canopy_bin" ]]; then
	echo "Canopy wrapper is not executable: $canopy_bin" >&2
	exit 127
fi

mkdir -p "$cache_dir"
temporary_cache="$(mktemp "$cache_dir/index.refresh.XXXXXX")"
temporary_head=""
cleanup() {
	rm -f -- "$temporary_cache"
	if [[ -n "$temporary_head" ]]; then
		rm -f -- "$temporary_head"
	fi
}
trap cleanup EXIT

if [[ -f "$cache_path" ]]; then
	cp -- "$cache_path" "$temporary_cache"
else
	rm -f -- "$temporary_cache"
fi

CANOPY_TIMEOUT="${CANOPY_TIMEOUT:-5m}" \
	"$canopy_bin" index build "$repo_root" \
	--incremental \
	--out "$temporary_cache"

"$canopy_bin" index validate "$repo_root" \
	--cache "$temporary_cache"

temporary_head="$(mktemp "$cache_dir/index.git-head.XXXXXX")"
git -C "$repo_root" rev-parse HEAD >"$temporary_head"
mv -- "$temporary_cache" "$cache_path"
mv -- "$temporary_head" "$cache_head_path"
trap - EXIT
