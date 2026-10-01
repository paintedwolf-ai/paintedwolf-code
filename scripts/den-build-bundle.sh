#!/usr/bin/env bash
set -euo pipefail

if [[ -n "${LYCAON_OPENGREP_CANDIDATE:-}" ]]; then
  echo "error: local Opengrep candidates cannot be used in release packaging" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GO_DIR="${ROOT}/lycaon"
DEN_DIR="${ROOT}/lycaon-den"
TAURI_DIR="${DEN_DIR}/src-tauri"
BINARIES_DIR="${TAURI_DIR}/binaries"
ENGINE_ROOT="${TAURI_DIR}/engine-root"
SIDECAR_NAME="pw"

if ! command -v bun >/dev/null 2>&1; then
  echo "error: bun required — https://bun.sh" >&2
  exit 1
fi
if ! command -v go >/dev/null 2>&1; then
  echo "error: go required — see lycaon/go.mod" >&2
  exit 1
fi
if ! command -v cargo >/dev/null 2>&1; then
  echo "error: cargo required for Tauri — install Rust from https://rustup.rs" >&2
  exit 1
fi

TARGET="$(rustc --print host-tuple)"
eval "$(python3 "${ROOT}/scripts/release-metadata.py" --root "${ROOT}" --format shell)"
HOST_KIND=""
EXE_SUFFIX=""
case "${TARGET}" in
  *-apple-darwin) HOST_KIND="darwin" ;;
  *-unknown-linux-gnu) HOST_KIND="linux" ;;
  *-pc-windows-msvc) HOST_KIND="windows"; EXE_SUFFIX=".exe" ;;
  *) echo "error: unsupported desktop target ${TARGET}" >&2; exit 1 ;;
esac
SIDECAR_BIN="${BINARIES_DIR}/${SIDECAR_NAME}-${TARGET}${EXE_SUFFIX}"
LOG_VIEWER_BIN="${BINARIES_DIR}/pw-logs-${TARGET}${EXE_SUFFIX}"
DECIDE_BIN="${BINARIES_DIR}/bialy-${TARGET}${EXE_SUFFIX}"

bash "${ROOT}/scripts/sync-den-versions.sh"

bash "${ROOT}/scripts/stage-engine.sh" --release
BUNDLED_OPENGREP="$(bash "${ROOT}/scripts/stage-bundled-opengrep.sh" --verify "${ENGINE_ROOT}" "${TARGET}" "${SIDECAR_BIN}")"

if [[ ! -f "${ROOT}/THIRD-PARTY-NOTICES.md" ]]; then
  echo "den:bundle — generating THIRD-PARTY-NOTICES.md" >&2
  bash "${ROOT}/scripts/licenses-notices.sh"
fi
cp -f "${ROOT}/THIRD-PARTY-NOTICES.md" "${TAURI_DIR}/THIRD-PARTY-NOTICES.md"

MACOS_FLOOR_FILE="${GO_DIR}/internal/platformfloor/macos_floor.txt"
if [[ ! -f "${MACOS_FLOOR_FILE}" ]]; then
  echo "error: missing ${MACOS_FLOOR_FILE}" >&2
  exit 1
fi
MACOS_FLOOR="$(tr -d '[:space:]' < "${MACOS_FLOOR_FILE}")"
if [[ "${HOST_KIND}" == "darwin" ]]; then
  export MACOSX_DEPLOYMENT_TARGET="${MACOS_FLOOR}"
fi

# Nested executables and libraries require individual signatures.
macho_files() {
  local root="$1"
  [[ -d "${root}" ]] || return 0
  find "${root}" -type f -print0 \
    | while IFS= read -r -d '' f; do
        if file -b "${f}" 2>/dev/null | grep -q 'Mach-O'; then
          printf '%s\n' "${f}"
        fi
      done
}

