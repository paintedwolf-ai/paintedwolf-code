#!/usr/bin/env bash

# Short paths keep Unix sockets within platform limits.
TEST_RUN_ROOT="/tmp/paintedwolf-test-runs-${UID}"
TEST_RUN_LEASE_HELPER="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/test-run-lease.py"

test_run_prune_abandoned() {
  python3 "${TEST_RUN_LEASE_HELPER}" prune "${TEST_RUN_ROOT}"
}

test_run_create_isolation() {
  local kind="$1"

  mkdir -p "${TEST_RUN_ROOT}"
  chmod 700 "${TEST_RUN_ROOT}"
  test_run_prune_abandoned
  # Creation and collection share a lock until the new directory is leased.
  exec 9>"${TEST_RUN_ROOT}/.collection.lock"
  python3 "${TEST_RUN_LEASE_HELPER}" lock 9
  TEST_RUN_DIR="$(mktemp -d "${TEST_RUN_ROOT}/${kind}.lease.XXXXXX")"
  exec 8>"${TEST_RUN_DIR}/.lease"
  python3 "${TEST_RUN_LEASE_HELPER}" lock 8
  exec 9>&-
  mkdir -p \
    "${TEST_RUN_DIR}/home" \
    "${TEST_RUN_DIR}/tmp" \
    "${TEST_RUN_DIR}/xdg-cache" \
    "${TEST_RUN_DIR}/xdg-config" \
    "${TEST_RUN_DIR}/xdg-data"
}

test_run_remove_isolation() {
  local run_dir="$1"
  case "${run_dir}" in
    "${TEST_RUN_ROOT}"/*) ;;
    *) return 1 ;;
  esac
  exec 8>&-
  python3 "${TEST_RUN_LEASE_HELPER}" remove "${TEST_RUN_ROOT}" "${run_dir}"
}

test_run_export_isolation() {
  local run_dir="$1"

  # Browser tests reuse the pinned browser already provisioned under the real home.
  export PW_TEST_HOST_HOME="${PW_TEST_HOST_HOME:-${HOME}}"
  export HOME="${run_dir}/home"
  export TMPDIR="${run_dir}/tmp"
  export XDG_CACHE_HOME="${run_dir}/xdg-cache"
  export XDG_CONFIG_HOME="${run_dir}/xdg-config"
  export XDG_DATA_HOME="${run_dir}/xdg-data"
  unset LYCAON_CONFIG_DIR
}
