#!/usr/bin/env bash
# Verify preserved, usable history when upgrading a released installation.
# DOWNLOAD_BASE_URL supplies the public channel pointers.
#
# Usage:
#   ./task upgrade:rehearse
#   ./task upgrade:rehearse -- --release v0.2.0     # pin the prior release
#   ./task upgrade:rehearse -- --self-test          # exercise the guards
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/upgrade-store-contract.sh
source "${ROOT}/scripts/upgrade-store-contract.sh"
# shellcheck source=scripts/artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
# Sourced here, not in rehearse(): its RETURN trap fires when a sourced file ends.
# shellcheck source=scripts/config-dir.sh
source "${ROOT}/scripts/config-dir.sh"
GO_DIR="${ROOT}/lycaon"
CORPUS_ROOT="${ROOT}/lycaon/testdata/upgrade-corpus"
HEAD_SIDECAR="${PW_BUILD_DIR}/lycaon-dev"
SCHEMA_LOCK="${GO_DIR}/internal/db/schema_version_lock.json"

PIN_RELEASE=""
SELF_TEST=0
SIGNING_GENERATION="${SIGNING_GENERATION:-$(jq -er '.signing_generation' "${ROOT}/packaging/update-keys.json")}"

usage() {
  cat >&2 <<'EOF'
Usage: upgrade-rehearse.sh [--release vX.Y.Z] [--generation N] [--self-test]

Exits 0 with a skip notice when no prior release has been published — that is
the expected state before the first publication. Both channels are checked.
EOF
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --release) PIN_RELEASE="${2:-}"; shift 2 ;;
    --generation) SIGNING_GENERATION="${2:-}"; shift 2 ;;
    --self-test) SELF_TEST=1; shift ;;
    -h|--help) usage ;;
    *) echo "error: unknown argument: $1" >&2; usage ;;
  esac
done
[[ "${SIGNING_GENERATION}" =~ ^[1-9][0-9]*$ ]] || { echo "error: signing generation must be positive" >&2; exit 2; }

for need in curl jq python3 sqlite3 tar; do
  command -v "${need}" >/dev/null 2>&1 || { echo "error: ${need} required" >&2; exit 1; }
done

head_schema_version() { jq -r '.schema_version' "${SCHEMA_LOCK}"; }

# Validate a public manifest through the same contract as publication.
parse_prior_release() {
  bash "${ROOT}/scripts/release-validate-updater-manifest.sh" --file "$1" || return 1
  jq -r '.version' "$1"
}

resolve_prior_release() {
  if [[ -n "${PIN_RELEASE}" ]]; then
    python3 "${ROOT}/scripts/semver-compare.py" eq "${PIN_RELEASE#v}" "${PIN_RELEASE#v}" >/dev/null
    printf 'v%s\n' "${PIN_RELEASE#v}"
    return
  fi
  python3 "${ROOT}/scripts/release_control.py" prior \
    --base-url "${DOWNLOAD_BASE_URL:?configure DOWNLOAD_BASE_URL for upgrade rehearsal}" \
    --candidate "$(tr -d '[:space:]' < "${ROOT}/VERSION")" --generation "${SIGNING_GENERATION}"
}

