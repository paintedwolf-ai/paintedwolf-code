#!/usr/bin/env bash
# Exercise release controls under an isolated object prefix.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=release-r2-rest.sh
source "${ROOT}/scripts/release-r2-rest.sh"
WRANGLER=(bunx wrangler@4.115.0)

for name in \
  RELEASE_TEST_R2_ACCOUNT_ID RELEASE_TEST_R2_API_TOKEN \
  RELEASE_TEST_R2_BUCKET RELEASE_TEST_DOWNLOAD_BASE_URL; do
  [[ -n "${!name:-}" ]] || {
    echo "error: ${name} is required for the credentialed release live test" >&2
    exit 1
  }
done
[[ "${RELEASE_TEST_DOWNLOAD_BASE_URL}" =~ ^https://[^/?#]+/?$ ]] || {
  echo "error: RELEASE_TEST_DOWNLOAD_BASE_URL must be an HTTPS origin with no path, query, or fragment" >&2
  exit 1
}

export CLOUDFLARE_ACCOUNT_ID="${RELEASE_TEST_R2_ACCOUNT_ID}"
export CLOUDFLARE_API_TOKEN="${RELEASE_TEST_R2_API_TOKEN}"
export R2_BUCKET="${RELEASE_TEST_R2_BUCKET}"

RUN_NONCE="$(python3 -c 'import uuid; print(uuid.uuid4().hex)')"
if [[ -n "${GITHUB_RUN_ID:-}" && -n "${GITHUB_RUN_ATTEMPT:-}" ]]; then
  RUN_ID="gha-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}-${RUN_NONCE}"
else
  RUN_ID="local-$(date -u +%Y%m%dT%H%M%SZ)-${RUN_NONCE}"
fi
STORAGE_PREFIX="release-system-tests/${RUN_ID}"
export DOWNLOAD_BASE_URL="${RELEASE_TEST_DOWNLOAD_BASE_URL%/}/${STORAGE_PREFIX}"
GENERATION="$(jq -r '.generations[-1].generation' "${ROOT}/packaging/update-keys.json")"
POINTER_REL="updates/stable/key-${GENERATION}/latest.json"
POINTER_KEY="${STORAGE_PREFIX}/${POINTER_REL}"
PREVIEW_POINTER_REL="updates/preview/key-${GENERATION}/latest.json"
PREVIEW_POINTER_KEY="${STORAGE_PREFIX}/${PREVIEW_POINTER_REL}"
RELEASES_REL="updates/releases"
VERSIONS=(0.0.900001 0.0.900002 0.0.900003)

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/release-r2-live-test.XXXXXX")"
declare -a CREATED_KEYS=()
CLEANED=0

object_url() {
  local key="$1"
  printf 'https://api.cloudflare.com/client/v4/accounts/%s/r2/buckets/%s/objects/%s' \
    "${CLOUDFLARE_ACCOUNT_ID}" "${R2_BUCKET}" "${key}"
}

get_status() {
  local key="$1" output="$2"
  curl --silent --show-error --output "${output}" --write-out '%{http_code}' \
    -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" "$(object_url "${key}")"
}

delete_created_objects() {
  local failed=0 key
  [[ "${CLEANED}" -eq 0 ]] || return 0
  for key in "${CREATED_KEYS[@]}"; do
    "${WRANGLER[@]}" r2 object delete "${R2_BUCKET}/${key}" --remote \
      >/dev/null 2>&1 || failed=1
  done
  [[ "${failed}" -ne 0 ]] || CLEANED=1
  return "${failed}"
}

finish() {
  local status=$?
  trap - EXIT
  if ! delete_created_objects; then
    echo "error: release live-test cleanup failed under ${STORAGE_PREFIX}" >&2
    [[ "${status}" -ne 0 ]] || status=1
  fi
  rm -rf "${WORKDIR}"
  exit "${status}"
}
trap finish EXIT

register_key() {
  CREATED_KEYS+=("$1")
}

require_absent() {
  local key="$1" stat
  stat="$(r2_rest_object_stat "${key}")" || {
    echo "error: transport failure checking isolated object ${key}" >&2
    return 1
  }
  [[ -z "${stat}" ]] || {
    echo "error: isolated test object already exists at ${key}; refusing to overwrite" >&2
    return 1
  }
}

immutable_put() {
  local key="$1" file="$2" content_type="$3"
  require_absent "${key}"
  register_key "${key}"
  bash "${ROOT}/scripts/release-r2-immutable-put.sh" \
    --key "${key}" --file "${file}" --content-type "${content_type}"
}

public_equals() {
  local relative="$1" expected="$2" output="$3" attempt
  for attempt in $(seq 1 60); do
    if curl --fail --silent --show-error --location \
      -H 'Cache-Control: no-cache' \
      "${DOWNLOAD_BASE_URL}/${relative}?live_test=${RUN_ID}-${attempt}" \
      --output "${output}" && cmp -s "${expected}" "${output}"; then
      return 0
    fi
    sleep 2
  done
  echo "error: public object did not converge: ${relative}" >&2
  return 1
}

public_headers() {
  local relative="$1" output="$2"
  curl --fail --silent --show-error \
    -H 'Cache-Control: no-cache' \
    --dump-header "${output}" --output /dev/null \
    "${DOWNLOAD_BASE_URL}/${relative}?live_test_headers=${RUN_ID}-${RANDOM}"
}

require_header_tokens() {
  local headers="$1" header="$2"
  shift 2
  local value token
  value="$(awk -v wanted="${header}" '
    tolower($0) ~ "^" tolower(wanted) ":" {
      sub("^[^:]*:[[:space:]]*", "")
      sub("\\r$", "")
      print
      exit
    }
  ' "${headers}")"
  [[ -n "${value}" ]] || {
    echo "error: public response is missing ${header}" >&2
    return 1
  }
  value="$(printf '%s' "${value}" | tr '[:upper:]' '[:lower:]')"
  for token in "$@"; do
    token="$(printf '%s' "${token}" | tr '[:upper:]' '[:lower:]')"
    [[ "${value}" == *"${token}"* ]] || {
      echo "error: public ${header} is missing ${token}: ${value}" >&2
      return 1
    }
  done
}

write_manifest() {
  local version="$1" output="$2"
  jq -n \
    --arg version "${version}" \
    --argjson update_keys "$(PYTHONPATH="${ROOT}/scripts" python3 -c 'from update_keys import load_registry,release_binding; import json; r=load_registry(); r["signing_generation"]=r["embedded_generation"]=len(r["generations"]); print(json.dumps(release_binding(r,"0.0.1-live.1")))')" \
    --arg notes "Credentialed release-system rehearsal ${RUN_ID}." \
    --arg pub_date "2026-01-01T00:00:00Z" \
    --arg signature "$(python3 "${ROOT}/scripts/updater_signature.py" rehearsal "${version}")" \
    --arg base "${DOWNLOAD_BASE_URL}" \
    --slurpfile catalog "${ROOT}/packaging/release-platforms.json" \
    '{version: $version, update_keys: $update_keys, notes: $notes, pub_date: $pub_date,
      platforms: (reduce ($catalog[0].platforms[] | select(.publication == "public")) as $platform ({};
        .[$platform.updater_key] = {
          signature: $signature,
          url: ($base + "/releases/v" + $version + "/painted-wolf-code_v" +
            $version + "_" + $platform.updater_key + "." + $platform.updater_extension)
        }))}' \
    > "${output}"
}

