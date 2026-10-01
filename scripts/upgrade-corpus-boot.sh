#!/usr/bin/env bash
# Boot upgrade-corpus fixtures against the current sidecar.
# Usage:
#   ./task upgrade:corpus:boot -- [fixture-dir ...]
#   ./task upgrade:corpus:boot -- --self-test
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/config-dir.sh
source "${ROOT}/scripts/config-dir.sh"
# shellcheck source=scripts/upgrade-store-contract.sh
source "${ROOT}/scripts/upgrade-store-contract.sh"
# shellcheck source=scripts/artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
GO_DIR="${ROOT}/lycaon"
CORPUS_ROOT="${ROOT}/lycaon/testdata/upgrade-corpus"
SIDECAR="${PW_BUILD_DIR}/lycaon-dev"
SCHEMA_LOCK="${GO_DIR}/internal/db/schema_version_lock.json"

BOOT_LYCAON_DEV=1
BOOT_OVERLAY_DIR="$(lycaon_overlay_dir)"

SELF_TEST=0
FIXTURE_ARGS=()

usage() {
  cat >&2 <<'EOF'
Usage: upgrade-corpus-boot.sh [fixture-dir ...]
       upgrade-corpus-boot.sh --self-test

With no fixture args, tests every retained versioned release fixture.
EOF
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --self-test)
      SELF_TEST=1
      shift
      ;;
    -h|--help)
      usage
      ;;
    --)
      shift
      FIXTURE_ARGS+=("$@")
      break
      ;;
    *)
      FIXTURE_ARGS+=("$1")
      shift
      ;;
  esac
done

for need in curl jq python3 sqlite3; do
  if ! command -v "${need}" >/dev/null 2>&1; then
    echo "error: ${need} required" >&2
    exit 1
  fi
done
if [[ ! -x "${SIDECAR}" ]]; then
  echo "error: sidecar not executable: ${SIDECAR} — run ./task build:lycaon-dev" >&2
  exit 1
fi

binary_schema_version() {
  jq -r '.schema_version' "${SCHEMA_LOCK}"
}

select_fixtures() {
  python3 - "${ROOT}/scripts" "${CORPUS_ROOT}" <<'PYCODE'
import functools
from pathlib import Path
import sys

sys.path.insert(0, sys.argv[1])
from release_semver import compare, parse

root = Path(sys.argv[2])
versions = {}
if root.is_dir():
    for entry in root.iterdir():
        if not entry.is_dir():
            continue
        try:
            versions[entry.name] = parse(entry.name)
        except ValueError:
            continue

def newest_first(left, right):
    order = compare(versions[right], versions[left])
    return order or (left > right) - (left < right)

for name in sorted(versions, key=functools.cmp_to_key(newest_first)):
    print(name)
PYCODE
}

