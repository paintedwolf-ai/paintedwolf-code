#!/usr/bin/env bash
# Fetch the exact catalog commits recorded in rules-provenance.yaml.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LYCAON="${ROOT}/lycaon"
SCANNERS="${LYCAON}/config/runtime/scanners"
VENDOR="${SCANNERS}/rules/vendor"
PATCHES="${SCANNERS}/rules/patches"
PROVENANCE="${SCANNERS}/rules-provenance.yaml"

MODE="sync"
ONLY=""
BUMP_ID=""
BUMP_REF=""

usage() {
  cat <<'USAGE'
usage: vendor-scan-rules.sh [--check | --bump id=REF] [--only id]
  No mode flag  Materialize catalogs at their pinned commits and apply local patches.
  --check       Verify vendored bytes against provenance without network access.
  --bump id=REF  Pin a catalog to a tag, branch, or commit.
  --only id     Select one catalog.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --check) MODE="check"; shift ;;
    --only) ONLY="$2"; shift 2 ;;
    --only=*) ONLY="${1#*=}"; shift ;;
    --bump)
      MODE="bump"
      [[ "$2" == *=* ]] || { echo "vendor-scan-rules: --bump takes id=REF" >&2; exit 2; }
      BUMP_ID="${2%%=*}"; BUMP_REF="${2#*=}"; shift 2 ;;
    --bump=*)
      MODE="bump"
      arg="${1#*=}"
      [[ "${arg}" == *=* ]] || { echo "vendor-scan-rules: --bump takes id=REF" >&2; exit 2; }
      BUMP_ID="${arg%%=*}"; BUMP_REF="${arg#*=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "vendor-scan-rules: unknown argument $1" >&2; exit 2 ;;
  esac
done

sha() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    sha256sum "$1" | awk '{print $1}'
  fi
}

sha_stdin() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 | awk '{print $1}'
  else
    sha256sum | awk '{print $1}'
  fi
}

sha_many() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$@"
  else
    sha256sum "$@"
  fi
}

