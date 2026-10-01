#!/usr/bin/env bash
# Materialize the common-password list from its pinned source.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CATALOG="${ROOT}/lycaon/config/packs/painted-wolf/security/host/secret-mint"
VENDOR="${CATALOG}/vendor"
PROVENANCE="${CATALOG}/zxcvbn-provenance.yaml"

MODE="sync"
ONLY=""
BUMP_ID=""
BUMP_REF=""

usage() {
  cat <<'USAGE'
usage: vendor-zxcvbn-passwords.sh [--check | --bump id=REF] [--only id]
  No mode flag  Materialize password lists at their pinned commits.
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
      [[ "$2" == *=* ]] || { echo "vendor-zxcvbn-passwords: --bump takes id=REF" >&2; exit 2; }
      BUMP_ID="${2%%=*}"; BUMP_REF="${2#*=}"; shift 2 ;;
    --bump=*)
      MODE="bump"
      arg="${1#*=}"
      [[ "${arg}" == *=* ]] || { echo "vendor-zxcvbn-passwords: --bump takes id=REF" >&2; exit 2; }
      BUMP_ID="${arg%%=*}"; BUMP_REF="${arg#*=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "vendor-zxcvbn-passwords: unknown argument $1" >&2; exit 2 ;;
  esac
done

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
#   id, upstream, ref, commit, license, tree_sha256, include(comma)
read_vendor_pins() {
  awk '
    function val(s) { sub(/^[^:]*:[ \t]*/, "", s); gsub(/^"|"$/, "", s); return s }
    function flush() {
      if (id != "") {
        printf "%s\037%s\037%s\037%s\037%s\037%s\037%s\n",
          id, upstream, ref, commit, license, tree, include
      }
      id=""; upstream=""; ref=""; commit=""; license=""; tree=""; include=""; list=""
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
    /^    include:/     { list = "include"; next }
    /^      - / {
      item = $0
      sub(/^      - [ \t]*/, "", item)
      gsub(/^"|"$/, "", item)
      if (list == "include") { include = (include == "" ? item : include "," item) }
      next
    }
    END { flush() }
  ' "${PROVENANCE}"
}

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

assert_tree_clean() {
  local dir="$1" label="$2" bad=0

  if find "${dir}" -name '.git' -print -quit | grep -q .; then
    echo "vendor-zxcvbn-passwords: ${label} contains a .git entry" >&2
    bad=1
  fi
  local link
  while IFS= read -r link; do
    echo "vendor-zxcvbn-passwords: ${label} contains a symlink: ${link#"${dir}"/}" >&2
    bad=1
  done < <(find "${dir}" -type l)

  local ent
  while IFS= read -r ent; do
    echo "vendor-zxcvbn-passwords: ${label} contains a non-regular file: ${ent#"${dir}"/}" >&2
    bad=1
  done < <(find "${dir}" ! -type f ! -type d ! -type l)

  if find "${dir}" -name '*
*' -print -quit | grep -q .; then
    echo "vendor-zxcvbn-passwords: ${label} contains a file name with a newline" >&2
    bad=1
  fi

  return "${bad}"
}

fetch_commit() {
  local dest="$1" url="$2" commit="$3"

  case "${url}" in
    https://*) ;;
    *) echo "vendor-zxcvbn-passwords: refusing non-https upstream ${url}" >&2; return 1 ;;
  esac

  git init -q "${dest}"
  git -C "${dest}" remote add origin "${url}"
  if ! GIT_TERMINAL_PROMPT=0 git -C "${dest}" \
      -c core.hooksPath=/dev/null \
      -c transfer.fsckobjects=true \
      -c fetch.fsckobjects=true \
      fetch -q --depth 1 --no-tags --no-recurse-submodules origin "${commit}" 2>/dev/null; then
    GIT_TERMINAL_PROMPT=0 git -C "${dest}" \
      -c core.hooksPath=/dev/null \
      -c transfer.fsckobjects=true \
      -c fetch.fsckobjects=true \
      fetch -q --no-tags --no-recurse-submodules origin
  fi
  git -C "${dest}" -c advice.detachedHead=false checkout -q "${commit}"
  git -C "${dest}" rev-parse HEAD
}