boot_one() {
  upgrade_install_child_cleanup
  local src="$1"
  local name
  name="$(basename "${src}")"
  if [[ ! -d "${src}" ]]; then
    echo "error: fixture missing: ${src}" >&2
    return 1
  fi
  if [[ ! -f "${src}/MANIFEST.json" ]]; then
    echo "error: fixture ${name}: MANIFEST.json missing" >&2
    return 1
  fi

  local manifest bin_schema
  manifest="$(cat "${src}/MANIFEST.json")"
  bin_schema="$(binary_schema_version)"

  local project_id session_id tool_id
  project_id="$(jq -r '.project_id' <<<"${manifest}")"
  session_id="$(jq -r '.session_id' <<<"${manifest}")"
  tool_id="$(jq -r '.tool_result_message_id' <<<"${manifest}")"

  local tmp config_dir project_abs port addr token pid log base
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/upgrade-corpus-boot.XXXXXX")"
  # Materialized roots stay outside the config directory.
  config_dir="${tmp}/config"
  project_abs="${tmp}/project-root"
  mkdir -p "${config_dir}"
  python3 "${ROOT}/scripts/upgrade-fixture-files.py" materialize "${src}" "${config_dir}"
  if [[ -d "${src}/extensions-cache" ]]; then
    cp -a "${src}/extensions-cache" "${config_dir}/extensions-cache"
  fi
  cp -a "${src}/project-root" "${project_abs}"
  # Fixture setup restores the shared overlay directory name.
  mv "${project_abs}/${OVERLAY_FIXTURE_DIR}" "${project_abs}/${BOOT_OVERLAY_DIR}"
  project_abs="$(cd "${project_abs}" && pwd -P)"
  sqlite3 "${config_dir}/store.db" \
    "UPDATE project_roots SET path = '${project_abs//\'/\'\'}' WHERE path = 'project-root' OR path LIKE 'project-root/%';"

  local baseline
  baseline="$(upgrade_store_baseline "${SIDECAR}" "${config_dir}/store.db")" || {
    rm -rf "${tmp}"
    return 1
  }

  port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
  addr="127.0.0.1:${port}"
  token="upgrade-corpus-boot-token"
  log="${tmp}/sidecar.log"

  export LYCAON_CONFIG_DIR="${config_dir}"
  export LYCAON_LLM_MOCK=1
  export LYCAON_DEV="${BOOT_LYCAON_DEV}"
  export LYCAON_ADDR="${addr}"
  export LYCAON_API_TOKEN="${token}"
  export LYCAON_LOG_LEVEL="${LYCAON_LOG_LEVEL:-warn}"
  printf '%s' "${token}" >"${config_dir}/api.token"
  chmod 600 "${config_dir}/api.token"

  (
    cd "${GO_DIR}"
    exec "${SIDECAR}" serve
  ) >"${log}" 2>&1 &
  pid=$!
  UPGRADE_CHILD_PID="${pid}"

  stop_sidecar() {
    upgrade_stop_owned_child || return 1
    pid=
  }
  finish() {
    local code=$1
    stop_sidecar || return 1
    rm -rf "${tmp}"
    return "${code}"
  }

  base="http://${addr}"
  local deadline body status got_schema
  deadline=$((SECONDS + 60))
  body=""
  while (( SECONDS < deadline )); do
    if body="$(curl -sf --max-time 2 "${base}/health" 2>/dev/null)"; then
      status="$(jq -r '.status // empty' <<<"${body}")"
      if [[ "${status}" == "ok" || "${status}" == "degraded" || "${status}" == "recovery" ]]; then
        break
      fi
    fi
    if ! kill -0 "${pid}" 2>/dev/null; then
      echo "fixture ${name}: sidecar exited before reporting its baseline outcome" >&2
      cat "${log}" >&2 || true
      finish 1
      return 1
    fi
    sleep 0.25
  done
  status="$(jq -r '.status // empty' <<<"${body}")"
  if ! upgrade_store_health_matches "${body}" "${baseline}"; then
    echo "fixture ${name}: health disagrees with the current baseline: ${body}" >&2
    cat "${log}" >&2 || true
    finish 1
    return 1
  fi
  got_schema="$(jq -r '.schema_version // empty' <<<"${body}")"
  if [[ "${got_schema}" != "${bin_schema}" ]]; then
    echo "fixture ${name}: schema_version want ${bin_schema} got ${got_schema} — re-seed" >&2
    finish 1
    return 1
  fi

  local http_code
  http_code="$(curl -s -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer ${token}" \
    "${base}/v1/projects/${project_id}")"
  if [[ "${http_code}" != "200" ]]; then
    echo "fixture ${name}: seeded project missing — re-seed with upgrade:corpus:seed" >&2
    finish 1
    return 1
  fi

  # Overlay accepted: project loads and reports its primary root under the temp tree.
  local proj_json root_path
  proj_json="$(curl -sf -H "Authorization: Bearer ${token}" "${base}/v1/projects/${project_id}")"
  root_path="$(jq -r '.roots[0].path // empty' <<<"${proj_json}")"
  if [[ -z "${root_path}" || ! -d "${root_path}/${BOOT_OVERLAY_DIR}" ]]; then
    echo "fixture ${name}: overlay refused — check ${BOOT_OVERLAY_DIR}/ contents" >&2
    finish 1
    return 1
  fi

  local msgs tool_hit
  msgs="$(curl -sf -H "Authorization: Bearer ${token}" \
    "${base}/v1/sessions/${session_id}/messages")"
  tool_hit="$(jq -r --arg id "${tool_id}" \
    '.messages[]? | select(.id==$id and .role=="tool") | .id' <<<"${msgs}" | head -n1)"
  if [[ -z "${tool_hit}" ]]; then
    echo "fixture ${name}: tool_result missing — seed recipe incomplete" >&2
    finish 1
    return 1
  fi

  python3 "${ROOT}/scripts/upgrade-corpus-semantics.py" verify --base "${base}" \
    --token-file "${config_dir}/api.token" --fixture "${src}" --config-dir "${config_dir}" || { finish 1; return 1; }
  stop_sidecar || return 1
  "${SIDECAR}" diagnostics upgrade-fixture-verify "${config_dir}" "${src}/MANIFEST.json" || { finish 1; return 1; }
  bash "${ROOT}/scripts/upgrade-archive-roundtrip.sh" "${SIDECAR}" "${src}" || { finish 1; return 1; }
  rm -rf "${tmp}"
  echo "fixture ${name}: ok" >&2
  return 0
}