# US (0x1f) separators preserve empty digest fields that IFS whitespace collapses.
#   id, upstream, ref, commit, license, tree_sha256, patch_sha256,
#   vendored_sha256, include(comma), paths(comma)
# Missing fields fail validation below.
read_vendor_pins() {
  awk '
    function val(s) { sub(/^[^:]*:[ \t]*/, "", s); gsub(/^"|"$/, "", s); return s }
    function flush() {
      if (id != "") {
        printf "%s\037%s\037%s\037%s\037%s\037%s\037%s\037%s\037%s\037%s\n",
          id, upstream, ref, commit, license, tree, patch, vendored, include, paths
      }
      id=""; upstream=""; ref=""; commit=""; license=""; tree=""; patch=""; vendored=""
      include=""; paths=""; list=""
    }
    /^vendors:[ \t]*$/ { in_v = 1; next }
    /^[^ \t#]/ { if (in_v) { flush(); in_v = 0 } }
    !in_v { next }
    /^[ \t]*#/ { next }
    /^  - id:/          { flush(); id = val($0); next }
    /^    upstream:/    { upstream = val($0); list = ""; next }
    /^    ref:/         { ref = val($0); list = ""; next }
    /^    commit:/      { commit = val($0); list = ""; next }
    /^    license:/     { license = val($0); list = ""; next }
    /^    tree_sha256:/ { tree = val($0); list = ""; next }
    /^    patch_sha256:/ { patch = val($0); list = ""; next }
    /^    vendored_sha256:/ { vendored = val($0); list = ""; next }
    /^    include:/     { list = "include"; next }
    /^    paths:/       { list = "paths"; next }
    /^      - / {
      item = $0
      sub(/^      - [ \t]*/, "", item)
      gsub(/^"|"$/, "", item)
      if (list == "include") { include = (include == "" ? item : include "," item) }
      else if (list == "paths") { paths = (paths == "" ? item : paths "," item) }
      next
    }
    END { flush() }
  ' "${PROVENANCE}"
}

# Hash regular file names and bytes in locale-stable order.
tree_digest() {
  local dir="$1"
  (
    cd "${dir}" || exit 1
    local -a files=()
    local f
    while IFS= read -r f; do files+=("${f}"); done < <(find . -type f -print | LC_ALL=C sort)
    if [[ ${#files[@]} -gt 0 ]]; then
      sha_many "${files[@]}"
    fi
  ) | sha_stdin
}

# An absent patch set has no digest.
patch_digest() {
  local dir="$1"
  [[ -d "${dir}" ]] || return 0
  find "${dir}" -type f -name '*.patch' -print -quit | grep -q . || return 0
  tree_digest "${dir}"
}

# Apply patches in name order and fail when the staged source diverges.
apply_patches() {
  local id="$1" stage="$2" dir="${PATCHES}/${id}" pf
  [[ -d "${dir}" ]] || return 0
  for pf in "${dir}"/*.patch; do
    [[ -e "${pf}" ]] || continue
    # The discovery ceiling keeps patch paths relative to the staging directory.
    if ! ( cd "${stage}" && GIT_CEILING_DIRECTORIES="$(dirname "${stage}")" \
             git apply --unsafe-paths --directory=. "${pf}" ); then
      echo "vendor-scan-rules: ${id} patch ${pf##*/} does not apply at ${PIN_COMMIT[$(pin_index "${id}")]:0:12}" >&2
      echo "  rewrite or retire the patch — do not vendor around it" >&2
      return 1
    fi
  done
}

# Only regular files have reproducible tree digests and bundle contents.
assert_tree_clean() {
  local dir="$1" label="$2" bad=0

  if find "${dir}" -name '.git' -print -quit | grep -q .; then
    echo "vendor-scan-rules: ${label} contains a .git entry — git would record the whole catalog as a submodule pointer and its rules would not ship" >&2
    bad=1
  fi
  local link
  while IFS= read -r link; do
    echo "vendor-scan-rules: ${label} contains a symlink: ${link#"${dir}"/}" >&2
    bad=1
  done < <(find "${dir}" -type l)

  local ent
  while IFS= read -r ent; do
    echo "vendor-scan-rules: ${label} contains a non-regular file: ${ent#"${dir}"/}" >&2
    bad=1
  done < <(find "${dir}" ! -type f ! -type d ! -type l)

  if find "${dir}" -name '*
*' -print -quit | grep -q .; then
    echo "vendor-scan-rules: ${label} contains a file name with a newline" >&2
    bad=1
  fi

  return "${bad}"
}

# Fetches validate objects with hooks, submodules, and prompts disabled.
fetch_commit() {
  local dest="$1" url="$2" commit="$3"

  case "${url}" in
    https://*) ;;
    *) echo "vendor-scan-rules: refusing non-https upstream ${url}" >&2; return 1 ;;
  esac

  git init -q "${dest}"
  git -C "${dest}" remote add origin "${url}"
  if ! GIT_TERMINAL_PROMPT=0 git -C "${dest}" \
      -c core.hooksPath=/dev/null \
      -c transfer.fsckobjects=true \
      -c fetch.fsckobjects=true \
      fetch -q --depth 1 --no-tags --no-recurse-submodules origin "${commit}" 2>/dev/null; then
    # Some remotes require fetching history before resolving a pinned commit.
    GIT_TERMINAL_PROMPT=0 git -C "${dest}" \
      -c core.hooksPath=/dev/null \
      -c transfer.fsckobjects=true \
      -c fetch.fsckobjects=true \
      fetch -q --no-tags --no-recurse-submodules origin
  fi
  git -C "${dest}" -c advice.detachedHead=false checkout -q "${commit}"
  git -C "${dest}" rev-parse HEAD
}

# Literal commit IDs resolve without a network request.
resolve_ref() {
  local url="$1" ref="$2"
  if [[ "${ref}" =~ ^[0-9a-f]{40}$ ]]; then
    printf '%s\n' "${ref}"
    return 0
  fi
  local line
  line="$(GIT_TERMINAL_PROMPT=0 git ls-remote "${url}" "refs/tags/${ref}^{}" "refs/tags/${ref}" "refs/heads/${ref}" | head -n 1)"
  if [[ -z "${line}" ]]; then
    echo "vendor-scan-rules: ${url} has no tag or branch ${ref}" >&2
    return 1
  fi
  printf '%s\n' "${line%%$'\t'*}"
}

# ---------------------------------------------------------------------------
# load and validate pins
# ---------------------------------------------------------------------------

if [[ ! -f "${PROVENANCE}" ]]; then
  echo "vendor-scan-rules: missing ${PROVENANCE}" >&2
  exit 1
fi

PIN_IDS=(); PIN_UPSTREAM=(); PIN_REF=(); PIN_COMMIT=(); PIN_LICENSE=(); PIN_TREE=()
PIN_PATCH=(); PIN_VENDORED=(); PIN_INCLUDE=(); PIN_PATHS=()
while IFS=$'\037' read -r p_id p_upstream p_ref p_commit p_license p_tree p_patch p_vendored p_include p_paths; do
  [[ -n "${p_id}" ]] || continue
  PIN_IDS+=("${p_id}")
  PIN_UPSTREAM+=("${p_upstream}")
  PIN_REF+=("${p_ref}")
  PIN_COMMIT+=("${p_commit}")
  PIN_LICENSE+=("${p_license}")
  PIN_TREE+=("${p_tree}")
  PIN_PATCH+=("${p_patch}")
  PIN_VENDORED+=("${p_vendored}")
  PIN_INCLUDE+=("${p_include}")
  PIN_PATHS+=("${p_paths}")
done < <(read_vendor_pins)

if [[ ${#PIN_IDS[@]} -eq 0 ]]; then
  echo "vendor-scan-rules: ${PROVENANCE} has no vendors: entries" >&2
  exit 1
fi

pin_index() {
  local want="$1" i
  for i in "${!PIN_IDS[@]}"; do
    if [[ "${PIN_IDS[$i]}" == "${want}" ]]; then
      printf '%s\n' "${i}"
      return 0
    fi
  done
  return 1
}

validate_pin() {
  local i="$1" bad=0
  local id="${PIN_IDS[$i]}"
  if [[ ! "${PIN_COMMIT[$i]}" =~ ^[0-9a-f]{40}$ ]]; then
    echo "vendor-scan-rules: ${id} commit is not a 40-hex SHA: '${PIN_COMMIT[$i]}'" >&2
    bad=1
  fi
  if [[ -z "${PIN_UPSTREAM[$i]}" || -z "${PIN_LICENSE[$i]}" || -z "${PIN_INCLUDE[$i]}" || -z "${PIN_PATHS[$i]}" ]]; then
    echo "vendor-scan-rules: ${id} pin is incomplete (upstream/license/include/paths)" >&2
    bad=1
  fi
  return "${bad}"
}

fail=0
for i in "${!PIN_IDS[@]}"; do
  validate_pin "${i}" || fail=1
done
[[ ${fail} -eq 0 ]] || exit 1

if [[ -n "${ONLY}" ]] && ! pin_index "${ONLY}" >/dev/null; then
  echo "vendor-scan-rules: no vendor id ${ONLY} in ${PROVENANCE}" >&2
  exit 2
fi

# ---------------------------------------------------------------------------
# --check: offline
# ---------------------------------------------------------------------------

if [[ "${MODE}" == "check" ]]; then
  fail=0
  for i in "${!PIN_IDS[@]}"; do
    id="${PIN_IDS[$i]}"
    dir="${VENDOR}/${id}"
    if [[ ! -d "${dir}" ]]; then
      echo "vendor-scan-rules: ${dir#"${ROOT}"/} is missing — run scripts/vendor-scan-rules.sh" >&2
      fail=1
      continue
    fi
    if [[ -z "${PIN_TREE[$i]}" ]]; then
      echo "vendor-scan-rules: ${id} has no tree_sha256 in the ledger — run scripts/vendor-scan-rules.sh --only ${id}" >&2
      fail=1
      continue
    fi
    assert_tree_clean "${dir}" "${id}" || fail=1

    got_patch="$(patch_digest "${PATCHES}/${id}")"
    if [[ "${got_patch}" != "${PIN_PATCH[$i]}" ]]; then
      echo "vendor-scan-rules: ${id} patch set does not match the ledger" >&2
      echo "  got:  ${got_patch:-<none>}" >&2
      echo "  want: ${PIN_PATCH[$i]:-<none>}" >&2
      echo "  re-run scripts/vendor-scan-rules.sh --only ${id} so the ledger records the patch set" >&2
      fail=1
    fi

    # Unpatched catalogs use the source digest as their shipped digest.
    want="${PIN_VENDORED[$i]:-${PIN_TREE[$i]}}"
    got="$(tree_digest "${dir}")"
    if [[ "${got}" != "${want}" ]]; then
      echo "vendor-scan-rules: ${id} vendored bytes do not match the ledger" >&2
      echo "  got:  ${got}" >&2
      echo "  want: ${want}  (commit ${PIN_COMMIT[$i]})" >&2
      if [[ -n "${PIN_PATCH[$i]}" ]]; then
        echo "  vendored rules are upstream plus rules/patches/${id}/ — edit the patch, not the rule" >&2
      else
        echo "  vendored rules are upstream's — patch them in rules/patches/${id}/, do not hand-edit" >&2
      fi
      fail=1
    fi
  done
  if [[ ${fail} -ne 0 ]]; then
    exit 1
  fi
  echo "ok: ${#PIN_IDS[@]} vendored rule catalog(s) match rules-provenance.yaml"
  exit 0
fi

# ---------------------------------------------------------------------------
# --bump: move one pin
# ---------------------------------------------------------------------------

if [[ "${MODE}" == "bump" ]]; then
  if ! BUMP_I="$(pin_index "${BUMP_ID}")"; then
    echo "vendor-scan-rules: no vendor id ${BUMP_ID} in ${PROVENANCE}" >&2
    exit 2
  fi
  NEW_COMMIT="$(resolve_ref "${PIN_UPSTREAM[$BUMP_I]}" "${BUMP_REF}")"
  echo "==> ${BUMP_ID}: ${PIN_COMMIT[$BUMP_I]:0:12} -> ${NEW_COMMIT:0:12} (${BUMP_REF})" >&2
  PIN_COMMIT[$BUMP_I]="${NEW_COMMIT}"
  PIN_REF[$BUMP_I]="${BUMP_REF}"
  PIN_TREE[$BUMP_I]=""
  PIN_VENDORED[$BUMP_I]=""
  ONLY="${BUMP_ID}"
fi

# ---------------------------------------------------------------------------
# sync: fetch each pin, verify, stage, digest
# ---------------------------------------------------------------------------

WORK="$(mktemp -d "${TMPDIR:-/tmp}/vendor-scan-rules.XXXXXX")"
trap 'rm -rf "${WORK}"' EXIT

mkdir -p "${VENDOR}"

for i in "${!PIN_IDS[@]}"; do
  id="${PIN_IDS[$i]}"
  if [[ -n "${ONLY}" && "${id}" != "${ONLY}" ]]; then
    continue
  fi

  echo "==> ${id} @ ${PIN_COMMIT[$i]:0:12} (${PIN_REF[$i]:-pinned})" >&2
  src="${WORK}/${id}"
  got_commit="$(fetch_commit "${src}" "${PIN_UPSTREAM[$i]}" "${PIN_COMMIT[$i]}")"
  if [[ "${got_commit}" != "${PIN_COMMIT[$i]}" ]]; then
    echo "vendor-scan-rules: ${id} resolved to ${got_commit}, pinned at ${PIN_COMMIT[$i]} — nothing written" >&2
    exit 1
  fi
  rm -rf "${src}/.git"

  # Reject symlinks and nested repositories before publishing the staged tree.
  stage="${WORK}/stage-${id}"
  rm -rf "${stage}"
  mkdir -p "${stage}"
  IFS=',' read -r -a includes <<< "${PIN_INCLUDE[$i]}"
  for rel in "${includes[@]}"; do
    if [[ "${rel}" == "." ]]; then
      cp -R "${src}/." "${stage}/"
      continue
    fi
    if [[ ! -e "${src}/${rel}" ]]; then
      echo "vendor-scan-rules: ${id} commit ${PIN_COMMIT[$i]:0:12} has no ${rel} — fix include: in the ledger" >&2
      exit 1
    fi
    mkdir -p "$(dirname "${stage}/${rel}")"
    cp -R "${src}/${rel}" "${stage}/${rel}"
  done
  rm -rf "${stage}/.git"

  if ! assert_tree_clean "${stage}" "${id} (staged from ${PIN_COMMIT[$i]:0:12})"; then
    echo "vendor-scan-rules: ${id} not vendored — staged tree rejected" >&2
    exit 1
  fi

  digest="$(tree_digest "${stage}")"
  if [[ -n "${PIN_TREE[$i]}" && "${digest}" != "${PIN_TREE[$i]}" ]]; then
    echo "vendor-scan-rules: ${id} at pinned commit ${PIN_COMMIT[$i]} now produces a different tree" >&2
    echo "  got:  ${digest}" >&2
    echo "  want: ${PIN_TREE[$i]}" >&2
    echo "  a pinned commit's bytes do not change — treat this as tampering, not as drift" >&2
    exit 1
  fi
  PIN_TREE[$i]="${digest}"

  # Patches apply to verified upstream bytes.
  apply_patches "${id}" "${stage}" || exit 1
  if ! assert_tree_clean "${stage}" "${id} (patched)"; then
    echo "vendor-scan-rules: ${id} not vendored — patched tree rejected" >&2
    exit 1
  fi
  PIN_PATCH[$i]="$(patch_digest "${PATCHES}/${id}")"
  vendored="$(tree_digest "${stage}")"
  PIN_VENDORED[$i]=""
  if [[ "${vendored}" != "${digest}" ]]; then
    PIN_VENDORED[$i]="${vendored}"
  fi

  rm -rf "${VENDOR}/${id}"
  mv "${stage}" "${VENDOR}/${id}"
done

# ---------------------------------------------------------------------------
# rewrite the vendors block; everything from lycaon: on is hand-authored
# ---------------------------------------------------------------------------

if ! grep -q '^lycaon:' "${PROVENANCE}"; then
  echo "vendor-scan-rules: ${PROVENANCE} has no lycaon: block — refusing to rewrite it" >&2
  exit 1
fi

{
  cat <<'EOF'
# First-party rules are CC-BY-4.0; vendored catalogs are MIT.
# tree_sha256 covers upstream bytes; vendored_sha256 covers patched bytes.

vendors:
EOF
  for i in "${!PIN_IDS[@]}"; do
    echo "  - id: ${PIN_IDS[$i]}"
    echo "    upstream: ${PIN_UPSTREAM[$i]}"
    echo "    ref: ${PIN_REF[$i]}"
    echo "    commit: \"${PIN_COMMIT[$i]}\""
    echo "    tree_sha256: \"${PIN_TREE[$i]}\""
    if [[ -n "${PIN_PATCH[$i]}" ]]; then
      echo "    patch_sha256: \"${PIN_PATCH[$i]}\""
    fi
    if [[ -n "${PIN_VENDORED[$i]}" ]]; then
      echo "    vendored_sha256: \"${PIN_VENDORED[$i]}\""
    fi
    echo "    license: ${PIN_LICENSE[$i]}"
    echo "    include:"
    IFS=',' read -r -a inc <<< "${PIN_INCLUDE[$i]}"
    for rel in "${inc[@]}"; do
      echo "      - ${rel}"
    done
    echo "    paths:"
    IFS=',' read -r -a pth <<< "${PIN_PATHS[$i]}"
    for rel in "${pth[@]}"; do
      echo "      - ${rel}"
    done
  done
  echo
  awk '/^lycaon:/{p=1} p' "${PROVENANCE}"
} > "${PROVENANCE}.new"
mv "${PROVENANCE}.new" "${PROVENANCE}"

echo "==> done — ${PROVENANCE#"${ROOT}"/} rewritten at verified pins" >&2
