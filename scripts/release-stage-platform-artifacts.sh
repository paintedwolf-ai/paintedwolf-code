#!/usr/bin/env bash
# Verify one native bundle and emit its immutable release set.
set -euo pipefail

if [[ -n "${LYCAON_OPENGREP_CANDIDATE:-}" ]]; then
  echo "error: local Opengrep candidates cannot be used in release packaging" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
source "${ROOT}/scripts/sha256.sh"
PLATFORM=""
GOOS=""
ARCH=""
VERSION=""
PACKAGE_EXTENSIONS=""
UPDATER_EXTENSION=""
OUTPUT=""

usage() {
  echo "Usage: release-stage-platform-artifacts.sh --platform KEY --goos OS --arch ARCH --version VERSION --package-extensions EXT[,EXT] [--updater-extension EXT] --output DIR" >&2
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --platform) PLATFORM="${2:-}"; shift 2 ;;
    --goos) GOOS="${2:-}"; shift 2 ;;
    --arch) ARCH="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --package-extensions) PACKAGE_EXTENSIONS="${2:-}"; shift 2 ;;
    --updater-extension) UPDATER_EXTENSION="${2:-}"; shift 2 ;;
    --output) OUTPUT="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "error: unknown argument: $1" >&2; usage ;;
  esac
done