run_self_test() {
  local self_root base_fix
  self_root="$(mktemp -d "${TMPDIR:-/tmp}/upgrade-corpus-self.XXXXXX")"
  trap 'rm -rf "${self_root}"' RETURN
  base_fix="${self_root}/current"
  bash "${ROOT}/scripts/upgrade-corpus-seed.sh" --out "${base_fix}" --sidecar "${SIDECAR}"

  local d1
  d1="$(mktemp -d "${TMPDIR:-/tmp}/upgrade-corpus-neg-project.XXXXXX")"
  cp -a "${base_fix}/." "${d1}/"
  jq '.project_id = "00000000-0000-0000-0000-000000000000"' "${d1}/MANIFEST.json" >"${d1}/MANIFEST.json.tmp"
  mv "${d1}/MANIFEST.json.tmp" "${d1}/MANIFEST.json"
  local err1=0
  local out1
  boot_one "${d1}" >"${self_root}/case-1.log" 2>&1 || err1=$?
  out1="$(cat "${self_root}/case-1.log")"
  rm -rf "${d1}"
  if [[ ${err1} -eq 0 ]] || ! grep -q 'seeded project missing' <<<"${out1}"; then
    echo "error: negative missing-project expected exit 1 with 'seeded project missing'; got ${err1}: ${out1}" >&2
    return 1
  fi
  echo "self-test: missing project — ok" >&2

  local d2
  d2="$(mktemp -d "${TMPDIR:-/tmp}/upgrade-corpus-neg-tool.XXXXXX")"
  cp -a "${base_fix}/." "${d2}/"
  jq '.tool_result_message_id = "00000000-0000-0000-0000-000000000000"' "${d2}/MANIFEST.json" >"${d2}/MANIFEST.json.tmp"
  mv "${d2}/MANIFEST.json.tmp" "${d2}/MANIFEST.json"
  local err2=0
  local out2
  boot_one "${d2}" >"${self_root}/case-2.log" 2>&1 || err2=$?
  out2="$(cat "${self_root}/case-2.log")"
  rm -rf "${d2}"
  if [[ ${err2} -eq 0 ]] || ! grep -q 'tool_result missing' <<<"${out2}"; then
    echo "error: negative missing-tool_result expected exit 1 with 'tool_result missing'; got ${err2}: ${out2}" >&2
    return 1
  fi
  echo "self-test: missing tool_result — ok" >&2

  local d3
  d3="$(mktemp -d "${TMPDIR:-/tmp}/upgrade-corpus-mismatch.XXXXXX")"
  cp -a "${base_fix}/." "${d3}/"
  sqlite3 "${d3}/store.db" 'CREATE INDEX rehearsal_extra_index ON projects(id);'
  local out3 err3=0
  boot_one "${d3}" >"${self_root}/case-3.log" 2>&1 || err3=$?
  out3="$(cat "${self_root}/case-3.log")"
  rm -rf "${d3}"
  if [[ ${err3} -eq 0 ]]; then
    echo "error: changed shape must fail the upgrade gate; got ${err3}: ${out3}" >&2
    return 1
  fi
  echo "self-test: equal-marker shape mismatch — ok" >&2

  local before after
  before="$(shasum -a 256 "${base_fix}/store.db" | awk '{print $1}')"
  boot_one "${base_fix}" >/dev/null
  after="$(shasum -a 256 "${base_fix}/store.db" | awk '{print $1}')"
  if [[ "${before}" != "${after}" ]]; then
    echo "error: boot mutated the source fixture" >&2
    return 1
  fi
  echo "self-test: immutable corpus — ok" >&2

  # Select every retained release, including skipped-release sources.
  local sel_root
  sel_root="$(mktemp -d "${TMPDIR:-/tmp}/upgrade-corpus-select.XXXXXX")"
  mkdir -p "${sel_root}/scratch" "${sel_root}/0.1.0" "${sel_root}/0.3.0-rc.2" \
    "${sel_root}/0.4.0-rc.1" "${sel_root}/0.4.0-rc.10" "${sel_root}/0.4.0-rc.01" "${sel_root}/0.4.0"
  # Point CORPUS_ROOT at the temp tree for selection only.
  local old_corpus="${CORPUS_ROOT}"
  CORPUS_ROOT="${sel_root}"
  local selected expected
  selected="$(select_fixtures)"
  CORPUS_ROOT="${old_corpus}"
  rm -rf "${sel_root}"
  expected=$'0.4.0\n0.4.0-rc.10\n0.4.0-rc.1\n0.3.0-rc.2\n0.1.0'
  if [[ "${selected}" != "${expected}" ]]; then
    echo "error: release fixture selection or order differs: ${selected}" >&2
    return 1
  fi
  echo "self-test: selection — ok" >&2
  echo "upgrade-corpus:boot --self-test passed" >&2
}

