#!/usr/bin/env bash
# Validate release inputs without modifying them.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
REQUIRE_CORPUS=0

usage() {
  cat >&2 <<'EOF'
Usage: release-preflight.sh [--require-corpus]

  --require-corpus  Require lycaon/testdata/upgrade-corpus/<VERSION>/ and verify
                    its version, schema, stores, and archive integrity.
                    Publishing tags always use this mode.
EOF
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --require-corpus) REQUIRE_CORPUS=1; shift ;;
    -h|--help) usage ;;
    *) echo "error: unknown argument: $1" >&2; usage ;;
  esac
done

for need in jq awk python3 bun; do
  command -v "${need}" >/dev/null 2>&1 || {
    echo "error: ${need} is required for release preflight" >&2
    exit 1
  }
done

eval "$(python3 "${ROOT}/scripts/release-metadata.py" --root "${ROOT}" --format shell)"
VERSION="${PRODUCT_VERSION}"

fail=0
check_equal() {
  local label="$1" got="$2" want="$3"
  if [[ "${got}" != "${want}" ]]; then
    printf 'error: %s=%q; want %q\n' "${label}" "${got}" "${want}" >&2
    fail=1
  fi
}

PACKAGE_VERSION="$(jq -r '.version' "${ROOT}/lycaon-den/package.json")"
TAURI_VERSION="$(jq -r '.version' "${ROOT}/lycaon-den/src-tauri/tauri.conf.json")"
CARGO_VERSION="$(awk -F'"' '/^version = "/{print $2; exit}' "${ROOT}/lycaon-den/src-tauri/Cargo.toml")"
CARGO_LOCK_VERSION="$(awk '/name = "painted-wolf-code"/{found=1; next} found && /^version = "/{gsub(/"/, "", $3); print $3; exit}' "${ROOT}/lycaon-den/src-tauri/Cargo.lock")"
BUNDLE_VERSION="$(jq -r '.bundle.macOS.bundleVersion // empty' "${ROOT}/lycaon-den/src-tauri/tauri.conf.json")"
check_equal "Den package version" "${PACKAGE_VERSION}" "${VERSION}"
check_equal "Tauri native version" "${TAURI_VERSION}" "${NATIVE_VERSION}"
check_equal "Cargo native version" "${CARGO_VERSION}" "${NATIVE_VERSION}"
check_equal "Cargo.lock native version" "${CARGO_LOCK_VERSION}" "${NATIVE_VERSION}"
check_equal "macOS bundle build" "${BUNDLE_VERSION}" "${MACOS_BUNDLE_VERSION}"

CHANGELOG_NOTES="$(awk -v heading="## [${VERSION}]" 'index($0, heading) == 1 {flag=1; next} /^## \[/ {flag=0} flag' "${ROOT}/CHANGELOG.md")"
if [[ -z "$(printf '%s' "${CHANGELOG_NOTES}" | tr -d '[:space:]')" ]]; then
  echo "error: CHANGELOG.md has no non-empty ## [${VERSION}] section" >&2
  fail=1
fi

TAURI_CONF="${ROOT}/lycaon-den/src-tauri/tauri.conf.json"
PUBKEY="$(jq -r '.plugins.updater.pubkey // empty' "${TAURI_CONF}")"
if ! python3 - "${PUBKEY}" <<'PY'
import base64
import sys

raw = sys.argv[1].strip()
if not raw:
    raise SystemExit("empty pubkey")
try:
    decoded = base64.b64decode(raw, validate=True).decode("utf-8")
except Exception as exc:
    raise SystemExit(f"pubkey is not base64: {exc}")
lines = [line for line in decoded.splitlines() if line.strip()]
if len(lines) != 2 or not lines[0].startswith("untrusted comment:"):
    raise SystemExit("pubkey does not decode to a minisign public key file")
try:
    key = base64.b64decode(lines[1], validate=True)
except Exception as exc:
    raise SystemExit(f"pubkey key line is not base64: {exc}")
if len(key) != 42 or key[:2] != b"Ed":
    raise SystemExit("pubkey key material is not a minisign Ed25519 key")
PY
then
  echo "error: tauri.conf.json updater pubkey is not a valid minisign public key" >&2
  fail=1
fi
ENDPOINT_COUNT="$(jq '.plugins.updater.endpoints | length' "${TAURI_CONF}")"
ENDPOINT="$(jq -r '.plugins.updater.endpoints[0] // empty' "${TAURI_CONF}")"
check_equal "updater endpoint count" "${ENDPOINT_COUNT}" "1"
check_equal "updater endpoint" "${ENDPOINT}" "https://downloads.paintedwolf.dev/updates/stable/key-${EMBEDDED_GENERATION}/latest.json"
EXPECTED_PUBKEY="$(jq -r --argjson generation "${EMBEDDED_GENERATION}" '.generations[] | select(.generation == $generation) | .public_key' "${ROOT}/packaging/update-keys.json")"
check_equal "embedded updater public key" "${PUBKEY}" "${EXPECTED_PUBKEY}"

if ! jq -e '
  .schema_version == 1
  and (.platforms | type == "array" and length > 0)
  and ([.platforms[].updater_key] | length == (unique | length))
  and all(.platforms[];
    ((keys | sort) == ["artifact_arch", "goarch", "goos", "package_extension", "publication", "runner", "updater_extension", "updater_key"])
    and (.updater_key | test("^[a-z0-9]+-[a-z0-9_]+$"))
    and (.runner | type == "string" and length > 0)
    and (.artifact_arch | test("^(aarch64|x86_64)$"))
    and (.goos | test("^(darwin|linux|windows)$"))
    and (.goarch | test("^(arm64|amd64)$"))
    and (.publication | test("^(public|candidate)$"))
    and (.package_extension | test("^[A-Za-z0-9.]+$"))
    and (.updater_extension | test("^[A-Za-z0-9.]+$")))
  and ([.platforms[] | select(.goos == "linux") | .updater_key] | sort
    == ["linux-aarch64", "linux-x86_64"])
  and ([.platforms[] | select(.goos == "windows") | .updater_key]
    == ["windows-x86_64"])
  and ([.platforms[] | select(.publication == "public") | .updater_key]
    == ["darwin-aarch64"])
' "${ROOT}/packaging/release-platforms.json" >/dev/null; then
  echo "error: packaging/release-platforms.json is invalid" >&2
  fail=1
fi

ISSUES_URL="$(bun -e 'const brand = await import(process.argv[1]); process.stdout.write(brand.ISSUES_URL)' "${ROOT}/lycaon-den/shared/brand.ts")"
check_equal "issues URL" "${ISSUES_URL}" "https://github.com/paintedwolf-ai/paintedwolf-code/issues"

HOMEBREW_HOMEPAGE="$(sed -n 's/^  homepage "\([^"]*\)"/\1/p' "${ROOT}/packaging/homebrew/painted-wolf-code.rb.tmpl")"
check_equal "Homebrew homepage" "${HOMEBREW_HOMEPAGE}" "https://paintedwolf.ai"

RECOVERY="${ROOT}/.release/recovery.json"
if [[ -f "${RECOVERY}" ]]; then
  if ! jq -e '
    type == "object"
    and (keys == ["restores_from", "signing_generation", "version", "withdraws"])
    and ([.version, .withdraws, .restores_from] | all(type == "string" and length > 0))
  ' "${RECOVERY}" >/dev/null; then
    echo "error: .release/recovery.json does not satisfy the recovery metadata schema" >&2
    fail=1
  else
    RECOVERY_VERSION="$(jq -r '.version' "${RECOVERY}")"
    WITHDRAWS="$(jq -r '.withdraws' "${RECOVERY}")"
    RESTORES_FROM="$(jq -r '.restores_from' "${RECOVERY}")"
    check_equal "recovery version" "${RECOVERY_VERSION}" "${VERSION}"
    python3 "${ROOT}/scripts/semver-compare.py" gt "${VERSION}" "${WITHDRAWS}" >/dev/null || {
      echo "error: recovery VERSION=${VERSION} must be newer than withdrawn ${WITHDRAWS}" >&2
      fail=1
    }
    python3 "${ROOT}/scripts/semver-compare.py" gt "${WITHDRAWS}" "${RESTORES_FROM}" >/dev/null || {
      echo "error: withdrawn ${WITHDRAWS} must be newer than restored source ${RESTORES_FROM}" >&2
      fail=1
    }
  fi
fi

if [[ "${REQUIRE_CORPUS}" -eq 1 ]]; then
  CORPUS="${ROOT}/lycaon/testdata/upgrade-corpus/${VERSION}"
  if [[ ! -d "${CORPUS}" ]]; then
    echo "error: versioned upgrade corpus missing: ${CORPUS}" >&2
    echo "       Run ./task upgrade:corpus:prepare after the version is final." >&2
    fail=1
  elif [[ ! -f "${CORPUS}/MANIFEST.json" ]]; then
    echo "error: ${CORPUS}/MANIFEST.json missing" >&2
    fail=1
  else
    python3 "${ROOT}/scripts/upgrade-fixture-files.py" candidate "${CORPUS}" \
      --sidecar "${PW_BUILD_DIR}/lycaon-dev" --version "${VERSION}" || fail=1
  fi
fi

if [[ "${fail}" -ne 0 ]]; then
  exit 1
fi
echo "release preflight passed for ${VERSION}" >&2
