#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if ! python3 "$ROOT/scripts/test-execution.py" holding; then
  exec python3 "$ROOT/scripts/test-execution.py" run --name "fuzz-short" -- bash "$0" "$@"
fi
SOURCE_SNAPSHOT_SH="${ROOT}/scripts/test-source-snapshot.sh"
if ! bash "$SOURCE_SNAPSHOT_SH" holding; then
  exec bash "$SOURCE_SNAPSHOT_SH" run -- bash scripts/fuzz-short.sh "$@"
fi
CAPACITY_SH="${ROOT}/scripts/test-host-capacity.sh"
# shellcheck source=test-host-capacity.sh
source "${CAPACITY_SH}"
# shellcheck source=document-core-env.sh
source "${ROOT}/scripts/document-core-env.sh"

PKGS=("$@")
if [[ ${#PKGS[@]} -eq 0 ]]; then
  PKGS=("./...")
fi
FUZZTIME="${FUZZTIME:-20s}"
export GOCACHE="${GOCACHE:-$(go env GOCACHE)}"

HOST_CPUS="$(test_host_cpu_count)"
HOST_LOAD="$(test_host_load_one)"
TIMEOUT_SCALE="$(test_host_timeout_scale "${HOST_CPUS}" "${HOST_LOAD}")"
export PW_TEST_TIMEOUT_SCALE="${TIMEOUT_SCALE}"
FUZZ_WORKERS="${PW_FUZZ_WORKERS:?verification admission must set PW_FUZZ_WORKERS}"
echo "fuzz: host load ${HOST_LOAD} on ${HOST_CPUS} CPUs; workers=${FUZZ_WORKERS}, timeout scale=${TIMEOUT_SCALE}x" >&2
PACKAGE_TIMEOUT="$(test_scale_go_duration 10m "${TIMEOUT_SCALE}")"
cd "${ROOT}/lycaon"

TARGETS=()
set +e
LIST_OUTPUT="$(go test -p="${FUZZ_WORKERS}" -parallel="${FUZZ_WORKERS}" -list '^Fuzz' "${PKGS[@]}" 2>&1)"
LIST_RC=$?
set -e
if [[ "$LIST_RC" -ne 0 ]]; then
  printf '%s\n' "$LIST_OUTPUT" >&2
  exit "$LIST_RC"
fi
while IFS= read -r target; do
  TARGETS+=("${target}")
done < <(printf '%s\n' "$LIST_OUTPUT" | awk '
    /^Fuzz/ { fns[++n] = $0 }
    /^(ok|FAIL)/ {
      for (i = 1; i <= n; i++) print $2 " " fns[i]
      n = 0
    }
  ')

if [[ ${#TARGETS[@]} -eq 0 ]]; then
  echo "No fuzz targets in ${PKGS[*]}" >&2
  exit 0
fi

failed=0
for entry in "${TARGETS[@]}"; do
  pkg="${entry%% *}"
  fn="${entry##* }"
  echo "→ ${pkg} :: ${fn} (${FUZZTIME})" >&2
  if ! go test -p="${FUZZ_WORKERS}" -parallel="${FUZZ_WORKERS}" -timeout="${PACKAGE_TIMEOUT}" \
    -run='^$' -fuzz="^${fn}$" -fuzztime="${FUZZTIME}" "${pkg}"; then
    failed=$((failed + 1))
  fi
done

if [[ ${failed} -gt 0 ]]; then
  echo "fuzz: ${failed} target(s) failed" >&2
  exit 1
fi
