#!/usr/bin/env bash
# Verify one native bundle and emit its immutable release set.
set -euo pipefail

if [[ -n "${LYCAON_OPENGREP_CANDIDATE:-}" ]]; then
  echo "error: local Opengrep candidates cannot be used in release packaging" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PLATFORM=""
GOOS=""
ARCH=""
VERSION=""
PACKAGE_EXTENSION=""
UPDATER_EXTENSION=""
OUTPUT=""

usage() {
  echo "Usage: release-stage-platform-artifacts.sh --platform KEY --goos OS --arch ARCH --version VERSION --package-extension EXT --updater-extension EXT --output DIR" >&2
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --platform) PLATFORM="${2:-}"; shift 2 ;;
    --goos) GOOS="${2:-}"; shift 2 ;;
    --arch) ARCH="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --package-extension) PACKAGE_EXTENSION="${2:-}"; shift 2 ;;
    --updater-extension) UPDATER_EXTENSION="${2:-}"; shift 2 ;;
    --output) OUTPUT="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "error: unknown argument: $1" >&2; usage ;;
  esac
done

[[ "${PLATFORM}" =~ ^[a-z0-9]+-[a-z0-9_]+$ ]] || usage
case "${GOOS}" in darwin|linux|windows) ;; *) usage ;; esac
case "${ARCH}" in aarch64|x86_64) ;; *) usage ;; esac
[[ "${PACKAGE_EXTENSION}" =~ ^[A-Za-z0-9.]+$ ]] || usage
[[ "${UPDATER_EXTENSION}" =~ ^[A-Za-z0-9.]+$ ]] || usage
[[ -n "${VERSION}" && -n "${OUTPUT}" ]] || usage
python3 "${ROOT}/scripts/semver-compare.py" eq "${VERSION}" "${VERSION}" >/dev/null
eval "$(python3 "${ROOT}/scripts/release-metadata.py" --root "${ROOT}" --format shell)"
[[ "${VERSION}" == "${PRODUCT_VERSION}" ]] || {
  echo "error: artifact version ${VERSION} does not equal repository VERSION ${PRODUCT_VERSION}" >&2
  exit 1
}

assert_native_arch() {
  local label="$1" path="$2" kind
  kind="$(file -b "${path}")"
  case "${ARCH}" in
    x86_64) [[ "${kind}" == *"x86-64"* ]] ;;
    aarch64) [[ "${kind}" == *"ARM aarch64"* || "${kind}" == *"ARM64"* ]] ;;
  esac || {
    echo "error: ${PLATFORM} ${label} architecture does not match ${ARCH}: ${kind}" >&2
    exit 1
  }
}

verify_opengrep() {
  local root="$1" sidecar="${2:-${SIDECAR:-}}" target
  case "${GOOS}" in
    darwin) target="${ARCH}-apple-darwin" ;;
    linux) target="${ARCH}-unknown-linux-gnu" ;;
    windows) target="${ARCH}-pc-windows-msvc" ;;
  esac
  bash "${ROOT}/scripts/stage-bundled-opengrep.sh" --verify "${root}" "${target}" "${sidecar}"
}