if [[ "${SELF_TEST}" -eq 1 ]]; then
  run_self_test
  exit 0
fi

TARGETS=()
if (( ${#FIXTURE_ARGS[@]} > 0 )); then
  for arg in "${FIXTURE_ARGS[@]}"; do
    if [[ "${arg}" = /* ]]; then
      TARGETS+=("${arg}")
    elif [[ -d "${arg}" ]]; then
      TARGETS+=("$(cd "${arg}" && pwd)")
    elif [[ -d "${CORPUS_ROOT}/${arg}" ]]; then
      TARGETS+=("${CORPUS_ROOT}/${arg}")
    else
      echo "error: fixture not found: ${arg}" >&2
      exit 1
    fi
  done
else
  while IFS= read -r name; do
    [[ -n "${name}" ]] || continue
    TARGETS+=("${CORPUS_ROOT}/${name}")
  done < <(select_fixtures)
fi

if (( ${#TARGETS[@]} == 0 )); then
  echo "error: upgrade-corpus:boot found no fixtures under ${CORPUS_ROOT}" >&2
  exit 1
fi

fail=0
booted=0
for t in "${TARGETS[@]}"; do
  status=0
  boot_one "${t}" || status=$?
  case "${status}" in
    0) booted=$((booted + 1)) ;;
    *) fail=1 ;;
  esac
done
if (( booted == 0 )); then
  echo "error: upgrade-corpus:boot exercised no fixture" >&2
  fail=1
else
  echo "upgrade-corpus:boot — ${booted} baseline outcomes verified" >&2
fi
exit "${fail}"
