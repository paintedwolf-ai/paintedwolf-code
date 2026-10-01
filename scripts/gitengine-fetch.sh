#!/usr/bin/env bash
# Fetch and verify the pinned Git toolchain.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PIN="${ROOT}/lycaon/config/gitengine/pin.yaml"
DEST="${ROOT}/lycaon-den/src-tauri/engine-root/gitengine"
TARGET="$(rustc --print host-tuple)"
PLATFORM_KEY=""
LAYOUT="unix"
SIZE_BUDGET_MB=40
case "${TARGET}" in
  aarch64-apple-darwin) PLATFORM_KEY="darwin-arm64" ;;
  x86_64-unknown-linux-gnu) PLATFORM_KEY="linux-amd64" ;;
  aarch64-unknown-linux-gnu) PLATFORM_KEY="linux-arm64" ;;
  x86_64-pc-windows-msvc)
    PLATFORM_KEY="windows-amd64"
    LAYOUT="windows"
    SIZE_BUDGET_MB=140
    ;;
  *)
    echo "error: gitengine:fetch does not support ${TARGET}" >&2
    exit 1
    ;;
esac

if [[ ! -f "${PIN}" ]]; then
  echo "error: missing pin manifest: ${PIN}" >&2
  exit 1
fi

# The pin schema is flat within each platform.
read_pin() {
  local key="$1"
  awk -v want="${key}" '
    $1 == want ":" {
      line = $0
      sub(/^[^:]+:[[:space:]]*/, "", line)
      gsub(/"/, "", line)
      print line
      exit
    }
  ' "${PIN}"
}

read_platform() {
  local key="$1"
  awk -v platform="${PLATFORM_KEY}" -v want="${key}" '
    $0 ~ "^[[:space:]]{2}" platform ":" { in_plat = 1; next }
    in_plat && /^[[:space:]]{2}[a-z0-9_-]+:/ { exit }
    in_plat && $1 == want ":" {
      line = $0
      sub(/^[^:]+:[[:space:]]*/, "", line)
      gsub(/"/, "", line)
      print line
      exit
    }
  ' "${PIN}"
}

git_path() {
  local root="$1"
  if [[ "${LAYOUT}" == "windows" ]]; then
    printf '%s\n' "${root}/cmd/git.exe"
  else
    printf '%s\n' "${root}/bin/git"
  fi
}

lfs_path() {
  local root="$1"
  if [[ "${LAYOUT}" == "windows" ]]; then
    printf '%s\n' "${root}/mingw64/libexec/git-core/git-lfs.exe"
  else
    printf '%s\n' "${root}/bin/git-lfs"
  fi
}

GIT_VERSION="$(read_pin git_version)"
LFS_VERSION="$(read_pin lfs_version)"
URL_TMPL="$(read_platform url)"
BUILD="$(read_platform build)"
REPORTED_VERSION="$(read_platform reported_version)"
EXPECTED_SHA="$(read_platform sha256)"

if [[ -z "${GIT_VERSION}" || -z "${LFS_VERSION}" || -z "${URL_TMPL}" || -z "${BUILD}" || -z "${REPORTED_VERSION}" || -z "${EXPECTED_SHA}" ]]; then
  echo "error: incomplete ${PLATFORM_KEY} pin in ${PIN}" >&2
  echo "  git_version=${GIT_VERSION:-<empty>} lfs_version=${LFS_VERSION:-<empty>} build=${BUILD:-<empty>} reported_version=${REPORTED_VERSION:-<empty>} sha256=${EXPECTED_SHA:-<empty>}" >&2
  exit 1
fi

URL="${URL_TMPL}"
URL="${URL//\{git_version\}/${GIT_VERSION}}"
URL="${URL//\{build\}/${BUILD}}"

# Remove files outside the shipped command surface.
prune_gitengine_tree() {
  local root="$1"
  rm -rf "${root}/share"
  find "${root}" -type d \( \
    -name 'zh-Hans' -o -name 'zh-Hant' -o -name 'pl' -o -name 'ja' -o \
    -name 'it' -o -name 'cs' -o -name 'ru' -o -name 'pt-BR' -o -name 'de' -o \
    -name 'ko' -o -name 'fr' -o -name 'es' -o -name 'tr' -o -name 'doc' \
  \) -prune -exec rm -rf {} + 2>/dev/null || true

  local core="${root}/libexec/git-core"
  if [[ -d "${core}" ]]; then
    find "${core}" -maxdepth 1 -type f \( \
      -name '*.dll' -o -name '*.pdb' -o \
      -name 'git-credential-manager*' -o -name 'createdump' -o \
      -name '*.deps.json' -o -name '*.runtimeconfig.json' -o \
      -name 'Avalonia*' -o -name 'Tmds*' -o \
      -name 'libSkiaSharp*' -o -name 'libHarfBuzzSharp*' -o \
      -name 'libAvalonia*' -o -name 'libcoreclr*' -o -name 'libclrjit*' -o \
      -name 'libmscor*' -o -name 'libhostfxr*' -o -name 'libhostpolicy*' -o \
      -name 'libnethost*' -o -name 'libSystem.*.dylib' -o \
      -name 'libclrgc.dylib' -o \
      -name 'Microsoft.*' -o -name 'System.*' -o -name 'Newtonsoft.*' -o \
      -name 'HarfBuzzSharp.*' -o -name 'SkiaSharp.*' -o \
      -name 'scalar' -o -name 'git-scalar' -o \
      -name 'git-daemon' -o -name 'git-shell' -o -name 'git-http-backend' -o \
      -name 'git-imap-send' -o -name 'git-http-push' -o -name 'git-http-fetch' -o \
      -name 'git-sh-i18n--envsubst' \
    \) -delete 2>/dev/null || true

    rm -rf "${core}/mergetools"
    find "${core}" -maxdepth 1 -type f \( \
      -name 'git-cvsserver' -o -name 'git-cvsimport' -o -name 'git-cvsexportcommit' -o \
      -name 'git-archimport' -o -name 'git-instaweb' -o -name 'git-send-email' -o \
      -name 'git-request-pull' -o -name 'git-quiltimport' -o -name 'git-web--browse' -o \
      -name 'uninstall.sh' \
    \) -delete 2>/dev/null || true
  fi
}

prune_windows_gitengine_tree() {
  local root="$1"
  # Runtime helpers use this directory topology.
  rm -rf \
    "${root}/mingw64/doc" \
    "${root}/mingw64/share/bash-completion" \
    "${root}/mingw64/share/doc"
  rm -f \
    "${root}/cmd/scalar.exe" \
    "${root}/mingw64/bin/scalar.exe" \
    "${root}/mingw64/bin/git-credential-helper-selector.exe" \
    "${root}/mingw64/bin/git-credential-manager.exe" \
    "${root}/mingw64/bin/git-credential-manager.exe.config"
}

# Keep only executable helpers used by the command surface.
shape_git_exec_helpers() {
  local root="$1"
  local core="${root}/libexec/git-core"
  local git_bin="${core}/git"
  [[ -x "${git_bin}" ]] || return 0

  find "${core}" -maxdepth 1 -type l -lname 'git' -delete

  local f base
  while IFS= read -r -d '' f; do
    base="$(basename "${f}")"
    case "${base}" in
      git-lfs|git-remote-http) continue ;;
    esac
    if cmp -s "${f}" "${git_bin}"; then
      rm -f "${f}"
    fi
  done < <(find "${core}" -maxdepth 1 -type f -name 'git-*' -print0)

  # Resource packaging dereferences symlinks.
  rm -f "${core}/git-remote-https" "${core}/git-remote-ftp" "${core}/git-remote-ftps"
  cp -f "${core}/git-remote-http" "${core}/git-remote-https"
  chmod 755 "${core}/git-remote-https"
}

place_git_lfs() {
  local root="$1"
  local core_lfs="${root}/libexec/git-core/git-lfs"
  local bin_lfs="${root}/bin/git-lfs"
  mkdir -p "${root}/bin"

  local src=""
  if [[ -e "${core_lfs}" ]]; then
    src="${core_lfs}"
  elif [[ -e "${bin_lfs}" ]]; then
    src="${bin_lfs}"
  fi
  if [[ -z "${src}" ]]; then
    echo "error: archive missing git-lfs" >&2
    exit 1
  fi

  local tmp="${root}/bin/.git-lfs.$$"
  # Materialize the target before removing its source link.
  cp -f "${src}" "${tmp}"
  rm -f "${bin_lfs}" "${core_lfs}"
  mv "${tmp}" "${bin_lfs}"
  chmod 755 "${bin_lfs}"
  if [[ ! -x "${bin_lfs}" || -L "${bin_lfs}" ]]; then
    echo "error: staged tree missing regular bin/git-lfs" >&2
    exit 1
  fi
}

place_git_bin() {
  local root="$1"
  local core_git="${root}/libexec/git-core/git"
  local bin_git="${root}/bin/git"
  [[ -x "${core_git}" ]] || {
    echo "error: staged tree missing libexec/git-core/git" >&2
    exit 1
  }
  mkdir -p "${root}/bin"
  if [[ -f "${bin_git}" ]] && cmp -s "${bin_git}" "${core_git}"; then
    rm -f "${bin_git}"
  fi
  if [[ ! -e "${bin_git}" ]]; then
    ln -s ../libexec/git-core/git "${bin_git}"
  fi
  rm -f "${root}/bin/scalar" "${root}/libexec/git-core/scalar" "${root}/libexec/git-core/git-scalar"
}

assert_gitengine_budget() {
  local root="$1"
  local size_bytes size_mb
  size_bytes="$(du -sk "${root}" | awk '{print $1 * 1024}')"
  size_mb="$(( size_bytes / 1024 / 1024 ))"
  if (( size_bytes > SIZE_BUDGET_MB * 1024 * 1024 )); then
    echo "error: extracted gitengine tree is ${size_mb} MB (budget ${SIZE_BUDGET_MB} MB) after prune" >&2
    exit 1
  fi
  if [[ "${LAYOUT}" == "unix" ]]; then
    local status="${root}/libexec/git-core/git-status"
    if [[ -e "${status}" ]]; then
      echo "error: ${status} must not exist after prune (Tauri would copy it as a full git binary)" >&2
      exit 1
    fi
    if [[ -e "${root}/libexec/git-core/libclrgc.dylib" ]]; then
      echo "error: libclrgc.dylib must be pruned (GCM residue)" >&2
      exit 1
    fi
  fi
  printf '%s\n' "${size_mb}"
}

codesign_gitengine_machos() {
  local root="$1"
  if ! command -v codesign >/dev/null 2>&1; then
    return 0
  fi
  # Helpers launch independently and require their own signatures.
  find "${root}" -type f -print0 \
    | while IFS= read -r -d '' f; do
        if file -b "${f}" 2>/dev/null | grep -q 'Mach-O'; then
          codesign -s - --force "${f}" >/dev/null 2>&1 || true
        fi
      done
}

finalize_gitengine_tree() {
  local root="$1"
  if [[ "${LAYOUT}" == "windows" ]]; then
    prune_windows_gitengine_tree "${root}"
  else
    prune_gitengine_tree "${root}"
    shape_git_exec_helpers "${root}"
    place_git_lfs "${root}"
    place_git_bin "${root}"
  fi
  if [[ ! -f "$(git_path "${root}")" ]]; then
    echo "error: staged tree missing $(git_path "${root}")" >&2
    exit 1
  fi
  if [[ ! -f "$(lfs_path "${root}")" ]]; then
    echo "error: staged tree missing $(lfs_path "${root}")" >&2
    exit 1
  fi
  assert_gitengine_budget "${root}"
}

verify_gitengine_versions() {
  local root="$1" git_output git_found lfs_output lfs_found
  git_output="$("$(git_path "${root}")" --version)"
  git_found="$(printf '%s\n' "${git_output}" | awk '$1 == "git" && $2 == "version" { print $3; exit }')"
  if [[ "${git_found}" != "${REPORTED_VERSION}" ]]; then
    echo "error: ${PLATFORM_KEY} Git reports ${git_found:-<empty>}; expected ${REPORTED_VERSION}" >&2
    exit 1
  fi
  lfs_output="$("$(lfs_path "${root}")" version)"
  lfs_found="$(printf '%s\n' "${lfs_output}" | sed -n 's#^git-lfs/\([^[:space:]]*\).*$#\1#p')"
  if [[ "${lfs_found}" != "${LFS_VERSION}" ]]; then
    echo "error: ${PLATFORM_KEY} Git LFS reports ${lfs_found:-<empty>}; expected ${LFS_VERSION}" >&2
    exit 1
  fi
}

if [[ -f "${DEST}/VERSION" ]]; then
  CURRENT="$(tr -d '[:space:]' < "${DEST}/VERSION")"
  if [[ "${CURRENT}" == "${GIT_VERSION}" && -e "$(git_path "${DEST}")" && -e "$(lfs_path "${DEST}")" ]]; then
    echo "gitengine:fetch — already at ${GIT_VERSION}; re-applying prune invariants" >&2
    SIZE_MB="$(finalize_gitengine_tree "${DEST}")"
    verify_gitengine_versions "${DEST}"
    codesign_gitengine_machos "${DEST}"
    echo "gitengine:fetch — staged ${DEST#${ROOT}/} (git ${GIT_VERSION}, ~${SIZE_MB} MB)" >&2
    exit 0
  fi
fi

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/gitengine-fetch.XXXXXX")"
cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

ARCHIVE="${TMP_DIR}/dugite.tar.gz"
EXTRACT="${TMP_DIR}/extract"
mkdir -p "${EXTRACT}"

echo "gitengine:fetch — downloading ${URL}" >&2
curl -fsSL -o "${ARCHIVE}" "${URL}"

ACTUAL_SHA="$(shasum -a 256 "${ARCHIVE}" | awk '{print tolower($1)}')"
if [[ "${ACTUAL_SHA}" != "$(printf '%s' "${EXPECTED_SHA}" | tr '[:upper:]' '[:lower:]')" ]]; then
  echo "error: gitengine sha256 mismatch — deleting download" >&2
  echo "  got:  ${ACTUAL_SHA}" >&2
  echo "  want: ${EXPECTED_SHA}" >&2
  rm -f "${ARCHIVE}"
  exit 1
fi

echo "gitengine:fetch — extracting verified archive" >&2
tar -xzf "${ARCHIVE}" -C "${EXTRACT}"

SRC="${EXTRACT}"
if [[ ! -f "$(git_path "${SRC}")" ]]; then
  echo "error: archive layout missing $(git_path "${SRC}") for ${PLATFORM_KEY}" >&2
  exit 1
fi

STAGE="${TMP_DIR}/stage"
mkdir -p "${STAGE}"
# Preserve hardlinks while shaping the payload.
cp -a "${SRC}/." "${STAGE}/"

printf '%s\n' "${GIT_VERSION}" > "${STAGE}/VERSION"
SIZE_MB="$(finalize_gitengine_tree "${STAGE}")"
verify_gitengine_versions "${STAGE}"

rm -rf "${DEST}"
mkdir -p "$(dirname "${DEST}")"
mv "${STAGE}" "${DEST}"

codesign_gitengine_machos "${DEST}"

echo "gitengine:fetch — staged ${DEST#${ROOT}/} (git ${GIT_VERSION}, ~${SIZE_MB} MB)" >&2