if [[ "${HOST_KIND}" == "darwin" ]]; then
  GITENGINE_ROOT="${ENGINE_ROOT}/gitengine"
  # The staged credential helper already carries its signature and profile.
  if [[ -d "${GITENGINE_ROOT}" ]]; then
    echo "den:bundle — Developer ID signing bundled git toolchain" >&2
    GITENGINE_SIGNED=0
    while IFS= read -r f; do
      codesign --force --timestamp --options runtime \
        -s "${APPLE_SIGNING_IDENTITY}" "${f}"
      GITENGINE_SIGNED=$(( GITENGINE_SIGNED + 1 ))
    done < <(macho_files "${GITENGINE_ROOT}")
    if (( GITENGINE_SIGNED == 0 )); then
      echo "error: no Mach-O binaries found under ${GITENGINE_ROOT} — the git" >&2
      echo "       engine is missing from the bundle; run ./task gitengine:fetch" >&2
      exit 1
    fi
    echo "den:bundle — signed ${GITENGINE_SIGNED} git engine binaries" >&2
  else
    echo "error: missing ${GITENGINE_ROOT} — the app ships its own git and has" >&2
    echo "       no host fallback, so a bundle without it cannot read a repository." >&2
    echo "       Run ./task gitengine:fetch before bundling." >&2
    exit 1
  fi
  BROWSER_BIN="${ENGINE_ROOT}/browser/chrome-headless-shell"
  if [[ -f "${BROWSER_BIN}" ]]; then
    echo "den:bundle — Developer ID signing chrome-headless-shell tree" >&2
    BROWSER_ENTITLEMENTS="${TAURI_DIR}/browser-entitlements.plist"
    if [[ ! -f "${BROWSER_ENTITLEMENTS}" ]]; then
      echo "error: missing ${BROWSER_ENTITLEMENTS} — hardened runtime without" >&2
      echo "       allow-jit kills V8; refusing to sign a browser that cannot start" >&2
      exit 1
    fi
    find "${ENGINE_ROOT}/browser" -type f -name '*.dylib' -print0 \
      | while IFS= read -r -d '' f; do
          codesign --force --timestamp --options runtime -s "${APPLE_SIGNING_IDENTITY}" "${f}"
        done
    codesign --force --timestamp --options runtime \
      --entitlements "${BROWSER_ENTITLEMENTS}" \
      -s "${APPLE_SIGNING_IDENTITY}" "${BROWSER_BIN}"
  fi
fi

chmod +x "${SIDECAR_BIN}"

export CI=false
if [[ "${HOST_KIND}" == "darwin" ]]; then
  if [[ -n "${APPLE_API_ISSUER:-}" && -n "${APPLE_API_KEY:-}" && -n "${APPLE_API_KEY_PATH:-}" ]]; then
    echo "den:bundle — notarizing via App Store Connect API key" >&2
  elif [[ -n "${APPLE_ID:-}" && -n "${APPLE_PASSWORD:-}" && -n "${APPLE_TEAM_ID:-}" ]]; then
    echo "den:bundle — notarizing via Apple ID app-specific password" >&2
  else
    echo "warning: APPLE_SIGNING_IDENTITY set but no notary credentials —" >&2
    echo "         producing a SIGNED but NOT NOTARIZED .app. Set either" >&2
    echo "         APPLE_API_ISSUER + APPLE_API_KEY + APPLE_API_KEY_PATH, or" >&2
    echo "         APPLE_ID + APPLE_PASSWORD + APPLE_TEAM_ID to notarize." >&2
  fi
fi

BUNDLE_KIND="dmg"
BUILD_CONFIG=()
if [[ "${HOST_KIND}" == "linux" ]]; then
  BUNDLE_KIND="appimage"
