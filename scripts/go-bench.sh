#!/usr/bin/env bash
# Run every Go benchmark repeatedly and emit a machine-readable comparison report.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if ! python3 "$ROOT/scripts/test-execution.py" holding; then
  exec python3 "$ROOT/scripts/test-execution.py" run --name "perf:bench" -- bash "$0" "$@"
fi
if ! bash "${ROOT}/scripts/repo-snapshot-lock.sh" holding; then
  exec bash "${ROOT}/scripts/repo-snapshot-lock.sh" read -- bash "$0" "$@"
fi

# shellcheck source=artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
COUNT="${PERF_BENCH_COUNT:-6}"
OUTPUT="${PERF_BENCH_OUTPUT:-${PW_ARTIFACT_ROOT}/perf/go-bench.json}"
BASELINE="${PERF_BENCH_BASELINE:-}"
PACKAGES=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --count) COUNT="$2"; shift 2 ;;
    --output) OUTPUT="$2"; shift 2 ;;
    --baseline) BASELINE="$2"; shift 2 ;;
    --) shift; PACKAGES+=("$@"); break ;;
    *) PACKAGES+=("$1"); shift ;;
  esac
done

if [[ ${#PACKAGES[@]} -eq 0 ]]; then
  while IFS= read -r file; do
    package="./$(dirname "${file}")"
    seen=false
    for existing in "${PACKAGES[@]:-}"; do
      if [[ "${existing}" == "${package}" ]]; then seen=true; break; fi
    done
    if [[ "${seen}" == false ]]; then PACKAGES+=("${package}"); fi
  done < <(cd "${ROOT}/lycaon" && rg -l '^func Benchmark' --glob '*_test.go' | sort)
fi

mkdir -p "$(dirname "${OUTPUT}")"
RAW="$(mktemp "${TMPDIR:-/tmp}/painted-wolf-bench.XXXXXX")"
trap 'rm -f "${RAW}"' EXIT

cd "${ROOT}/lycaon"
LYCAON_LLM_MOCK=1 go test -run '^$' -bench . -benchmem -count "${COUNT}" -timeout 45m "${PACKAGES[@]}" | tee "${RAW}"

ARGS=(--input "${RAW}" --output "${OUTPUT}")
if [[ -n "${BASELINE}" ]]; then ARGS+=(--baseline "${BASELINE}"); fi
python3 "${ROOT}/scripts/go-bench-report.py" "${ARGS[@]}"