# A halt records the withdrawal as an immutable public marker.
prior_withdrawn() {
  local version="$1" base="${DOWNLOAD_BASE_URL:-}"
  [[ -n "${base}" ]] || return 1
  [[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 30 -A painted-wolf-release/1 \
    -H 'Cache-Control: no-cache' "${base%/}/release-metadata/${version}/withdrawn.json")" == 200 ]]
}

# Extract the prior release's engine binary into $1. Echoes the binary path.
fetch_prior_engine() {
  local tag="$1" dest="$2"
  local base="${DOWNLOAD_BASE_URL:-}"
  if [[ -z "${base}" ]]; then
    echo "error: DOWNLOAD_BASE_URL unset — cannot fetch ${tag} artifacts" >&2
    return 1
  fi
  local url="${base%/}/releases/${tag}/painted-wolf-code_${tag}_darwin-aarch64.app.tar.gz"
  local tarball="${dest}/prior.app.tar.gz"
  if ! curl -sf --location --retry 5 --retry-delay 3 --max-time 300 "${url}" -o "${tarball}"; then
    echo "error: could not download ${url}" >&2
    return 1
  fi
  tar -xzf "${tarball}" -C "${dest}"
  local engine
  engine="$(find "${dest}" -type f -path '*/Contents/MacOS/pw' -print -quit)"
  if [[ -z "${engine}" ]]; then
    echo "error: ${tag} bundle has no Contents/MacOS/pw" >&2
    return 1
  fi
  chmod +x "${engine}"
  printf '%s' "${engine}"
}

# The parent shell owns the engine PID while health is written to $4.
boot_engine() {
  local engine="$1" config_dir="$2" log="$3" output="$4" fixture="${5:-}" archive="${6:-}"
  local port addr token pid base body deadline status
  upgrade_install_child_cleanup
  port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
  addr="127.0.0.1:${port}"
  token="upgrade-rehearse-token"
  printf '%s' "${token}" >"${config_dir}/api.token"
  chmod 600 "${config_dir}/api.token"

  (
    cd "${GO_DIR}"
    LYCAON_CONFIG_DIR="${config_dir}" \
    LYCAON_LLM_MOCK=1 \
    LYCAON_DEV=1 \
    LYCAON_ADDR="${addr}" \
    LYCAON_API_TOKEN="${token}" \
    LYCAON_LOG_LEVEL="${LYCAON_LOG_LEVEL:-warn}" \
    exec "${engine}" serve
  ) >"${log}" 2>&1 &
  pid=$!
  UPGRADE_CHILD_PID="${pid}"

  base="http://${addr}"
  body=""
  deadline=$((SECONDS + 60))
  while (( SECONDS < deadline )); do
    if body="$(curl -sf --max-time 2 "${base}/health" 2>/dev/null)"; then
      status="$(jq -r '.status // empty' <<<"${body}")"
      [[ "${status}" == "ok" || "${status}" == "degraded" || "${status}" == "recovery" ]] && break
    fi
    if ! kill -0 "${pid}" 2>/dev/null; then
      echo "engine exited during boot; log follows:" >&2
      cat "${log}" >&2 || true
      return 1
    fi
    sleep 0.25
  done

  status="$(jq -r '.status // empty' <<<"${body}")"
  if [[ "${status}" != "ok" && "${status}" != "degraded" ]]; then
    echo "health status=${status:-empty}" >&2
    cat "${log}" >&2 || true
    upgrade_stop_owned_child || return 1
    return 1
  fi

  if [[ -n "${fixture}" ]]; then
    python3 "${ROOT}/scripts/upgrade-corpus-semantics.py" verify --base "${base}" \
      --token-file "${config_dir}/api.token" --fixture "${fixture}" --config-dir "${config_dir}" || return 1
  fi
  if [[ -n "${archive}" ]]; then
    curl -fsS --max-time 120 -X POST -H "Authorization: Bearer ${token}" \
      -H 'Content-Type: application/zip' --data-binary "@${archive}" \
      "${base}/v1/backup/restore" >"${output}.restore.json" || return 1
    jq -e '.restart_required == true' "${output}.restore.json" >/dev/null || return 1
  fi
  upgrade_stop_owned_child || return 1
  if [[ -n "${fixture}" ]]; then
    "${engine}" diagnostics upgrade-fixture-verify "${config_dir}" "${fixture}/MANIFEST.json" || return 1
  fi
  printf '%s' "${body}" >"${output}"
}

# Per-table row counts for the durable tables, as "table=count" lines.
row_counts() {
  local db="$1"
  sqlite3 "${db}" \
    "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name;" |
  while IFS= read -r table; do
    [[ -n "${table}" ]] || continue
    printf '%s=%s\n' "${table}" "$(sqlite3 "${db}" "SELECT count(*) FROM \"${table}\";")"
  done
}

