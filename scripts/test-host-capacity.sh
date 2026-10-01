#!/usr/bin/env bash

test_host_cpu_count() {
  local count
  if command -v nproc >/dev/null 2>&1; then
    count="$(nproc)"
  else
    count="$(sysctl -n hw.ncpu 2>/dev/null || echo 4)"
  fi
  if [[ ! "${count}" =~ ^[1-9][0-9]*$ ]]; then
    count=4
  fi
  printf '%s\n' "${count}"
}

test_host_load_one() {
  local load=""
  if [[ -r /proc/loadavg ]]; then
    read -r load _ </proc/loadavg
  elif command -v sysctl >/dev/null 2>&1; then
    load="$(sysctl -n vm.loadavg 2>/dev/null | awk '{gsub(/[{}]/, ""); print $1}')"
  fi
  if [[ ! "${load}" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
    load=0
  fi
  printf '%s\n' "${load}"
}

test_host_timeout_scale() {
  local cpus="$1"
  local load="$2"
  if [[ -n "${PW_TEST_TIMEOUT_SCALE:-}" ]]; then
    if [[ ! "${PW_TEST_TIMEOUT_SCALE}" =~ ^[1-4]$ ]]; then
      echo "PW_TEST_TIMEOUT_SCALE must be an integer from 1 through 4" >&2
      return 2
    fi
    printf '%s\n' "${PW_TEST_TIMEOUT_SCALE}"
    return
  fi
  awk -v cpus="${cpus}" -v load="${load}" 'BEGIN {
    scale = int(load / cpus) + 1
    if (scale < 1) scale = 1
    if (scale > 4) scale = 4
    print scale
  }'
}

test_scale_integer() {
  local value="$1"
  local scale="$2"
  printf '%s\n' "$(( value * scale ))"
}

test_scale_go_duration() {
  local value="$1"
  local scale="$2"
  if [[ "${value}" =~ ^([0-9]+)(ns|us|µs|ms|s|m|h)$ ]]; then
    printf '%s%s\n' "$(( BASH_REMATCH[1] * scale ))" "${BASH_REMATCH[2]}"
    return
  fi
  # Composite durations pass through unchanged.
  printf '%s\n' "${value}"
}
