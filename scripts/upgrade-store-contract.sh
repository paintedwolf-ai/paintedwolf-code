#!/usr/bin/env bash

upgrade_store_baseline() {
  local engine="$1" store="$2"
  "${engine}" diagnostics store-baseline "${store}"
}

upgrade_store_health_matches() {
  local health="$1" baseline="$2"
  jq -en --argjson health "${health}" --argjson baseline "${baseline}" '
    $health.schema_version == $baseline.schema_version and
    ($health.status == "ok" or $health.status == "degraded")' >/dev/null
}

upgrade_store_fingerprint() {
  python3 - "$1" <<'PYCODE'
import hashlib
import json
import pathlib
import sys

store = pathlib.Path(sys.argv[1])
result = {}
for suffix in ("", "-wal", "-shm", "-journal"):
    path = pathlib.Path(str(store) + suffix)
    if path.exists():
        with path.open("rb") as source:
            digest = hashlib.sha256()
            for block in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(block)
            result[suffix] = digest.hexdigest()
print(json.dumps(result, sort_keys=True))
PYCODE
}

upgrade_release_fixture() {
  local corpus="$1" tag="$2" fixture version
  fixture="${corpus}/${tag#v}"
  if [[ ! -f "${fixture}/MANIFEST.json" || ! -f "${fixture}/store.db" ]]; then
    echo "error: exact corpus fixture missing for ${tag}: ${fixture}" >&2
    return 1
  fi
  version="$(jq -er '.app_version | strings' "${fixture}/MANIFEST.json")" || return 1
  if [[ "${version}" != "${tag#v}" ]]; then
    echo "error: corpus app_version=${version}; expected ${tag#v}" >&2
    return 1
  fi
  printf '%s' "${fixture}"
}

upgrade_store_contract_self_test() {
  local tmp="$1" healthy mismatch before after
  healthy='{"compatible":true,"schema_version":1,"store_schema_version":1}'
  mismatch='{"compatible":false,"schema_version":1,"store_schema_version":1,"recovery_reason":"schema_mismatch"}'
  upgrade_store_health_matches '{"status":"ok","schema_version":1}' "${healthy}" || return 1
  upgrade_store_health_matches '{"status":"ok","schema_version":1}' "${mismatch}" || return 1
  if upgrade_store_health_matches '{"status":"recovery","schema_version":1,"store_schema_version":1,"recovery_reason":"schema_mismatch"}' "${mismatch}" ||
     upgrade_store_health_matches '{"status":"recovery","schema_version":1,"store_schema_version":1,"recovery_reason":"integrity_failed"}' "${mismatch}" ||
     upgrade_store_health_matches '{"status":"recovery","schema_version":1,"store_schema_version":1,"recovery_reason":"schema_mismatch"}' "${healthy}"; then
    echo "error: store health contract accepted the wrong baseline outcome" >&2
    return 1
  fi
  sqlite3 "${tmp}/contract.db" 'CREATE TABLE t(x INTEGER); INSERT INTO t VALUES(1);'
  before="$(upgrade_store_fingerprint "${tmp}/contract.db")"
  sqlite3 "${tmp}/contract.db" 'UPDATE t SET x=2;'
  after="$(upgrade_store_fingerprint "${tmp}/contract.db")"
  if [[ "${before}" == "${after}" ]]; then
    echo "error: byte preservation check missed a same-row-count mutation" >&2
    return 1
  fi
  mkdir -p "${tmp}/corpus/0.1.0"
  if upgrade_release_fixture "${tmp}/corpus" v0.1.0 >/dev/null 2>&1; then
    echo "error: release fixture selection accepted missing exact provenance" >&2
    return 1
  fi
  cp "${tmp}/contract.db" "${tmp}/corpus/0.1.0/store.db"
  printf '%s' '{"app_version":"0.0.9"}' >"${tmp}/corpus/0.1.0/MANIFEST.json"
  if upgrade_release_fixture "${tmp}/corpus" v0.1.0 >/dev/null 2>&1; then
    echo "error: release fixture selection accepted the wrong app version" >&2
    return 1
  fi
  printf '%s' '{"app_version":"0.1.0"}' >"${tmp}/corpus/0.1.0/MANIFEST.json"
  upgrade_release_fixture "${tmp}/corpus" v0.1.0 >/dev/null || return 1
  echo "self-test: baseline outcomes, immutable bytes, and exact provenance — ok" >&2
}

upgrade_install_child_cleanup() {
  UPGRADE_CHILD_PID=""
  trap 'upgrade_stop_owned_child' EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
}

upgrade_stop_owned_child() {
  local pid="${UPGRADE_CHILD_PID:-}" deadline
  [[ -n "${pid}" ]] || return 0
  if kill -0 "${pid}" 2>/dev/null; then
    kill -TERM "${pid}" 2>/dev/null || true
    deadline=$((SECONDS + 5))
    while kill -0 "${pid}" 2>/dev/null; do
      if (( SECONDS >= deadline )); then
        kill -KILL "${pid}" 2>/dev/null || true
        deadline=$((SECONDS + 2))
        while kill -0 "${pid}" 2>/dev/null; do
          if (( SECONDS >= deadline )); then
            echo "error: captured engine ${pid} did not exit after KILL" >&2
            return 1
          fi
          sleep 0.1
        done
        break
      fi
      sleep 0.1
    done
  fi
  wait "${pid}" 2>/dev/null || true
  UPGRADE_CHILD_PID=""
}

upgrade_child_cleanup_self_test() {
  local tmp="$1" helper="$2" child started owner status=0
  cat >"${tmp}/stubborn.py" <<'PYCODE'
import pathlib
import signal
import sys

signal.signal(signal.SIGTERM, signal.SIG_IGN)
pathlib.Path(sys.argv[1]).touch()
while True:
    signal.pause()
PYCODE
  upgrade_install_child_cleanup
  python3 "${tmp}/stubborn.py" "${tmp}/stubborn-ready" &
  UPGRADE_CHILD_PID=$!
  child="${UPGRADE_CHILD_PID}"
  started=$SECONDS
  while [[ ! -f "${tmp}/stubborn-ready" ]]; do
    if (( SECONDS - started >= 5 )); then
      echo "error: stubborn-child fixture did not start" >&2
      return 1
    fi
    sleep 0.1
  done
  upgrade_stop_owned_child || return 1
  if kill -0 "${child}" 2>/dev/null || (( SECONDS - started > 9 )); then
    echo "error: captured child termination exceeded its bound" >&2
    return 1
  fi
  bash -c '
    source "$1"
    upgrade_install_child_cleanup
    python3 -c "import signal; signal.pause()" &
    UPGRADE_CHILD_PID=$!
    printf "%s" "${UPGRADE_CHILD_PID}" >"$2"
    wait "${UPGRADE_CHILD_PID}"
  ' _ "${helper}" "${tmp}/signal-child" &
  owner=$!
  UPGRADE_CHILD_PID="${owner}"
  started=$SECONDS
  while [[ ! -s "${tmp}/signal-child" ]]; do
    if (( SECONDS - started >= 5 )); then
      echo "error: signal-cleanup fixture did not start" >&2
      return 1
    fi
    sleep 0.1
  done
  child="$(cat "${tmp}/signal-child")"
  kill -TERM "${owner}"
  wait "${owner}" || status=$?
  UPGRADE_CHILD_PID=""
  if [[ "${status}" != 143 ]] || kill -0 "${child}" 2>/dev/null; then
    echo "error: signal cleanup left the captured child running" >&2
    return 1
  fi
  echo "self-test: captured child signal cleanup and bounded termination — ok" >&2
}