rehearse() {
  local tag="$1" fixture="$2"
  local tmp
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/upgrade-rehearse.XXXXXX")"
  trap 'upgrade_stop_owned_child && rm -rf "${tmp}"' RETURN

  local config_dir="${tmp}/config" project_abs="${tmp}/project-root"
  mkdir -p "${config_dir}"
  python3 "${ROOT}/scripts/upgrade-fixture-files.py" materialize "${fixture}" "${config_dir}"
  cp -a "${fixture}/project-root" "${project_abs}"
  mv "${project_abs}/${OVERLAY_FIXTURE_DIR}" "${project_abs}/$(lycaon_overlay_dir)"
  project_abs="$(cd "${project_abs}" && pwd -P)"
  sqlite3 "${config_dir}/store.db" \
    "UPDATE project_roots SET path = '${project_abs//\'/\'\'}' WHERE path = 'project-root' OR path LIKE 'project-root/%';"

  local prior_engine
  prior_engine="$(fetch_prior_engine "${tag}" "${tmp}")" || return 1

  echo "→ booting prior release ${tag}" >&2
  local prior_health prior_schema=""
  if boot_engine "${prior_engine}" "${config_dir}" "${tmp}/prior.log" "${tmp}/prior-health.json" "${fixture}"; then
    prior_health="$(cat "${tmp}/prior-health.json")"
    if ! jq -e --arg version "${tag#v}" '(.status == "ok" or .status == "degraded") and .version == $version' <<<"${prior_health}" >/dev/null; then
      echo "error: ${tag} did not serve its exact-version fixture: ${prior_health}" >&2
      return 1
    fi
    prior_schema="$(jq -r '.schema_version // empty' <<<"${prior_health}")"
    echo "  prior schema_version=${prior_schema}" >&2
  elif prior_withdrawn "${tag#v}"; then
    # A withdrawn release may be withdrawn because it cannot boot; its
    # installations still upgrade, so HEAD must still upgrade its fixture.
    echo "warning: withdrawn prior release ${tag} could not serve its fixture; rehearsing HEAD's upgrade of it" >&2
  else
    echo "error: prior release ${tag} could not serve its own corpus fixture" >&2
    return 1
  fi

  local baseline
  baseline="$(upgrade_store_baseline "${HEAD_SIDECAR}" "${config_dir}/store.db")" || return 1

  echo "→ booting HEAD against the same store" >&2
  local head_health
  if ! boot_engine "${HEAD_SIDECAR}" "${config_dir}" "${tmp}/head.log" "${tmp}/head-health.json" "${fixture}"; then
    echo "error: HEAD did not report store health for ${tag}" >&2
    return 1
  fi

  head_health="$(cat "${tmp}/head-health.json")"
  if ! upgrade_store_health_matches "${head_health}" "${baseline}"; then
    echo "error: HEAD health disagrees with its baseline check: ${head_health}" >&2
    return 1
  fi
  local head_schema want_schema
  head_schema="$(jq -r '.schema_version // empty' <<<"${head_health}")"
  want_schema="$(head_schema_version)"
  if [[ "${head_schema}" != "${want_schema}" ]]; then
    echo "error: after upgrade schema_version=${head_schema} want ${want_schema}" >&2
    return 1
  fi

  local stored_revision
  stored_revision="$(sqlite3 "${config_dir}/store.db" 'PRAGMA user_version;')"
  [[ "${stored_revision}" == "${want_schema}" ]] || {
    echo "error: upgraded store revision ${stored_revision}; expected ${want_schema}" >&2
    return 1
  }
  boot_engine "${HEAD_SIDECAR}" "${config_dir}" "${tmp}/restart.log" "${tmp}/restart-health.json" "${fixture}" || return 1

  local restored_dir="${tmp}/restored-config"
  mkdir -p "${restored_dir}"
  boot_engine "${HEAD_SIDECAR}" "${restored_dir}" "${tmp}/restore.log" "${tmp}/restore-health.json" "" "${fixture}/backup.zip" || return 1
  # Startup installs the staged archive before fixture paths can be relocated.
  boot_engine "${HEAD_SIDECAR}" "${restored_dir}" "${tmp}/install.log" "${tmp}/install-health.json" "" || return 1
  local restored_project="${tmp}/restored-project" project_id
  cp -a "${fixture}/project-root" "${restored_project}"
  mv "${restored_project}/${OVERLAY_FIXTURE_DIR}" "${restored_project}/$(lycaon_overlay_dir)"
  restored_project="$(cd "${restored_project}" && pwd -P)"
  project_id="$(jq -r '.project_id' "${fixture}/MANIFEST.json")"
  sqlite3 "${restored_dir}/store.db" \
    "UPDATE project_roots SET path = '${restored_project//\'/\'\'}' WHERE project_id = '${project_id//\'/\'\'}';"
  boot_engine "${HEAD_SIDECAR}" "${restored_dir}" "${tmp}/restored.log" "${tmp}/restored-health.json" "${fixture}" || return 1

  echo "upgrade:rehearse — ${tag} (schema ${prior_schema}) → HEAD (schema ${head_schema}): ok" >&2
  return 0
}