echo "release-live-test: isolated prefix ${STORAGE_PREFIX}" >&2
probe="${WORKDIR}/probe"
probe_status="$(get_status "${STORAGE_PREFIX}/permission-probe" "${probe}")" || {
  echo "error: R2 credential probe failed" >&2
  exit 1
}
[[ "${probe_status}" == 404 ]] || {
  echo "error: isolated permission probe returned HTTP ${probe_status}; expected 404" >&2
  exit 1
}
public_probe_status="$(curl --silent --show-error --output "${WORKDIR}/public-probe" \
  --write-out '%{http_code}' \
  "${DOWNLOAD_BASE_URL}/permission-probe?live_test_probe=${RUN_ID}")" || {
  echo "error: public-domain permission probe failed" >&2
  exit 1
}
[[ "${public_probe_status}" == 404 ]] || {
  echo "error: public-domain missing object returned HTTP ${public_probe_status}; expected 404" >&2
  exit 1
}
echo "release-live-test: credential and public-domain routing verified" >&2

printf 'immutable-one\n' > "${WORKDIR}/immutable-one"
printf 'immutable-two\n' > "${WORKDIR}/immutable-two"
IMMUTABLE_KEY="${STORAGE_PREFIX}/immutable/retry.txt"
immutable_put "${IMMUTABLE_KEY}" "${WORKDIR}/immutable-one" text/plain
bash "${ROOT}/scripts/release-r2-immutable-put.sh" \
  --key "${IMMUTABLE_KEY}" --file "${WORKDIR}/immutable-one" --content-type text/plain
