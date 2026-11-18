#!/usr/bin/env bash
# Validate release inputs without modifying them.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
MODE=""
BASELINE_REF=""
SOURCE_ROOT=""
PUBLISHED_VERSIONS="[]"

usage() {
  cat >&2 <<'EOF'
Usage: release-preflight.sh --static
       release-preflight.sh --require-corpus
       release-preflight.sh --compatibility-only --source-root DIR --baseline-ref COMMIT
                            [--published-versions JSON]

  --static              Check version projections, the changelog section, update
                        keys, the platform catalog, product URLs and recovery
                        metadata. Reads source only and builds nothing.
  --require-corpus      Build the development engine and verify
                        lycaon/testdata/upgrade-corpus/<VERSION>/ against it:
                        version, schema, stores and archive integrity.
  --compatibility-only  Check DIR against the published stable release at COMMIT
                        from a separate controller checkout.
EOF
  exit 2
}

select_mode() {
  [[ -z "${MODE}" ]] || usage
  MODE="$1"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --static) select_mode static; shift ;;
    --require-corpus) select_mode corpus; shift ;;
    --compatibility-only) select_mode compatibility; shift ;;
    --baseline-ref) [[ $# -ge 2 && -n "$2" ]] || usage; BASELINE_REF="$2"; shift 2 ;;
    --published-versions) [[ $# -ge 2 && -n "$2" ]] || usage; PUBLISHED_VERSIONS="$2"; shift 2 ;;
    --source-root) [[ $# -ge 2 && -n "$2" ]] || usage; SOURCE_ROOT="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "error: unknown argument: $1" >&2; usage ;;
  esac
done

require_tools() {
  local need
  for need in "$@"; do
    command -v "${need}" >/dev/null 2>&1 || {
      echo "error: ${need} is required for release preflight" >&2
      exit 1
    }
  done
}

fail=0
check_equal() {
  local label="$1" got="$2" want="$3"
  if [[ "${got}" != "${want}" ]]; then
    printf 'error: %s=%q; want %q\n' "${label}" "${got}" "${want}" >&2
    fail=1
  fi
}

check_compatibility() {
  [[ -n "${BASELINE_REF}" && -d "${SOURCE_ROOT}" ]] || usage
  (cd "${ROOT}/lycaon" && LYCAON_ENGINE_ROOT="${LYCAON_ENGINE_ROOT:-${ROOT}/lycaon-den/src-tauri/engine-root}" \
    go run ./cmd/release-contracts --check-all --baseline "${BASELINE_REF}" --repo-root "${SOURCE_ROOT}" --published-versions "${PUBLISHED_VERSIONS}")
  local candidate_version
  candidate_version="$(tr -d '[:space:]' < "${SOURCE_ROOT}/VERSION")"
  python3 "${ROOT}/scripts/upgrade-fixture-files.py" candidate-static \
    "${SOURCE_ROOT}/lycaon/testdata/upgrade-corpus/${candidate_version}" \
    --schema-lock "${SOURCE_ROOT}/lycaon/internal/db/schema_version_lock.json" --version "${candidate_version}" \
    --inventory-root "${SOURCE_ROOT}/lycaon/testdata/upgrade-corpus" --published-versions "${PUBLISHED_VERSIONS}"
}

check_static() {
  require_tools jq awk python3 bun
  eval "$(python3 "${ROOT}/scripts/release-metadata.py" --root "${ROOT}" --format shell)"
  local version="${PRODUCT_VERSION}"

  local package_version tauri_version cargo_version cargo_lock_version bundle_version
  package_version="$(jq -r '.version' "${ROOT}/lycaon-den/package.json")"
  tauri_version="$(jq -r '.version' "${ROOT}/lycaon-den/src-tauri/tauri.conf.json")"
  cargo_version="$(awk -F'"' '/^version = "/{print $2; exit}' "${ROOT}/lycaon-den/src-tauri/Cargo.toml")"
  cargo_lock_version="$(awk '/name = "painted-wolf-code"/{found=1; next} found && /^version = "/{gsub(/"/, "", $3); print $3; exit}' "${ROOT}/lycaon-den/src-tauri/Cargo.lock")"
  bundle_version="$(jq -r '.bundle.macOS.bundleVersion // empty' "${ROOT}/lycaon-den/src-tauri/tauri.conf.json")"
  check_equal "Den package version" "${package_version}" "${version}"
  check_equal "Tauri native version" "${tauri_version}" "${NATIVE_VERSION}"
  check_equal "Cargo native version" "${cargo_version}" "${NATIVE_VERSION}"
  check_equal "Cargo.lock native version" "${cargo_lock_version}" "${NATIVE_VERSION}"
  check_equal "macOS bundle build" "${bundle_version}" "${MACOS_BUNDLE_VERSION}"

  local changelog_notes
  changelog_notes="$(awk -v heading="## [${version}]" 'index($0, heading) == 1 {flag=1; next} /^## \[/ {flag=0} flag' "${ROOT}/CHANGELOG.md")"
  if [[ -z "$(printf '%s' "${changelog_notes}" | tr -d '[:space:]')" ]]; then
    echo "error: CHANGELOG.md has no non-empty ## [${version}] section" >&2
    fail=1
  fi

  # Clients embed both keys of the embedded generation straight from the registry.
  if ! python3 - "${ROOT}/scripts" "${EMBEDDED_GENERATION}" <<'PYKEYS'
import sys
sys.path.insert(0, sys.argv[1])
from update_keys import fingerprint, generation, load_registry
row = generation(load_registry(), int(sys.argv[2]))
if fingerprint(row["public_key"]) == fingerprint(row["feed_public_key"]):
    raise SystemExit("the feed key must differ from the artifact key")
PYKEYS
  then
    echo "error: packaging/update-keys.json does not carry valid artifact and feed keys for generation ${EMBEDDED_GENERATION}" >&2
    fail=1
  fi

  if ! python3 -c 'import sys
sys.path.insert(0, sys.argv.pop(1))
from pathlib import Path
from release_distribution_contract.platforms import validate_release_catalog
validate_release_catalog(Path(sys.argv[1]).read_bytes())' "${ROOT}/scripts" "${ROOT}/packaging/release-platforms.json"; then
    echo "error: packaging/release-platforms.json is invalid" >&2
    fail=1
  fi

  local issues_url homebrew_homepage
  issues_url="$(bun -e 'const brand = await import(process.argv[1]); process.stdout.write(brand.ISSUES_URL)' "${ROOT}/lycaon-den/shared/brand.ts")"
  check_equal "issues URL" "${issues_url}" "https://github.com/paintedwolf-ai/paintedwolf-code/issues"

  homebrew_homepage="$(sed -n 's/^  homepage "\([^"]*\)"/\1/p' "${ROOT}/packaging/homebrew/painted-wolf-code.rb.tmpl")"
  check_equal "Homebrew homepage" "${homebrew_homepage}" "https://paintedwolf.ai"

  local recovery="${ROOT}/.release/recovery.json"
  if [[ -f "${recovery}" ]]; then
    if ! jq -e '
      type == "object"
      and (keys == ["restores_from", "signing_generation", "version", "withdraws"])
      and ([.version, .withdraws, .restores_from] | all(type == "string" and length > 0))
    ' "${recovery}" >/dev/null; then
      echo "error: .release/recovery.json does not satisfy the recovery metadata schema" >&2
      fail=1
    else
      local recovery_version withdraws restores_from
      recovery_version="$(jq -r '.version' "${recovery}")"
      withdraws="$(jq -r '.withdraws' "${recovery}")"
      restores_from="$(jq -r '.restores_from' "${recovery}")"
      check_equal "recovery version" "${recovery_version}" "${version}"
      python3 "${ROOT}/scripts/semver-compare.py" gt "${version}" "${withdraws}" >/dev/null || {
        echo "error: recovery VERSION=${version} must be newer than withdrawn ${withdraws}" >&2
        fail=1
      }
      python3 "${ROOT}/scripts/semver-compare.py" gt "${withdraws}" "${restores_from}" >/dev/null || {
        echo "error: withdrawn ${withdraws} must be newer than restored source ${restores_from}" >&2
        fail=1
      }
    fi
  fi
}

check_corpus() {
  require_tools python3
  eval "$(python3 "${ROOT}/scripts/release-metadata.py" --root "${ROOT}" --format shell)"
  local version="${PRODUCT_VERSION}" corpus
  corpus="${ROOT}/lycaon/testdata/upgrade-corpus/${version}"
  if [[ ! -d "${corpus}" ]]; then
    echo "error: versioned upgrade corpus missing: ${corpus}" >&2
    echo "       Run ./task upgrade:corpus:prepare after the version is final." >&2
    fail=1
  elif [[ ! -f "${corpus}/MANIFEST.json" ]]; then
    echo "error: ${corpus}/MANIFEST.json missing" >&2
    fail=1
  else
    "${ROOT}/task" build:lycaon-dev
    python3 "${ROOT}/scripts/upgrade-fixture-files.py" candidate "${corpus}" \
      --sidecar "${PW_BUILD_DIR}/lycaon-dev" --version "${version}" \
      --inventory-root "${ROOT}/lycaon/testdata/upgrade-corpus" || fail=1
  fi
}

[[ "${MODE}" == compatibility || -z "${SOURCE_ROOT}${BASELINE_REF}" ]] || usage
case "${MODE}" in
  compatibility) check_compatibility ;;
  static) check_static ;;
  corpus) check_corpus ;;
  *) usage ;;
esac
[[ "${fail}" -eq 0 ]] || exit 1
echo "release preflight passed: ${MODE}" >&2