elif [[ "${HOST_KIND}" == "windows" ]]; then
  BUNDLE_KIND="nsis"
  if [[ -n "${TAURI_SIGNING_PRIVATE_KEY:-}" ]]; then
    : "${AZURE_CLIENT_ID:?release Windows builds require AZURE_CLIENT_ID}"
    : "${AZURE_CLIENT_SECRET:?release Windows builds require AZURE_CLIENT_SECRET}"
    : "${AZURE_TENANT_ID:?release Windows builds require AZURE_TENANT_ID}"
    : "${AZURE_ARTIFACT_SIGNING_ENDPOINT:?release Windows builds require an Artifact Signing endpoint}"
    : "${AZURE_ARTIFACT_SIGNING_ACCOUNT:?release Windows builds require an Artifact Signing account}"
    : "${AZURE_ARTIFACT_SIGNING_PROFILE:?release Windows builds require an Artifact Signing profile}"
    [[ "${AZURE_ARTIFACT_SIGNING_ENDPOINT}" =~ ^https://[a-z0-9.-]+\.codesigning\.azure\.net$ ]] || {
      echo "error: invalid Azure Artifact Signing endpoint" >&2
      exit 1
    }
    [[ "${AZURE_ARTIFACT_SIGNING_ACCOUNT}" =~ ^[A-Za-z0-9_-]+$ ]] || {
      echo "error: invalid Azure Artifact Signing account name" >&2
      exit 1
    }
    [[ "${AZURE_ARTIFACT_SIGNING_PROFILE}" =~ ^[A-Za-z0-9_-]+$ ]] || {
      echo "error: invalid Azure Artifact Signing profile name" >&2
      exit 1
    }
    WINDOWS_SIGN_COMMAND="artifact-signing-cli -e ${AZURE_ARTIFACT_SIGNING_ENDPOINT} -a ${AZURE_ARTIFACT_SIGNING_ACCOUNT} -c ${AZURE_ARTIFACT_SIGNING_PROFILE} -d \"Painted Wolf Code\" %1"
    WINDOWS_CONFIG="$(jq -cn \
      --arg version "${WINDOWS_PACKAGE_VERSION}" \
      --arg sign_command "${WINDOWS_SIGN_COMMAND}" \
      '{version:$version,bundle:{windows:{signCommand:$sign_command}}}')"
    WINDOWS_GIT="${ENGINE_ROOT}/gitengine/cmd/git.exe"
    WINDOWS_GIT_LFS="${ENGINE_ROOT}/gitengine/mingw64/libexec/git-core/git-lfs.exe"
    WINDOWS_OPENGREP="${BUNDLED_OPENGREP}"
    WINDOWS_BROWSER="${ENGINE_ROOT}/browser/chrome-headless-shell.exe"
    for bundled_executable in "${WINDOWS_GIT}" "${WINDOWS_GIT_LFS}" "${WINDOWS_OPENGREP}" "${WINDOWS_BROWSER}"; do
      if [[ ! -f "${bundled_executable}" ]]; then
        echo "error: missing Windows bundled executable ${bundled_executable}" >&2
        exit 1
      fi
    done
    WINDOWS_BUNDLED_EXECUTABLES=()
    while IFS= read -r -d '' bundled_executable; do
      # The scanner signature is part of its pinned artifact.
      if [[ "${bundled_executable}" != "${BUNDLED_OPENGREP}" ]]; then
        WINDOWS_BUNDLED_EXECUTABLES+=("${bundled_executable}")
      fi
    done < <(find "${ENGINE_ROOT}" -type f -iname '*.exe' -print0)
    if (( ${#WINDOWS_BUNDLED_EXECUTABLES[@]} == 0 )); then
      echo "error: Windows engine root contains no executable payloads" >&2
      exit 1
    fi
    artifact-signing-cli \
      -e "${AZURE_ARTIFACT_SIGNING_ENDPOINT}" \
      -a "${AZURE_ARTIFACT_SIGNING_ACCOUNT}" \
      -c "${AZURE_ARTIFACT_SIGNING_PROFILE}" \
      -d "Painted Wolf Code" \
      "${SIDECAR_BIN}" "${LOG_VIEWER_BIN}" "${DECIDE_BIN}" \
      "${WINDOWS_BUNDLED_EXECUTABLES[@]}"
    BUILD_CONFIG+=(--config "${WINDOWS_CONFIG}")
  fi
elif [[ -n "${TAURI_SIGNING_PRIVATE_KEY:-}" ]]; then
  # Updater archives require the app bundle beside the disk image.
  BUNDLE_KIND="app,dmg"
elif [[ "${HOST_KIND}" == "darwin" ]]; then
  echo "warning: TAURI_SIGNING_PRIVATE_KEY not set — building dmg only, WITHOUT" >&2
  echo "         updater artifacts (.app.tar.gz/.sig). Release publishes must set it." >&2
fi

echo "den:bundle — tauri build (--bundles ${BUNDLE_KIND}, target ${TARGET})" >&2
(
  cd "${DEN_DIR}"
  if [[ "${HOST_KIND}" == "darwin" ]]; then
    # A fresh asset compiler process avoids shared service state.
    export IBToolNeverDeque=1
    export PATH="${ROOT}/scripts/macos-build-tools:${PATH}"
  fi
  if (( ${#BUILD_CONFIG[@]} > 0 )); then
    bun run tauri build --bundles "${BUNDLE_KIND}" --target "${TARGET}" "${BUILD_CONFIG[@]}"
  else
    bun run tauri build --bundles "${BUNDLE_KIND}" --target "${TARGET}"
  fi
)

APP_PATH=""
if [[ "${HOST_KIND}" == "darwin" ]]; then
  APP_PATH="$(find "${TAURI_DIR}/target/${TARGET}/release/bundle/macos" -maxdepth 1 -name '*.app' -print -quit 2>/dev/null || true)"
fi
if [[ -n "${APP_PATH}" ]]; then
  VERIFY_ARGS=(--app "${APP_PATH}" --require-signed)
  echo "den:bundle — verifying bundle integrity" >&2
  bash "${ROOT}/scripts/verify-bundle.sh" "${VERIFY_ARGS[@]}"
fi

DMG_PATH=""
if [[ "${HOST_KIND}" == "darwin" ]]; then
  DMG_PATH="$(find "${TAURI_DIR}/target/${TARGET}/release/bundle/dmg" -maxdepth 1 -name '*.dmg' -print -quit 2>/dev/null || true)"
fi

DMG_NOTARIZED=0
if [[ "${HOST_KIND}" == "darwin" && -n "${DMG_PATH}" ]]; then
  if [[ -n "${APPLE_API_ISSUER:-}" && -n "${APPLE_API_KEY:-}" && -n "${APPLE_API_KEY_PATH:-}" ]]; then
    echo "den:bundle — notarizing DMG via App Store Connect API key" >&2
    xcrun notarytool submit "${DMG_PATH}" \
      --key "${APPLE_API_KEY_PATH}" --key-id "${APPLE_API_KEY}" --issuer "${APPLE_API_ISSUER}" --wait
    DMG_NOTARIZED=1
  elif [[ -n "${APPLE_ID:-}" && -n "${APPLE_PASSWORD:-}" && -n "${APPLE_TEAM_ID:-}" ]]; then
    echo "den:bundle — notarizing DMG via Apple ID app-specific password" >&2
    xcrun notarytool submit "${DMG_PATH}" \
      --apple-id "${APPLE_ID}" --password "${APPLE_PASSWORD}" --team-id "${APPLE_TEAM_ID}" --wait
    DMG_NOTARIZED=1
  fi
  if [[ "${DMG_NOTARIZED}" == "1" ]]; then
    xcrun stapler staple "${DMG_PATH}"
  else
    echo "warning: DMG signed but NOT notarized (no notary credentials)" >&2
  fi
fi

if [[ -n "${DMG_PATH}" ]]; then
  echo ""
  if [[ "${DMG_NOTARIZED}" == "1" ]]; then
    echo "Signed + notarized DMG:"
  else
    echo "Signed DMG (not notarized):"
  fi
  echo "  ${DMG_PATH}"
  echo ""
  echo "Verify: spctl --assess --type open --context context:primary-signature -vvv \"${DMG_PATH}\""
else
  echo ""
  echo "Bundle output under:"
  echo "  ${TAURI_DIR}/target/${TARGET}/release/bundle/"
fi