if bash "${ROOT}/scripts/release-r2-immutable-put.sh" \
  --key "${IMMUTABLE_KEY}" --file "${WORKDIR}/immutable-two" --content-type text/plain \
  >"${WORKDIR}/collision.out" 2>&1; then
  echo "error: immutable object accepted changed bytes" >&2
  exit 1
fi
grep -Fq "already exists with different bytes" "${WORKDIR}/collision.out"
public_equals "immutable/retry.txt" "${WORKDIR}/immutable-one" "${WORKDIR}/public-immutable"
public_headers "immutable/retry.txt" "${WORKDIR}/immutable-headers"
require_header_tokens "${WORKDIR}/immutable-headers" Cache-Control \
  public max-age=31536000 immutable
require_header_tokens "${WORKDIR}/immutable-headers" Content-Type text/plain
echo "release-live-test: immutable retry, collision, bytes, and caching verified" >&2

for version in "${VERSIONS[@]}"; do
  manifest_rel="${RELEASES_REL}/${version}.json"
  manifest_key="${STORAGE_PREFIX}/${manifest_rel}"
  write_manifest "${version}" "${WORKDIR}/manifest-${version}.json"
  bash "${ROOT}/scripts/release-validate-updater-manifest.sh" \
    --file "${WORKDIR}/manifest-${version}.json" --version "${version}"
  while IFS=$'\t' read -r platform extension; do
    platform_artifact="${platform}.${extension}"
    artifact_rel="releases/v${version}/painted-wolf-code_v${version}_${platform_artifact}"
    artifact_key="${STORAGE_PREFIX}/${artifact_rel}"
    artifact_file="${WORKDIR}/artifact-${version}-${platform_artifact}"
    printf 'updater-fixture:%s:%s:%s\n' "${RUN_ID}" "${version}" "${platform_artifact}" \
      > "${artifact_file}"
    immutable_put "${artifact_key}" "${artifact_file}" application/gzip
    public_equals "${artifact_rel}" "${artifact_file}" \
      "${WORKDIR}/public-artifact-${version}-${platform_artifact}"
  done < <(jq -r '.platforms[] | select(.publication == "public") | [.updater_key, .updater_extension] | @tsv' \
    "${ROOT}/packaging/release-platforms.json")
  immutable_put "${manifest_key}" "${WORKDIR}/manifest-${version}.json" application/json
  public_equals "${manifest_rel}" "${WORKDIR}/manifest-${version}.json" \
    "${WORKDIR}/public-manifest-${version}"
done
echo "release-live-test: versioned updater artifacts and manifests verified" >&2

jq '.platforms["darwin-aarch64"].url = "https://invalid.example/update"' \
  "${WORKDIR}/manifest-${VERSIONS[0]}.json" > "${WORKDIR}/malformed.json"
if bash "${ROOT}/scripts/release-validate-updater-manifest.sh" \
  --file "${WORKDIR}/malformed.json" >"${WORKDIR}/malformed.out" 2>&1; then
  echo "error: updater-manifest validator accepted a foreign artifact URL" >&2
  exit 1