[[ "${PLATFORM}" =~ ^[a-z0-9]+-[a-z0-9_]+$ ]] || usage
case "${GOOS}" in darwin|linux|windows) ;; *) usage ;; esac
case "${ARCH}" in aarch64|x86_64) ;; *) usage ;; esac
[[ "${PACKAGE_EXTENSIONS}" =~ ^[A-Za-z0-9.]+(,[A-Za-z0-9.]+)*$ ]] || usage
[[ -z "${UPDATER_EXTENSION}" || "${UPDATER_EXTENSION}" =~ ^[A-Za-z0-9.]+$ ]] || usage
[[ -n "${VERSION}" && -n "${OUTPUT}" ]] || usage
IFS=, read -r -a PACKAGE_EXTENSION_LIST <<<"${PACKAGE_EXTENSIONS}"
PACKAGE_EXTENSION="${PACKAGE_EXTENSION_LIST[0]}"
case "${GOOS}" in
  linux)
    # Linux installs through the package manager: two package families, no updater artifact.
    [[ ( "${PACKAGE_EXTENSIONS}" == "deb,rpm" || "${PACKAGE_EXTENSIONS}" == "deb,rpm,pkg.tar.zst" ) && -z "${UPDATER_EXTENSION}" ]] || {
      echo "error: Linux stages deb,rpm and optional native pacman with no updater artifact" >&2
      exit 2
    }
    ;;
  *)
    [[ ${#PACKAGE_EXTENSION_LIST[@]} -eq 1 && -n "${UPDATER_EXTENSION}" ]] || usage
    if [[ "${GOOS}" != darwin && "${UPDATER_EXTENSION}" != "${PACKAGE_EXTENSION}" ]]; then
      echo "error: ${GOOS} updater must be the native installer bundle" >&2
      exit 2
    fi
    ;;
esac
python3 "${ROOT}/scripts/semver-compare.py" eq "${VERSION}" "${VERSION}" >/dev/null
eval "$(python3 "${ROOT}/scripts/release-metadata.py" --root "${ROOT}" --format shell)"
[[ "${VERSION}" == "${PRODUCT_VERSION}" ]] || {
  echo "error: artifact version ${VERSION} does not equal repository VERSION ${PRODUCT_VERSION}" >&2
  exit 1
}
# Package versions follow each family's grammar; the metadata script is the sole projection.
: "${LINUX_PACKAGE_VERSION:?release metadata has no Linux package version}"

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
    DEB_PACKAGE="$(find "${TARGET_ROOT}" -maxdepth 8 -path '*/release/bundle/deb/*.deb' -print -quit)"
    RPM_PACKAGE="$(find "${TARGET_ROOT}" -maxdepth 8 -path '*/release/bundle/rpm/*.rpm' -print -quit)"
    [[ -n "${DEB_PACKAGE}" && -n "${RPM_PACKAGE}" ]] || {
      echo "error: incomplete Linux package output for ${PLATFORM}: need one .deb and one .rpm" >&2
      exit 1
    }
    for tool in dpkg-deb rpm rpm2cpio cpio; do
      command -v "${tool}" >/dev/null 2>&1 || { echo "error: ${tool} is required to audit Linux packages" >&2; exit 1; }
    done
    LINUX_PACKAGE_NAME="painted-wolf-code"
    LINUX_INSTALL_ROOT="opt/painted-wolf-code"
    case "${ARCH}" in x86_64) DEB_ARCH="amd64" ;; aarch64) DEB_ARCH="arm64" ;; esac
    EXTRACT="$(mktemp -d "${TMPDIR:-/tmp}/painted-wolf-linux.XXXXXX")"
    trap 'rm -rf "${EXTRACT}"' EXIT

    # The package manifest must describe the shipped bytes, every helper must be
    # native, and both families must install the same tree.
    audit_linux_tree() {
      local label="$1" tree="$2" root="$2/${LINUX_INSTALL_ROOT}"
      [[ -x "${root}/bin/${LINUX_PACKAGE_NAME}" ]] || { echo "error: ${PLATFORM} ${label} has no desktop shell executable under the install root" >&2; exit 1; }
      assert_native_arch "${label} desktop shell" "${root}/bin/${LINUX_PACKAGE_NAME}"
      for entry in pw pw-logs; do
        [[ -x "${tree}/usr/bin/${entry}" ]] || { echo "error: ${PLATFORM} ${label} has no /usr/bin/${entry} entry point" >&2; exit 1; }
        grep -Fq "exec /opt/painted-wolf-code/bin/${entry} \"\$@\"" "${tree}/usr/bin/${entry}" || {
          echo "error: ${PLATFORM} ${label} /usr/bin/${entry} does not exec the packaged binary" >&2
          exit 1
        }
      done
      ENGINE="${root}/bin/pw"
      [[ -x "${ENGINE}" ]] || { echo "error: ${PLATFORM} ${label} has no bundled engine sidecar" >&2; exit 1; }
      assert_native_arch "${label} bundled engine" "${ENGINE}"
      [[ -x "${root}/bin/pw-logs" ]] || { echo "error: ${PLATFORM} ${label} has no bundled log viewer" >&2; exit 1; }
      assert_native_arch "${label} bundled log viewer" "${root}/bin/pw-logs"
      DOCUMENT_CORE="${root}/bin/pw-document-core"
      [[ -x "${DOCUMENT_CORE}" ]] || { echo "error: ${PLATFORM} ${label} has no document core beside its engine" >&2; exit 1; }
      assert_native_arch "${label} bundled document core" "${DOCUMENT_CORE}"
      DECIDE_ENGINE="${root}/bin/bialy"
      [[ -x "${DECIDE_ENGINE}" ]] || { echo "error: ${PLATFORM} ${label} has no bundled decision engine" >&2; exit 1; }
      assert_native_arch "${label} bundled decision engine" "${DECIDE_ENGINE}"
      BROWSER="$(find "${root}/engine-root" -type f -name chrome-headless-shell -perm -u+x -print -quit)"
      [[ -n "${BROWSER}" ]] || { echo "error: ${PLATFORM} ${label} has no bundled browser" >&2; exit 1; }
      assert_native_arch "${label} bundled browser" "${BROWSER}"
      [[ -n "$(find "${root}/engine-root/decide/models" -type f -name .complete -print -quit 2>/dev/null)" ]] || {
        echo "error: ${PLATFORM} ${label} has no bundled decision checkpoint" >&2
        exit 1
      }
      [[ -f "${root}/engine-root/schemas/oar/oar.schema.json" ]] || { echo "error: ${PLATFORM} ${label} has no engine schema tree" >&2; exit 1; }
      [[ "$(tr -d '[:space:]' < "${root}/release-version")" == "${VERSION}" ]] || {
        echo "error: ${PLATFORM} ${label} release-version does not equal ${VERSION}" >&2
        exit 1
      }
      [[ -s "${root}/THIRD-PARTY-NOTICES.md" ]] || { echo "error: ${PLATFORM} ${label} has no third-party notices" >&2; exit 1; }
      # The application id is the bundle identifier; the GTK application sets it as the
      # Wayland app id and the X11 window class.
      APP_ID="$(jq -r .identifier "${ROOT}/lycaon-den/src-tauri/tauri.conf.json")"
      DESKTOP_ENTRY="${tree}/usr/share/applications/${APP_ID}.desktop"
      [[ -f "${DESKTOP_ENTRY}" ]] || { echo "error: ${PLATFORM} ${label} has no ${APP_ID} desktop entry" >&2; exit 1; }
      grep -Fxq "StartupWMClass=${APP_ID}" "${DESKTOP_ENTRY}" && grep -Fxq "Exec=/${LINUX_INSTALL_ROOT}/bin/${LINUX_PACKAGE_NAME} %U" "${DESKTOP_ENTRY}" || {
        echo "error: ${PLATFORM} ${label} desktop entry lacks the window class or exec line" >&2
        exit 1
      }
      [[ "$(jq -r .app.enableGTKAppId "${ROOT}/lycaon-den/src-tauri/tauri.linux.conf.json")" == true ]] || {
        echo "error: the Linux shell does not set its GTK application id" >&2
        exit 1
      }
      [[ "$(find "${tree}/usr/share/applications" -name '*.desktop' | wc -l)" -eq 1 ]] || {
        echo "error: ${PLATFORM} ${label} ships more than the ${APP_ID} desktop entry" >&2
        exit 1
      }
      [[ ! -e "${tree}/usr/bin/${LINUX_PACKAGE_NAME}" ]] || { echo "error: ${PLATFORM} ${label} installs the shell outside the install root" >&2; exit 1; }
      # Every packaged service runs an executable the package installs and the manifest lists.
      for unit in "${tree}"/usr/lib/systemd/system/*.service; do
        [[ -e "${unit}" ]] || continue
        exec_start="$(sed -n 's/^ExecStart=//p' "${unit}")"
        [[ "${exec_start}" == /opt/painted-wolf-code/bin/* && -x "${tree}${exec_start%% *}" ]] || {
          echo "error: ${PLATFORM} ${label} $(basename "${unit}") runs ${exec_start:-nothing}, not a packaged helper" >&2
          exit 1
        }
        jq -e --arg path "${exec_start%% *}" 'any(.executables[]; .path == $path)' "${root}/manifest.json" >/dev/null || {
          echo "error: ${PLATFORM} ${label} manifest omits ${exec_start%% *}" >&2
          exit 1
        }
      done
      # GTK, WebKitGTK and the C library come from the distribution, never from the package.
      [[ -z "$(find "${tree}" -type f \( -name 'libgtk-3*.so*' -o -name 'libgdk-3*.so*' -o -name 'libwebkit2gtk*.so*' -o -name 'libjavascriptcoregtk*.so*' -o -name 'libc.so*' -o -name 'libsoup-3*.so*' \) -print -quit)" ]] || {
        echo "error: ${PLATFORM} ${label} bundles a desktop toolkit or C library the distribution provides" >&2
        exit 1
      }
      [[ -n "$(find "${tree}/usr/share/icons/hicolor" -type f -name "${LINUX_PACKAGE_NAME}.png" -print -quit 2>/dev/null)" ]] || {
        echo "error: ${PLATFORM} ${label} has no application icons" >&2
        exit 1
      }
      python3 "${ROOT}/scripts/linux-package-manifest.py" verify-tree --tree "${tree}" --version "${VERSION}" --architecture "${ARCH}"
      LINUX_OPENGREP="$(verify_opengrep "${root}/engine-root" "${ENGINE}")"
      assert_native_arch "${label} bundled OpenGrep" "${LINUX_OPENGREP}"
      GIT_ENGINE_LINK="${root}/engine-root/gitengine/bin/git"
      GIT_LFS="${root}/engine-root/gitengine/bin/git-lfs"
      [[ -e "${GIT_ENGINE_LINK}" && -x "${GIT_ENGINE_LINK}" && -f "${GIT_LFS}" && -x "${GIT_LFS}" ]] || {
        echo "error: ${PLATFORM} ${label} has no complete bundled Git toolchain" >&2
        exit 1
      }
      GIT_ENGINE="$(readlink -f "${GIT_ENGINE_LINK}")"
      assert_native_arch "${label} bundled Git" "${GIT_ENGINE}"
      assert_native_arch "${label} bundled Git LFS" "${GIT_LFS}"
      (cd "${ROOT}" && python3 -m scripts.linux_release.abi_audit \
        --tree "${tree}" --architecture "${ARCH}" \
        --floors "${ROOT}/lycaon/internal/platformfloor/linux_floor.txt" \
        --output "${EXTRACT}/linux-abi-${label#.}.json")
      # Nothing the package installs may be writable by other users or carry setuid bits.
      [[ -z "$(find "${tree}" -type f \( -perm -o+w -o -perm -4000 -o -perm -2000 \) -print -quit)" ]] || {
        echo "error: ${PLATFORM} ${label} installs world-writable or setuid files" >&2
        exit 1
      }
    }

    # Maintainer scripts name the socket units they enable; a renamed unit would ship disabled.
    assert_sockets_enabled() {
      local label="$1" tree="$2" scripts="$3" socket
      for socket in "${tree}"/usr/lib/systemd/system/*.socket; do
        [[ -e "${socket}" ]] || continue
        [[ "${scripts}" == *"$(basename "${socket}")"* ]] || {
          echo "error: ${PLATFORM} ${label} maintainer scripts do not enable $(basename "${socket}")" >&2
          exit 1
        }
      done
    }

    echo "release-stage: auditing ${DEB_PACKAGE}" >&2
    DEB_TREE="${EXTRACT}/deb"
    mkdir -p "${DEB_TREE}"
    dpkg-deb -x "${DEB_PACKAGE}" "${DEB_TREE}"
    dpkg-deb -e "${DEB_PACKAGE}" "${EXTRACT}/deb-control"
    [[ "$(dpkg-deb -f "${DEB_PACKAGE}" Package)" == "${LINUX_PACKAGE_NAME}" ]] || { echo "error: .deb package name is not ${LINUX_PACKAGE_NAME}" >&2; exit 1; }
    [[ "$(dpkg-deb -f "${DEB_PACKAGE}" Architecture)" == "${DEB_ARCH}" ]] || { echo "error: .deb architecture is not ${DEB_ARCH}" >&2; exit 1; }
    [[ "$(dpkg-deb -f "${DEB_PACKAGE}" Version)" == "${LINUX_PACKAGE_VERSION}" ]] || {
      echo "error: .deb version $(dpkg-deb -f "${DEB_PACKAGE}" Version) does not equal ${LINUX_PACKAGE_VERSION}" >&2
      exit 1
    }
    DEB_DEPENDS="$(dpkg-deb -f "${DEB_PACKAGE}" Depends)"
    for required in libwebkit2gtk-4.1-0 libc6 libgtk-3 libsoup-3.0-0 ca-certificates xdg-utils polkitd; do
      [[ "${DEB_DEPENDS}" == *"${required}"* ]] || { echo "error: .deb Depends lacks ${required}" >&2; exit 1; }
    done
    for script in preinst postinst prerm postrm; do
      [[ -x "${EXTRACT}/deb-control/${script}" ]] || { echo "error: .deb has no ${script} maintainer script" >&2; exit 1; }
    done
    while IFS= read -r -d '' etc_file; do
      rel="${etc_file#"${DEB_TREE}/"}"
      grep -Fxq "/${rel}" "${EXTRACT}/deb-control/conffiles" 2>/dev/null || {
        echo "error: .deb omits /${rel} from DEBIAN/conffiles" >&2
        exit 1
      }
      ! grep -Fq "  ${rel}" "${EXTRACT}/deb-control/md5sums" 2>/dev/null || {
        echo "error: .deb includes conffile ${rel} in DEBIAN/md5sums" >&2
        exit 1
      }
    done < <(find "${DEB_TREE}/etc" -type f -print0 2>/dev/null)
    audit_linux_tree ".deb" "${DEB_TREE}"
    assert_sockets_enabled ".deb" "${DEB_TREE}" "$(cat "${EXTRACT}/deb-control/postinst")"
    if command -v lintian >/dev/null 2>&1; then
      lintian --fail-on error --no-tag-display-limit --suppress-tags-from-file "${ROOT}/packaging/linux/deb/lintian-suppress" "${DEB_PACKAGE}" || {
        echo "error: lintian reported errors for ${DEB_PACKAGE}" >&2
        exit 1
      }
    else
      echo "::warning title=lintian::lintian is not installed; the .deb policy check did not run"
    fi

    echo "release-stage: auditing ${RPM_PACKAGE}" >&2
    RPM_TREE="${EXTRACT}/rpm"
    mkdir -p "${RPM_TREE}"
    (cd "${RPM_TREE}" && rpm2cpio "${RPM_PACKAGE}" | cpio --quiet -idm)
    RPM_IDENTITY="$(rpm -qp --nosignature --qf '%{NAME} %{VERSION} %{ARCH}' "${RPM_PACKAGE}")"
    [[ "${RPM_IDENTITY}" == "${LINUX_PACKAGE_NAME} ${LINUX_PACKAGE_VERSION} ${ARCH}" ]] || {
      echo "error: .rpm identity '${RPM_IDENTITY}' does not equal '${LINUX_PACKAGE_NAME} ${LINUX_PACKAGE_VERSION} ${ARCH}'" >&2
      exit 1
    }
    RPM_REQUIRES="$(rpm -qp --nosignature --requires "${RPM_PACKAGE}")"
    for required in webkit2gtk4.1 glibc gtk3 libsoup3 ca-certificates xdg-utils polkit; do
      [[ "${RPM_REQUIRES}" == *"${required}"* ]] || { echo "error: .rpm Requires lacks ${required}" >&2; exit 1; }
    done
    RPM_SCRIPTS="$(rpm -qp --nosignature --scripts "${RPM_PACKAGE}")"
    for script in preinstall postinstall preuninstall postuninstall; do
      [[ "${RPM_SCRIPTS}" == *"${script} scriptlet"* ]] || { echo "error: .rpm has no ${script} scriptlet" >&2; exit 1; }
    done
    audit_linux_tree ".rpm" "${RPM_TREE}"
    assert_sockets_enabled ".rpm" "${RPM_TREE}" "${RPM_SCRIPTS}"
    if command -v rpmlint >/dev/null 2>&1; then
      rpmlint --config "${ROOT}/packaging/linux/rpm/rpmlint.toml" "${RPM_PACKAGE}" || {
        echo "error: rpmlint reported errors for ${RPM_PACKAGE}" >&2
        exit 1
      }
    else
      echo "::warning title=rpmlint::rpmlint is not installed; the .rpm policy check did not run"
    fi
    # Both families install the same tree apart from the fragments only one ships.
    python3 "${ROOT}/scripts/linux-package-assemble.py" compare --root "${ROOT}" --deb-tree "${DEB_TREE}" --rpm-tree "${RPM_TREE}"
    ENGINE="${DEB_TREE}/${LINUX_INSTALL_ROOT}/bin/pw"
    PROBE_ARGS=(--tree "${DEB_TREE}")
    [[ -z "${PW_PACKAGE_PROBE_LEASE:-}" ]] || PROBE_ARGS+=(--lease "${PW_PACKAGE_PROBE_LEASE}")
    PYTHONPATH="${ROOT}/scripts" python3 -m linux_release.package_probe "${PROBE_ARGS[@]}"
    OPENGREP="$(verify_opengrep "${DEB_TREE}/${LINUX_INSTALL_ROOT}/engine-root" "${ENGINE}")"
    if [[ "${PACKAGE_EXTENSIONS}" == "deb,rpm,pkg.tar.zst" ]]; then
      PACMAN_PACKAGE="$(find "${TARGET_ROOT}" -maxdepth 8 -path '*/release/bundle/pacman/*.pkg.tar.zst' -print -quit)"
      [[ -n "${PACMAN_PACKAGE}" ]] || { echo "error: missing native pacman package" >&2; exit 1; }
      PACMAN_TREE="${EXTRACT}/pacman"
      PYTHONPATH="${ROOT}/scripts" python3 -m linux_release.arch_stage --package "${PACMAN_PACKAGE}" \
        --destination "${PACMAN_TREE}" --root "${ROOT}" --architecture "${ARCH}" --version "${VERSION}"
      audit_linux_tree ".pkg.tar.zst" "${PACMAN_TREE}"
      PACMAN_RECEIPT="$(dirname "${PACMAN_PACKAGE}")/pacman-candidate.json"
      [[ -f "${PACMAN_RECEIPT}" ]] || { echo "error: pacman candidate has no producer receipt" >&2; exit 1; }
      jq -e --arg sha "$(sha256_hex "${PACMAN_PACKAGE}")" --arg arch "${ARCH}" --arg version "${VERSION}" --arg source "$(git -C "${ROOT}" rev-parse HEAD)" \
        '.source_commit == $source and .format_version == 1 and .family == "pacman" and .sha256 == $sha and .architecture == $arch and .version == $version and .published == false' \
        "${PACMAN_RECEIPT}" >/dev/null
    fi
    PACKAGE="${DEB_PACKAGE}"
    RPM_OUTPUT="${RPM_PACKAGE}"
    ;;
  windows)
    PACKAGE="$(find "${TARGET_ROOT}" -maxdepth 8 -path "*/release/bundle/nsis/*.${PACKAGE_EXTENSION}" -print -quit)"
    UPDATER="${PACKAGE}"
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
    DOCUMENT_CORE="${ROOT}/lycaon-den/src-tauri/binaries/pw-document-core-x86_64-pc-windows-msvc.exe"
    DECIDE_ENGINE="${ROOT}/lycaon-den/src-tauri/binaries/bialy-x86_64-pc-windows-msvc.exe"
    GIT_ENGINE="${ROOT}/lycaon-den/src-tauri/engine-root/gitengine/cmd/git.exe"
    GIT_LFS="${ROOT}/lycaon-den/src-tauri/engine-root/gitengine/mingw64/libexec/git-core/git-lfs.exe"
    OPENGREP="$(verify_opengrep "${ROOT}/lycaon-den/src-tauri/engine-root")"
    BROWSER="${ROOT}/lycaon-den/src-tauri/engine-root/browser/chrome-headless-shell.exe"
    DECIDE_MODEL="$(find "${ROOT}/lycaon-den/src-tauri/engine-root/decide/models" -type f -name .complete -print -quit 2>/dev/null || true)"
    [[ -f "${SIDECAR}" && -f "${LOG_VIEWER}" && -f "${DOCUMENT_CORE}" && -f "${DECIDE_ENGINE}" && -f "${GIT_ENGINE}" && -f "${GIT_LFS}" && -f "${OPENGREP}" && -f "${BROWSER}" && -n "${DECIDE_MODEL}" ]] || {
      echo "error: ${PLATFORM} build has incomplete sidecar or bundled tool payloads" >&2
      exit 1
    }
    assert_native_arch "application" "${APPLICATION}"
    assert_native_arch "bundled engine" "${SIDECAR}"
    assert_native_arch "bundled log viewer" "${LOG_VIEWER}"
    assert_native_arch "bundled document core" "${DOCUMENT_CORE}"
    assert_native_arch "bundled decision engine" "${DECIDE_ENGINE}"
    # Staged binaries carry the target triple; the installed layout drops it.
    LYCAON_DOCUMENT_CORE_BINARY="${DOCUMENT_CORE}" "${SIDECAR}" diagnostics document-core
    assert_native_arch "bundled Git" "${GIT_ENGINE}"
    assert_native_arch "bundled Git LFS" "${GIT_LFS}"
    assert_native_arch "bundled OpenGrep" "${OPENGREP}"
    assert_native_arch "bundled browser" "${BROWSER}"
    while IFS= read -r -d '' payload; do
      assert_native_arch "packaged executable payload" "${payload}"
    done < <(find "${ROOT}/lycaon-den/src-tauri/engine-root" -type f -iname '*.exe' -print0)
    pwsh -NoProfile -File "${ROOT}/scripts/release-verify-windows-signature.ps1" \
      -Path "${PACKAGE}" -UpdaterPath "${UPDATER}" -ApplicationPath "${APPLICATION}" \
      -SidecarPath "${SIDECAR}" -LogViewerPath "${LOG_VIEWER}" \
      -DocumentCorePath "${DOCUMENT_CORE}" -DecisionEnginePath "${DECIDE_ENGINE}" \
      -GitPath "${GIT_ENGINE}" -GitLFSPath "${GIT_LFS}" -OpenGrepPath "${OPENGREP}" -BrowserPath "${BROWSER}" \
      -EngineRootPath "${ROOT}/lycaon-den/src-tauri/engine-root" \
      -ExpectedVersion "${WINDOWS_PACKAGE_VERSION}"
    ;;
esac

if [[ -n "${UPDATER_EXTENSION}" ]]; then
SIGNATURE="${UPDATER}.sig"
[[ -f "${SIGNATURE}" ]] || { echo "error: missing updater signature ${SIGNATURE}" >&2; exit 1; }
python3 - "${ROOT}/packaging/update-limits.json" "${UPDATER}" "${VERSION}" "${PLATFORM}" <<'PYLIMITS'
# Staging uses the client's download and expansion limits.
import json, pathlib, sys, tarfile
limits = json.loads(pathlib.Path(sys.argv[1]).read_text())
artifact = pathlib.Path(sys.argv[2])
if limits["format_version"] != 1 or artifact.stat().st_size > limits["max_download_bytes"]:
    raise SystemExit("updater artifact exceeds the client download limit")


def inside_bundle(path: str, link: str) -> bool:
    if link.startswith("/"):
        return False
    depth = len([part for part in path.split("/")[:-1] if part])
    for part in link.split("/"):
        if part == "..":
            if depth == 0:
                return False
            depth -= 1
        elif part not in ("", "."):
            depth += 1
    return True


if artifact.name.endswith(".tar.gz"):
    size = 0
    seen = set()
    root = None
    product_versions = []
    with tarfile.open(artifact, "r|gz") as archive:
        for member in archive:
            parts = [part for part in member.name.split("/") if part]
            if not parts or member.name.startswith("/") or ".." in parts or "." in parts:
                raise SystemExit(f"updater archive entry escapes the bundle: {member.name}")
            if root is None:
                root = parts[0]
                if not root.endswith(".app"):
                    raise SystemExit("updater archive root must be one application bundle")
            if parts[0] != root:
                raise SystemExit("updater archive has more than one root")
            relative = "/".join(parts[1:])
            if relative:
                if relative in seen:
                    raise SystemExit(f"updater archive repeats {relative}")
                seen.add(relative)
            if member.issym():
                if not inside_bundle(relative, member.linkname):
                    raise SystemExit(f"updater archive link leaves the bundle: {member.name}")
            elif not (member.isfile() or member.isdir()):
                raise SystemExit(f"updater archive has an unsupported entry type: {member.name}")
            if relative == "Contents/Resources/release-version":
                if not member.isfile() or member.size > 128:
                    raise SystemExit("invalid updater product version resource")
                product_versions.append(archive.extractfile(member).read().decode().strip())
            size += member.size
            if size > limits["max_expanded_bytes"] or len(seen) > limits["max_archive_entries"]:
                raise SystemExit("updater artifact exceeds the client expansion limit")
    if sys.argv[4].startswith("darwin-") and product_versions != [sys.argv[3]]:
        raise SystemExit("updater bundle product version does not match the release")
PYLIMITS
bash "${ROOT}/scripts/verify-updater-signature.sh" "${UPDATER}" "${SIGNATURE}" "${VERSION}"
fi

mkdir -p "${OUTPUT}/opengrep"
OUTPUT="$(cd "${OUTPUT}" && pwd)"
if [[ "${GOOS}" == linux ]]; then
  mkdir -p "${OUTPUT}/linux-abi"
  cp "${EXTRACT}"/linux-abi-*.json "${OUTPUT}/linux-abi/"
fi
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
stage_package() {
  local source="$1" extension="$2"
  cp "${source}" "${OUTPUT}/${STEM}.${extension}"
  sha256_hex "${OUTPUT}/${STEM}.${extension}" > "${OUTPUT}/${STEM}.${extension}.sha256"
}
stage_package "${PACKAGE}" "${PACKAGE_EXTENSION}"
if [[ "${GOOS}" == linux ]]; then
  stage_package "${RPM_OUTPUT}" "rpm"
  if [[ "${PACKAGE_EXTENSIONS}" == "deb,rpm,pkg.tar.zst" ]]; then
    stage_package "${PACMAN_PACKAGE}" "pkg.tar.zst"
    jq --arg file "${STEM}.pkg.tar.zst" '.file = $file' "${PACMAN_RECEIPT}" > "${OUTPUT}/${STEM}.pacman-candidate.json"
    PYTHONPATH="${ROOT}/scripts" python3 -m linux_release.candidate_inventory --directory "${OUTPUT}" \
      --platform "${PLATFORM}" --architecture "${ARCH}" --version "${VERSION}" \
      --source-commit "$(git -C "${ROOT}" rev-parse HEAD)"
  fi
  # Package-managed platforms publish no updater artifact and no feed fragment.
  exit 0
fi
if [[ "${PACKAGE_EXTENSION}" != "${UPDATER_EXTENSION}" ]]; then
  cp "${UPDATER}" "${OUTPUT}/${STEM}.${UPDATER_EXTENSION}"
fi
cp "${SIGNATURE}" "${OUTPUT}/${STEM}.${UPDATER_EXTENSION}.sig"
jq -n --arg platform "${PLATFORM}" \
  --argjson update_keys "$(python3 "${ROOT}/scripts/release-metadata.py" | jq '{signing_generation,embedded_generation,signing_key_fingerprint,embedded_key_fingerprint}')" \
  --arg signature "$(cat "${SIGNATURE}")" \
  --arg updater_artifact "${STEM}.${UPDATER_EXTENSION}" \
  '{platform:$platform,signature:$signature,updater_artifact:$updater_artifact,update_keys:$update_keys}' \
  > "${OUTPUT}/fragment-${PLATFORM}.json"