resolve_ref() {
  local url="$1" ref="$2"
  if [[ "${ref}" =~ ^[0-9a-f]{40}$ ]]; then
    printf '%s\n' "${ref}"
    return 0
  fi
  local line
  line="$(GIT_TERMINAL_PROMPT=0 git ls-remote "${url}" "refs/tags/${ref}^{}" "refs/tags/${ref}" "refs/heads/${ref}" | head -n 1)"
  if [[ -z "${line}" ]]; then
    echo "vendor-zxcvbn-passwords: ${url} has no tag or branch ${ref}" >&2
    return 1
  fi
  printf '%s\n' "${line%%$'\t'*}"
}

# materialize_passwords writes a lowercase, unique, one-word-per-line list.
# Upstream data/passwords.txt is "word  count"; frequency_lists.coffee is not used.
materialize_passwords() {
  local src="$1" dest="$2"
  awk '
    {
      word = $1
      gsub(/^[ \t]+|[ \t]+$/, "", word)
      if (word == "" || word ~ /^#/) next
      print tolower(word)
    }
  ' "${src}" | LC_ALL=C sort -u > "${dest}"
  if [[ ! -s "${dest}" ]]; then
    echo "vendor-zxcvbn-passwords: materialized password list is empty" >&2
    return 1
  fi
}

if [[ ! -f "${PROVENANCE}" ]]; then
  echo "vendor-zxcvbn-passwords: missing ${PROVENANCE}" >&2
  exit 1
fi

PIN_IDS=(); PIN_UPSTREAM=(); PIN_REF=(); PIN_COMMIT=(); PIN_LICENSE=(); PIN_TREE=(); PIN_INCLUDE=()
while IFS=$'\037' read -r p_id p_upstream p_ref p_commit p_license p_tree p_include; do
  [[ -n "${p_id}" ]] || continue
  PIN_IDS+=("${p_id}")
  PIN_UPSTREAM+=("${p_upstream}")
  PIN_REF+=("${p_ref}")
  PIN_COMMIT+=("${p_commit}")
  PIN_LICENSE+=("${p_license}")
  PIN_TREE+=("${p_tree}")
  PIN_INCLUDE+=("${p_include}")
done < <(read_vendor_pins)

if [[ ${#PIN_IDS[@]} -eq 0 ]]; then
  echo "vendor-zxcvbn-passwords: ${PROVENANCE} has no vendors: entries" >&2
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
    echo "vendor-zxcvbn-passwords: ${id} commit is not a 40-hex SHA: '${PIN_COMMIT[$i]}'" >&2
    bad=1
  fi
  if [[ -z "${PIN_UPSTREAM[$i]}" || -z "${PIN_LICENSE[$i]}" || -z "${PIN_INCLUDE[$i]}" ]]; then
    echo "vendor-zxcvbn-passwords: ${id} pin is incomplete (upstream/license/include)" >&2
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
  echo "vendor-zxcvbn-passwords: no vendor id ${ONLY} in ${PROVENANCE}" >&2
  exit 2
fi

if [[ "${MODE}" == "check" ]]; then
  fail=0
  for i in "${!PIN_IDS[@]}"; do
    id="${PIN_IDS[$i]}"
    dir="${VENDOR}/${id}"
    if [[ ! -d "${dir}" ]]; then
      echo "vendor-zxcvbn-passwords: ${dir#"${ROOT}"/} is missing — run scripts/vendor-zxcvbn-passwords.sh" >&2
      fail=1
      continue
    fi
    if [[ -z "${PIN_TREE[$i]}" ]]; then
      echo "vendor-zxcvbn-passwords: ${id} has no tree_sha256 in the ledger — run scripts/vendor-zxcvbn-passwords.sh" >&2
      fail=1
      continue
    fi
    assert_tree_clean "${dir}" "${id}" || fail=1
    got="$(tree_digest "${dir}")"
    if [[ "${got}" != "${PIN_TREE[$i]}" ]]; then
      echo "vendor-zxcvbn-passwords: ${id} vendored bytes do not match the ledger" >&2
      echo "  got:  ${got}" >&2
      echo "  want: ${PIN_TREE[$i]}  (commit ${PIN_COMMIT[$i]})" >&2
      echo "  vendored lists are upstream's — change them upstream and --bump, do not hand-edit" >&2
      fail=1
    fi
  done
  if [[ ${fail} -ne 0 ]]; then
    exit 1
  fi
  echo "ok: ${#PIN_IDS[@]} vendored password catalog(s) match zxcvbn-provenance.yaml"
  exit 0
fi

if [[ "${MODE}" == "bump" ]]; then
  if ! BUMP_I="$(pin_index "${BUMP_ID}")"; then
    echo "vendor-zxcvbn-passwords: no vendor id ${BUMP_ID} in ${PROVENANCE}" >&2
    exit 2
  fi
  NEW_COMMIT="$(resolve_ref "${PIN_UPSTREAM[$BUMP_I]}" "${BUMP_REF}")"
  echo "==> ${BUMP_ID}: ${PIN_COMMIT[$BUMP_I]:0:12} -> ${NEW_COMMIT:0:12} (${BUMP_REF})" >&2
  PIN_COMMIT[$BUMP_I]="${NEW_COMMIT}"
  PIN_REF[$BUMP_I]="${BUMP_REF}"
  PIN_TREE[$BUMP_I]=""
  ONLY="${BUMP_ID}"
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/vendor-zxcvbn-passwords.XXXXXX")"
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
    echo "vendor-zxcvbn-passwords: ${id} resolved to ${got_commit}, pinned at ${PIN_COMMIT[$i]} — nothing written" >&2
    exit 1
  fi
  rm -rf "${src}/.git"

  stage="${WORK}/stage-${id}"
  rm -rf "${stage}"
  mkdir -p "${stage}"

  license_src=""
  passwords_src=""
  IFS=',' read -r -a includes <<< "${PIN_INCLUDE[$i]}"
  for rel in "${includes[@]}"; do
    if [[ ! -e "${src}/${rel}" ]]; then
      echo "vendor-zxcvbn-passwords: ${id} commit ${PIN_COMMIT[$i]:0:12} has no ${rel} — fix include: in the ledger" >&2
      exit 1
    fi
    base="$(basename "${rel}")"
    case "${base}" in
      LICENSE|LICENSE.txt|COPYING)
        license_src="${src}/${rel}"
        ;;
      passwords.txt)
        passwords_src="${src}/${rel}"
        ;;
      *)
        echo "vendor-zxcvbn-passwords: ${id} include ${rel} is not LICENSE or passwords.txt" >&2
        exit 1
        ;;
    esac
  done
  if [[ -z "${license_src}" || -z "${passwords_src}" ]]; then
    echo "vendor-zxcvbn-passwords: ${id} include: must name LICENSE and data/passwords.txt" >&2
    exit 1
  fi
  cp "${license_src}" "${stage}/LICENSE"
  materialize_passwords "${passwords_src}" "${stage}/passwords.txt"

  if ! assert_tree_clean "${stage}" "${id} (staged from ${PIN_COMMIT[$i]:0:12})"; then
    echo "vendor-zxcvbn-passwords: ${id} not vendored — staged tree rejected" >&2
    exit 1
  fi

  digest="$(tree_digest "${stage}")"
  if [[ -n "${PIN_TREE[$i]}" && "${digest}" != "${PIN_TREE[$i]}" ]]; then
    echo "vendor-zxcvbn-passwords: ${id} at pinned commit ${PIN_COMMIT[$i]} now produces a different tree" >&2
    echo "  got:  ${digest}" >&2
    echo "  want: ${PIN_TREE[$i]}" >&2
    echo "  a pinned commit's bytes do not change — treat this as tampering, not as drift" >&2
    exit 1
  fi
  PIN_TREE[$i]="${digest}"

  rm -rf "${VENDOR}/${id}"
  mv "${stage}" "${VENDOR}/${id}"
done

{
  cat <<'EOF'
# Pin and digest for the bundled common-password list.

vendors:
EOF
  for i in "${!PIN_IDS[@]}"; do
    echo "  - id: ${PIN_IDS[$i]}"
    echo "    upstream: ${PIN_UPSTREAM[$i]}"
    echo "    ref: ${PIN_REF[$i]}"
    echo "    commit: \"${PIN_COMMIT[$i]}\""
    echo "    tree_sha256: \"${PIN_TREE[$i]}\""
    echo "    license: ${PIN_LICENSE[$i]}"
    echo "    include:"
    IFS=',' read -r -a inc <<< "${PIN_INCLUDE[$i]}"
    for rel in "${inc[@]}"; do
      echo "      - ${rel}"
    done
  done
} > "${PROVENANCE}.new"
mv "${PROVENANCE}.new" "${PROVENANCE}"

echo "==> done — ${PROVENANCE#"${ROOT}"/} rewritten at verified pins" >&2