fi
echo "release-live-test: malformed updater manifest rejected" >&2

DMG_REL="releases/v${VERSIONS[0]}/painted-wolf-code_v${VERSIONS[0]}_darwin-aarch64.dmg"
printf 'dmg-fixture:%s:%s\n' "${RUN_ID}" "${VERSIONS[0]}" > "${WORKDIR}/fixture.dmg"
DMG_SHA="$(ruby -rdigest -e 'puts Digest::SHA256.file(ARGV.fetch(0)).hexdigest' \
  "${WORKDIR}/fixture.dmg")"
immutable_put "${STORAGE_PREFIX}/${DMG_REL}" "${WORKDIR}/fixture.dmg" \
  application/x-apple-diskimage
public_equals "${DMG_REL}" "${WORKDIR}/fixture.dmg" "${WORKDIR}/public-fixture.dmg"

sed -e "s|@@VERSION@@|${VERSIONS[0]}|g" \
    -e "s|@@SHA_ARM@@|${DMG_SHA}|g" \
    -e "s|@@BASE_URL@@|${DOWNLOAD_BASE_URL}|g" \
    -e "s|@@TOKEN@@|painted-wolf-code|g" \
    -e "s|@@CHANNEL@@|stable|g" \
    -e "s|@@CONFLICTS@@|painted-wolf-code@preview|g" \
    "${ROOT}/packaging/homebrew/painted-wolf-code.rb.tmpl" > "${WORKDIR}/cask.rb"
ruby -c "${WORKDIR}/cask.rb" >/dev/null
CASK_REL="release-metadata/${VERSIONS[0]}/painted-wolf-code.rb"
immutable_put "${STORAGE_PREFIX}/${CASK_REL}" "${WORKDIR}/cask.rb" text/plain
public_equals "${CASK_REL}" "${WORKDIR}/cask.rb" "${WORKDIR}/public-cask.rb"
echo "release-live-test: rendered Homebrew metadata verified" >&2

require_absent "${POINTER_KEY}"
register_key "${POINTER_KEY}"
for version in "${VERSIONS[@]}"; do
  register_key "${STORAGE_PREFIX}/release-metadata/${version}/activation.json"
done
bash "${ROOT}/scripts/release-r2-publish-pointer.sh" \
  --file "${WORKDIR}/manifest-${VERSIONS[0]}.json" \
  --channel stable --storage-prefix "${STORAGE_PREFIX}"
bash "${ROOT}/scripts/release-r2-publish-pointer.sh" \
  --file "${WORKDIR}/manifest-${VERSIONS[0]}.json" \
  --channel stable --storage-prefix "${STORAGE_PREFIX}"
bash "${ROOT}/scripts/release-r2-publish-pointer.sh" \
  --file "${WORKDIR}/manifest-${VERSIONS[1]}.json" \
  --channel stable --storage-prefix "${STORAGE_PREFIX}"
public_headers "${POINTER_REL}" "${WORKDIR}/pointer-headers"
require_header_tokens "${WORKDIR}/pointer-headers" Cache-Control \
  no-cache no-store must-revalidate
require_header_tokens "${WORKDIR}/pointer-headers" Content-Type application/json
echo "release-live-test: pointer activation, retry, public bytes, and caching verified" >&2

require_absent "${PREVIEW_POINTER_KEY}"
register_key "${PREVIEW_POINTER_KEY}"
bash "${ROOT}/scripts/release-r2-publish-pointer.sh" \
  --file "${WORKDIR}/manifest-${VERSIONS[0]}.json" \
  --channel preview --storage-prefix "${STORAGE_PREFIX}"
for version in "${VERSIONS[@]:0:2}"; do
  python3 "${ROOT}/scripts/release_distribution.py" activate \
    --file "${WORKDIR}/manifest-${version}.json" \
    --output "${WORKDIR}/active-${version}.json" --storage-prefix "${STORAGE_PREFIX}"