TARGET_ROOT="${ROOT}/lycaon-den/src-tauri/target"
PACKAGE=""
UPDATER=""
case "${GOOS}" in
  darwin)
    APP="$(find "${TARGET_ROOT}" -maxdepth 8 -path '*/release/bundle/macos/*.app' -print -quit)"
    PACKAGE="$(find "${TARGET_ROOT}" -maxdepth 8 -path "*/release/bundle/dmg/*.${PACKAGE_EXTENSION}" -print -quit)"
    UPDATER="$(find "$(dirname "${APP}")" -maxdepth 1 -name "*.${UPDATER_EXTENSION}" -print -quit)"
    [[ -n "${APP}" && -n "${PACKAGE}" && -n "${UPDATER}" ]] || {
      echo "error: incomplete macOS bundle output for ${PLATFORM}" >&2
      exit 1
    }
    "${ROOT}/task" bundle:verify -- --app "${APP}" --dmg "${PACKAGE}" --require-signed
    # GUI launch through LaunchServices is unproven on hosted runners; the
    # Keychain probe above still blocks.
    if [[ "${RUNNER_ENVIRONMENT:-}" == github-hosted ]]; then
      "${ROOT}/task" bundle:smoke -- --app "${APP}" \
        || echo "::warning title=Bundle smoke::launch smoke failed on a hosted runner; qualify the build on a real Mac"
    else
      "${ROOT}/task" bundle:smoke -- --app "${APP}"
    fi
    OPENGREP="$(verify_opengrep "${APP}/Contents/Resources/engine-root" "${APP}/Contents/Helpers/Painted Wolf Code engine.app/Contents/MacOS/pw")"
    ;;
  linux)
    PACKAGE="$(find "${TARGET_ROOT}" -maxdepth 8 -path "*/release/bundle/appimage/*.${PACKAGE_EXTENSION}" ! -name '*.tar.gz' -print -quit)"
    UPDATER="$(find "${TARGET_ROOT}" -maxdepth 8 -path "*/release/bundle/appimage/*.${UPDATER_EXTENSION}" -print -quit)"
    [[ -n "${PACKAGE}" && -n "${UPDATER}" ]] || {
      echo "error: incomplete Linux bundle output for ${PLATFORM}" >&2
      exit 1
    }
    assert_native_arch "AppImage" "${PACKAGE}"
    EXTRACT="$(mktemp -d "${TMPDIR:-/tmp}/painted-wolf-appimage.XXXXXX")"
    trap 'rm -rf "${EXTRACT}"' EXIT
    (
      cd "${EXTRACT}"
      "${PACKAGE}" --appimage-extract >/dev/null
    )
    [[ -x "${EXTRACT}/squashfs-root/AppRun" ]] || {
      echo "error: ${PLATFORM} AppImage has no executable AppRun" >&2
      exit 1
    }
    ENGINE="$(find "${EXTRACT}/squashfs-root" -type f -name pw -perm -u+x -print -quit)"
    [[ -n "${ENGINE}" ]] || {
      echo "error: ${PLATFORM} AppImage has no bundled engine sidecar" >&2
      exit 1
    }
    assert_native_arch "bundled engine" "${ENGINE}"
    LOG_VIEWER="$(find "${EXTRACT}/squashfs-root" -type f -name pw-logs -perm -u+x -print -quit)"
    [[ -n "${LOG_VIEWER}" ]] || {
      echo "error: ${PLATFORM} AppImage has no bundled log viewer" >&2
      exit 1
    }
    assert_native_arch "bundled log viewer" "${LOG_VIEWER}"
    DECIDE_ENGINE="$(find "${EXTRACT}/squashfs-root" -type f -name bialy -perm -u+x -print -quit)"
    [[ -n "${DECIDE_ENGINE}" ]] || {
      echo "error: ${PLATFORM} AppImage has no bundled decision engine" >&2
      exit 1
    }
    assert_native_arch "bundled decision engine" "${DECIDE_ENGINE}"
    BROWSER="$(find "${EXTRACT}/squashfs-root" -type f -name chrome-headless-shell -perm -u+x -print -quit)"
    [[ -n "${BROWSER}" ]] || {
      echo "error: ${PLATFORM} AppImage has no bundled browser" >&2
      exit 1
    }
    assert_native_arch "bundled browser" "${BROWSER}"
    [[ -n "$(find "${EXTRACT}/squashfs-root" -type f -path '*/engine-root/decide/models/*/.complete' -print -quit)" ]] || {
      echo "error: ${PLATFORM} AppImage has no bundled decision checkpoint" >&2
      exit 1
    }
    SCANNER_ROOTS=()
    while IFS= read -r -d '' candidate_root; do
      SCANNER_ROOTS+=("${candidate_root}")
    done < <(find "${EXTRACT}/squashfs-root" -type d -name engine-root -print0)
    if (( ${#SCANNER_ROOTS[@]} != 1 )); then
      echo "error: ${PLATFORM} AppImage requires exactly one engine resource root" >&2
      exit 1
    fi
    OPENGREP="$(verify_opengrep "${SCANNER_ROOTS[0]}" "${ENGINE}")"
    assert_native_arch "bundled OpenGrep" "${OPENGREP}"
    GIT_ENGINE_LINK="$(find "${EXTRACT}/squashfs-root" -path '*/engine-root/gitengine/bin/git' -print -quit)"
    GIT_LFS="$(find "${EXTRACT}/squashfs-root" -type f -path '*/engine-root/gitengine/bin/git-lfs' -perm -u+x -print -quit)"
    [[ -n "${GIT_ENGINE_LINK}" && -x "${GIT_ENGINE_LINK}" && -n "${GIT_LFS}" ]] || {
      echo "error: ${PLATFORM} AppImage has no complete bundled Git toolchain" >&2
      exit 1
    }
    GIT_ENGINE="$(readlink -f "${GIT_ENGINE_LINK}")"
    assert_native_arch "bundled Git" "${GIT_ENGINE}"
    assert_native_arch "bundled Git LFS" "${GIT_LFS}"
    while IFS= read -r -d '' payload; do
      if [[ "$(file -b "${payload}")" == *"ELF"* ]]; then
        assert_native_arch "packaged native payload" "${payload}"
      fi
    done < <(find "${EXTRACT}/squashfs-root" -type f -print0)
    ;;
  windows)
    PACKAGE="$(find "${TARGET_ROOT}" -maxdepth 8 -path "*/release/bundle/nsis/*.${PACKAGE_EXTENSION}" ! -name '*.nsis.zip' -print -quit)"
    UPDATER="$(find "${TARGET_ROOT}" -maxdepth 8 -path "*/release/bundle/nsis/*.${UPDATER_EXTENSION}" -print -quit)"
    [[ -n "${PACKAGE}" && -n "${UPDATER}" ]] || {
      echo "error: incomplete Windows bundle output for ${PLATFORM}" >&2
      exit 1
    }
    PACKAGE_KIND="$(file -b "${PACKAGE}")"
    [[ "${PACKAGE_KIND}" == *"PE32+"* && "${PACKAGE_KIND}" == *"x86-64"* ]] || {
      echo "error: ${PLATFORM} installer is not a 64-bit PE executable: ${PACKAGE_KIND}" >&2
      exit 1
    }
    APPLICATION="$(find "${TARGET_ROOT}" -maxdepth 6 -path '*/release/painted-wolf-code.exe' -print -quit)"
    [[ -n "${APPLICATION}" ]] || {
      echo "error: ${PLATFORM} build has no application executable" >&2
      exit 1
    }
    SIDECAR="${ROOT}/lycaon-den/src-tauri/binaries/pw-x86_64-pc-windows-msvc.exe"
    LOG_VIEWER="${ROOT}/lycaon-den/src-tauri/binaries/pw-logs-x86_64-pc-windows-msvc.exe"
    DECIDE_ENGINE="${ROOT}/lycaon-den/src-tauri/binaries/bialy-x86_64-pc-windows-msvc.exe"
    GIT_ENGINE="${ROOT}/lycaon-den/src-tauri/engine-root/gitengine/cmd/git.exe"
    GIT_LFS="${ROOT}/lycaon-den/src-tauri/engine-root/gitengine/mingw64/libexec/git-core/git-lfs.exe"
    OPENGREP="$(verify_opengrep "${ROOT}/lycaon-den/src-tauri/engine-root")"
    BROWSER="${ROOT}/lycaon-den/src-tauri/engine-root/browser/chrome-headless-shell.exe"
    DECIDE_MODEL="$(find "${ROOT}/lycaon-den/src-tauri/engine-root/decide/models" -type f -name .complete -print -quit 2>/dev/null || true)"
    [[ -f "${SIDECAR}" && -f "${LOG_VIEWER}" && -f "${DECIDE_ENGINE}" && -f "${GIT_ENGINE}" && -f "${GIT_LFS}" && -f "${OPENGREP}" && -f "${BROWSER}" && -n "${DECIDE_MODEL}" ]] || {
      echo "error: ${PLATFORM} build has incomplete sidecar or bundled tool payloads" >&2
      exit 1
    }
    assert_native_arch "application" "${APPLICATION}"
    assert_native_arch "bundled engine" "${SIDECAR}"
    assert_native_arch "bundled log viewer" "${LOG_VIEWER}"
    assert_native_arch "bundled decision engine" "${DECIDE_ENGINE}"
    assert_native_arch "bundled Git" "${GIT_ENGINE}"
    assert_native_arch "bundled Git LFS" "${GIT_LFS}"
    assert_native_arch "bundled OpenGrep" "${OPENGREP}"
    assert_native_arch "bundled browser" "${BROWSER}"
    while IFS= read -r -d '' payload; do
      assert_native_arch "packaged executable payload" "${payload}"
    done < <(find "${ROOT}/lycaon-den/src-tauri/engine-root" -type f -iname '*.exe' -print0)
    pwsh -NoProfile -File "${ROOT}/scripts/release-verify-windows-signature.ps1" \
      -Path "${PACKAGE}" -UpdaterArchive "${UPDATER}" -ApplicationPath "${APPLICATION}" \
      -SidecarPath "${SIDECAR}" -LogViewerPath "${LOG_VIEWER}" \
      -GitPath "${GIT_ENGINE}" -GitLFSPath "${GIT_LFS}" -OpenGrepPath "${OPENGREP}" -BrowserPath "${BROWSER}" \
      -EngineRootPath "${ROOT}/lycaon-den/src-tauri/engine-root" \
      -ExpectedVersion "${WINDOWS_PACKAGE_VERSION}"
    ;;