run_self_test() {
  # Self-test row counts, pointer validation, and required configuration.
  local tmp
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/upgrade-rehearse-self.XXXXXX")"
  trap 'upgrade_stop_owned_child && rm -rf "${tmp}"' RETURN

  upgrade_store_contract_self_test "${tmp}" || return 1
  upgrade_child_cleanup_self_test "${tmp}" "${ROOT}/scripts/upgrade-store-contract.sh" || return 1

  sqlite3 "${tmp}/a.db" 'CREATE TABLE t (x INTEGER); INSERT INTO t VALUES (1),(2);'
  local counts
  counts="$(row_counts "${tmp}/a.db")"
  if ! grep -qx 't=2' <<<"${counts}"; then
    echo "error: row_counts want t=2 got: ${counts}" >&2
    return 1
  fi
  sqlite3 "${tmp}/a.db" 'DELETE FROM t WHERE x = 2;'
  counts="$(row_counts "${tmp}/a.db")"
  if ! grep -qx 't=1' <<<"${counts}"; then
    echo "error: row_counts must observe deletion; got: ${counts}" >&2
    return 1
  fi
  echo "self-test: row_counts — ok" >&2

  local base="https://rehearse.invalid"
  jq -n \
    --arg base "${base}" \
    --argjson update_keys "$(PYTHONPATH="${ROOT}/scripts" python3 -c 'from update_keys import load_registry,release_binding; import json; r=load_registry(); r["signing_generation"]=r["embedded_generation"]=len(r["generations"]); print(json.dumps(release_binding(r,"0.0.9")))')" \
    --slurpfile catalog "${ROOT}/packaging/release-platforms.json" \
    '{version: "0.0.9", notes: "self-test", pub_date: "2026-01-01T00:00:00Z", update_keys: $update_keys,
      platforms: (reduce ($catalog[0].platforms[] | select(.publication == "public")) as $platform ({};
        .[$platform.updater_key] = {
          signature: "self-test-signature",
          url: ($base + "/releases/v0.0.9/painted-wolf-code_v0.0.9_" +
            $platform.updater_key + "." + $platform.updater_extension)
        }))}' \
    > "${tmp}/pointer.json"
  local version
  version="$(DOWNLOAD_BASE_URL="${base}" parse_prior_release "${tmp}/pointer.json" 2>/dev/null)" || {
    echo "error: parse_prior_release must accept a contract-valid pointer" >&2
    return 1
  }
  if [[ "${version}" != "0.0.9" ]]; then
    echo "error: parse_prior_release want 0.0.9 got ${version}" >&2
    return 1
  fi
  jq '.version = "v0.0.9"' "${tmp}/pointer.json" > "${tmp}/pointer-bad.json"
  if DOWNLOAD_BASE_URL="${base}" parse_prior_release "${tmp}/pointer-bad.json" >/dev/null 2>&1; then
    echo "error: parse_prior_release must reject a manifest outside the contract" >&2
    return 1
  fi
  echo "self-test: pointer manifest parse and rejection — ok" >&2

  local out err=0
  out="$(DOWNLOAD_BASE_URL= fetch_prior_engine v0.0.0 "${tmp}" 2>&1)" || err=$?
  if (( err == 0 )) || ! grep -q 'DOWNLOAD_BASE_URL unset' <<<"${out}"; then
    echo "error: missing DOWNLOAD_BASE_URL must fail loudly; got ${err}: ${out}" >&2
    return 1
  fi
  echo "self-test: missing download base — ok" >&2
  echo "upgrade:rehearse --self-test passed" >&2
}

if (( SELF_TEST == 1 )); then
  run_self_test
  exit 0
fi

if [[ ! -x "${HEAD_SIDECAR}" ]]; then
  echo "error: HEAD sidecar missing: ${HEAD_SIDECAR} — run ./task build:lycaon-dev" >&2
  exit 1
fi

TAG="$(resolve_prior_release)"
if [[ -z "${TAG}" ]]; then
  echo "upgrade:rehearse — skipped: no published release to upgrade from." >&2
  echo "  Both public feeds have no older release; candidate corpus verification still applies." >&2
  exit 0
fi

while IFS= read -r prior_tag; do
  FIXTURE="$(upgrade_release_fixture "${CORPUS_ROOT}" "${prior_tag}")" || exit 1
  rehearse "${prior_tag}" "${FIXTURE}"
done <<< "${TAG}"