done
public_equals "${PREVIEW_POINTER_REL}" "${WORKDIR}/active-${VERSIONS[0]}.json" \
  "${WORKDIR}/public-preview.json"
public_equals "${POINTER_REL}" "${WORKDIR}/active-${VERSIONS[1]}.json" \
  "${WORKDIR}/public-stable.json"
bash "${ROOT}/scripts/release-r2-publish-pointer.sh" \
  --file "${WORKDIR}/manifest-${VERSIONS[1]}.json" \
  --channel preview --storage-prefix "${STORAGE_PREFIX}" --advance-if-newer
bash "${ROOT}/scripts/release-r2-publish-pointer.sh" \
  --file "${WORKDIR}/manifest-${VERSIONS[0]}.json" \
  --channel preview --storage-prefix "${STORAGE_PREFIX}" --advance-if-newer
public_equals "${PREVIEW_POINTER_REL}" "${WORKDIR}/active-${VERSIONS[1]}.json" \
  "${WORKDIR}/public-preview-after-stable.json"
echo "release-live-test: stable and preview pointers are isolated" >&2

if bash "${ROOT}/scripts/release-r2-publish-pointer.sh" \
  --file "${WORKDIR}/manifest-${VERSIONS[0]}.json" \
  --channel stable --storage-prefix "${STORAGE_PREFIX}" \
  >"${WORKDIR}/regression.out" 2>&1; then
  echo "error: ordinary updater activation accepted a SemVer regression" >&2
  exit 1
fi
grep -Fq "updater pointer may only advance" "${WORKDIR}/regression.out"

if bash "${ROOT}/scripts/release-r2-publish-pointer.sh" \
  --file "${WORKDIR}/manifest-${VERSIONS[0]}.json" \
  --channel stable --storage-prefix "${STORAGE_PREFIX}" \
  --from-version "${VERSIONS[2]}" >"${WORKDIR}/cas.out" 2>&1; then
  echo "error: halt compare-and-swap accepted the wrong active version" >&2
  exit 1
fi
grep -Fq "expected ${VERSIONS[2]}" "${WORKDIR}/cas.out"
echo "release-live-test: pointer regression and compare-and-swap rejected" >&2

bash "${ROOT}/scripts/release-halt.sh" \
  --channel stable --bad "${VERSIONS[1]}" --last-good "${VERSIONS[0]}" \
  --storage-prefix "${STORAGE_PREFIX}"
bash "${ROOT}/scripts/release-halt.sh" \
  --channel stable --bad "${VERSIONS[1]}" --last-good "${VERSIONS[0]}" \
  --storage-prefix "${STORAGE_PREFIX}"

bash "${ROOT}/scripts/release-r2-publish-pointer.sh" \
  --file "${WORKDIR}/manifest-${VERSIONS[2]}.json" \
  --channel stable --storage-prefix "${STORAGE_PREFIX}"
if bash "${ROOT}/scripts/release-halt.sh" \
  --channel stable --bad "${VERSIONS[1]}" --last-good "${VERSIONS[0]}" \
  --storage-prefix "${STORAGE_PREFIX}" >"${WORKDIR}/stale-halt.out" 2>&1; then
  echo "error: stale halt replaced a newer recovery pointer" >&2
  exit 1
fi
grep -Fq "active updater version is ${VERSIONS[2]}" "${WORKDIR}/stale-halt.out"
echo "release-live-test: halt, idempotent retry, recovery, and stale halt verified" >&2

delete_created_objects
for key in "${CREATED_KEYS[@]}"; do
  stat="$(r2_rest_object_stat "${key}")" || {
    CLEANED=0
    echo "error: cleanup verification transport failure for ${key}" >&2
    exit 1
  }
  [[ -z "${stat}" ]] || {
    CLEANED=0
    echo "error: cleanup left ${key}" >&2
    exit 1
  }
done

echo "release-live-test: exact-key cleanup verified" >&2

echo "release-live-test: PASS — immutable, pointer, halt, recovery, public, and cleanup paths" >&2