esac

SIGNATURE="${UPDATER}.sig"
[[ -f "${SIGNATURE}" ]] || { echo "error: missing updater signature ${SIGNATURE}" >&2; exit 1; }
bash "${ROOT}/scripts/verify-updater-signature.sh" "${UPDATER}" "${SIGNATURE}"

mkdir -p "${OUTPUT}/opengrep"
OUTPUT="$(cd "${OUTPUT}" && pwd)"
STEM="painted-wolf-code_v${VERSION}_${PLATFORM}"
OPENGREP_ARTIFACT="$(bash "${ROOT}/scripts/resolve-opengrep.sh" --artifact-dir-only --offline)"
(
  cd "${ROOT}/lycaon"
  env -u LYCAON_OPENGREP_CANDIDATE go run ./cmd/opengrep-artifact -mode audit \
    -artifact-directory "${OPENGREP_ARTIFACT}" \
    -packaged-directory "$(dirname "${OPENGREP}")" \
    -evidence-root "${ROOT}/lycaon/config/runtime/scanners" \
    -audit-output "${OUTPUT}/opengrep/${STEM}.json" >/dev/null
)
cp "${PACKAGE}" "${OUTPUT}/${STEM}.${PACKAGE_EXTENSION}"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "${OUTPUT}/${STEM}.${PACKAGE_EXTENSION}" | awk '{print $1}' \
    > "${OUTPUT}/${STEM}.${PACKAGE_EXTENSION}.sha256"
else
  shasum -a 256 "${OUTPUT}/${STEM}.${PACKAGE_EXTENSION}" | awk '{print $1}' \
    > "${OUTPUT}/${STEM}.${PACKAGE_EXTENSION}.sha256"
fi
cp "${UPDATER}" "${OUTPUT}/${STEM}.${UPDATER_EXTENSION}"
cp "${SIGNATURE}" "${OUTPUT}/${STEM}.${UPDATER_EXTENSION}.sig"
jq -n --arg platform "${PLATFORM}" \
  --argjson update_keys "$(python3 "${ROOT}/scripts/release-metadata.py" | jq '{signing_generation,embedded_generation,signing_key_fingerprint,embedded_key_fingerprint}')" \
  --arg signature "$(cat "${SIGNATURE}")" \
  --arg updater_artifact "${STEM}.${UPDATER_EXTENSION}" \
  '{platform:$platform,signature:$signature,updater_artifact:$updater_artifact,update_keys:$update_keys}' \
  > "${OUTPUT}/fragment-${PLATFORM}.json"
