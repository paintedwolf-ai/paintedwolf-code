#!/usr/bin/env bash
# Reusable Paired Performance Runner for Portable Python (PY10 baseline & PY11/PY12/PY13 A/B comparisons).
#
# Modes:
#   --mode baseline:
#     Stages (or uses --engine-root) the frozen product pair with PW_PORTABLE_PYTHON_IMAGE_DIR and
#     PW_PORTABLE_PYTHON_ENGINE_DIR UNSET, enforces 30s >= 90% CPU idle pre-series gate and during-series
#     host monitoring, and runs 2 workloads (pass_noop, frozen_workbook) x 2 modes (cold x5, warm x5).
#
#   --mode compare --baseline-dir <engine-root-A> --candidate-dir <engine-root-B>:
#     Runs alternating A/B/A/B/... pairs on the same host with identical idle-host gates,
#     checking frozen_workbook <= 5.0s and flagging pass_noop regressions > 10% AND > 50 ms.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: run-paired.sh --mode <baseline|compare> --repo-root <dir> --out-dir <dir>
       [--engine-root <dir>] [--baseline-dir <dir>] [--candidate-dir <dir>]
       [--product-rev <sha>] [--image-archive <tar.gz>] [--engine-archive <tar.gz>]
       [--samples <N>] [--min-idle <pct>] [--precheck-seconds <S>]
       [--max-attempts <K>] [--retry-wait <S>] [--stage-now]
EOF
  exit 2
}

MODE=""
REPO_ROOT=""
OUT_DIR=""
ENGINE_ROOT=""
BASELINE_DIR=""
CANDIDATE_DIR=""
PRODUCT_REV=""
IMAGE_ARCHIVE=""
ENGINE_ARCHIVE=""
SAMPLES=5
MIN_IDLE=90
PRECHECK_SECONDS=30
MAX_ATTEMPTS=3
RETRY_WAIT=30
STAGE_NOW=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode) MODE="$2"; shift 2 ;;
    --repo-root) REPO_ROOT="$2"; shift 2 ;;
    --out-dir) OUT_DIR="$2"; shift 2 ;;
    --engine-root) ENGINE_ROOT="$2"; shift 2 ;;
    --baseline-dir) BASELINE_DIR="$2"; shift 2 ;;
    --candidate-dir) CANDIDATE_DIR="$2"; shift 2 ;;
    --product-rev) PRODUCT_REV="$2"; shift 2 ;;
    --image-archive) IMAGE_ARCHIVE="$2"; shift 2 ;;
    --engine-archive) ENGINE_ARCHIVE="$2"; shift 2 ;;
    --samples) SAMPLES="$2"; shift 2 ;;
    --min-idle) MIN_IDLE="$2"; shift 2 ;;
    --precheck-seconds) PRECHECK_SECONDS="$2"; shift 2 ;;
    --max-attempts) MAX_ATTEMPTS="$2"; shift 2 ;;
    --retry-wait) RETRY_WAIT="$2"; shift 2 ;;
    --stage-now) STAGE_NOW=1; shift 1 ;;
    *) usage ;;
  esac
done

[[ -n "${MODE}" && -n "${REPO_ROOT}" && -n "${OUT_DIR}" ]] || usage
mkdir -p "${OUT_DIR}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

unset PW_PORTABLE_PYTHON_IMAGE_DIR
unset PW_PORTABLE_PYTHON_ENGINE_DIR

OS_NAME="$(uname -s)"
TARGET_TRIPLE="x86_64-unknown-linux-gnu"
if [[ "${OS_NAME}" == "Darwin" ]]; then
  TARGET_TRIPLE="aarch64-apple-darwin"
fi

HARNESS_REV="$(git -C "${REPO_ROOT}" rev-parse HEAD 2>/dev/null || echo unknown)"
if [[ -z "${PRODUCT_REV}" ]]; then
  PRODUCT_REV="${HARNESS_REV}"
fi

# Optionally stage bundle before any timed series (staging is NOT timed).
if [[ "${MODE}" == "baseline" && "${STAGE_NOW}" == "1" ]]; then
  [[ -n "${ENGINE_ROOT}" ]] || usage
  env -u PW_PORTABLE_PYTHON_IMAGE_DIR -u PW_PORTABLE_PYTHON_ENGINE_DIR \
    bash "${REPO_ROOT}/scripts/portable-python/stage-bundled.sh" \
      --root "${ENGINE_ROOT}" \
      --target "${TARGET_TRIPLE}" \
      --require-engine \
      --require-image 2>&1 | tee "${OUT_DIR}/stage-bundle.log"
fi

# Pre-compile the Go test binary outside timed windows so compilation never pollutes idle or timing!
TEST_BIN="${OUT_DIR}/.paired_baseline.test"
(
  cd "${REPO_ROOT}/lycaon"
  CGO_ENABLED=1 go test -c -o "${TEST_BIN}" ./internal/portablepython
) 2>&1 | tee "${OUT_DIR}/compile-harness.log"
trap 'rm -f "${TEST_BIN}"' EXIT

run_one_baseline_attempt() {
  local attempt_dir="$1"
  mkdir -p "${attempt_dir}"

  local pre_rc=0
  python3 "${SCRIPT_DIR}/monitor_host.py" precheck \
    --duration "${PRECHECK_SECONDS}" \
    --min-idle "${MIN_IDLE}" \
    --ignore-pid "$$" \
    --out "${attempt_dir}/host-precheck.json" \
    2>&1 | tee "${attempt_dir}/host-precheck.log" || pre_rc=$?

  local stop_file="${attempt_dir}/.watch.stop"
  rm -f "${stop_file}"
  python3 "${SCRIPT_DIR}/monitor_host.py" watch \
    --stop-file "${stop_file}" \
    --ignore-pid "$$" \
    --out "${attempt_dir}/host-during.json" &
  local watch_pid=$!

  (
    cd "${REPO_ROOT}/lycaon/internal/portablepython"
    env -u PW_PORTABLE_PYTHON_IMAGE_DIR -u PW_PORTABLE_PYTHON_ENGINE_DIR \
      PW_QUAL_ENGINE_ROOT="${ENGINE_ROOT}" \
      PW_PY10_PAIRED_BASELINE=1 \
      PW_PY10_REQUIRE_INSTALLED=1 \
      PW_PAIRED_LABEL="py10-baseline" \
      PW_PAIRED_SAMPLES="${SAMPLES}" \
      PW_PAIRED_HARNESS_REV="${HARNESS_REV}" \
      PW_PAIRED_PRODUCT_REV="${PRODUCT_REV}" \
      PW_PAIRED_IMAGE_ARCHIVE="${IMAGE_ARCHIVE}" \
      PW_PAIRED_ENGINE_ARCHIVE="${ENGINE_ARCHIVE}" \
      PW_PAIRED_OUT_JSON="${attempt_dir}/paired-baseline-raw.json" \
      "${TEST_BIN}" -test.v -test.count=1 -test.timeout=300s -test.run '^TestPY10_PairedBaseline$' \
      2>&1 | tee "${attempt_dir}/paired-baseline-test.log"
  )

  touch "${stop_file}"
  wait "${watch_pid}" || true
  rm -f "${stop_file}"

  # Combine raw measurements with precheck & during host telemetry into final receipt + markdown summary.
  python3 - "${attempt_dir}" "${pre_rc}" <<'PY'
import json, os, sys
from pathlib import Path

adir = Path(sys.argv[1])
pre_rc = int(sys.argv[2])
raw = json.loads((adir / "paired-baseline-raw.json").read_text())
pre = json.loads((adir / "host-precheck.json").read_text())
dur = json.loads((adir / "host-during.json").read_text())

contaminated = bool(pre.get("contaminated")) or bool(dur.get("contaminated")) or (pre_rc != 0)
reasons = list(pre.get("contamination_reasons", [])) + list(dur.get("contamination_reasons", []))

receipt = dict(raw)
receipt["contaminated"] = contaminated
receipt["contamination_reasons"] = reasons
receipt["host_telemetry"] = {
    "hostname": pre.get("hostname"),
    "logical_cpus": pre.get("logical_cpus"),
    "precheck_summary": pre.get("summary"),
    "precheck_loadavg_end": pre["samples"][-1]["loadavg"] if pre.get("samples") else [],
    "during_summary": dur.get("summary"),
    "during_loadavg_end": dur["samples"][-1]["loadavg"] if dur.get("samples") else [],
}

(adir / "paired-baseline-receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")

lines = [
    f"# Paired Performance Baseline ({receipt['platform']})",
    "",
    f"- **Contaminated:** `{contaminated}`" + (f" ({'; '.join(reasons)})" if reasons else " (clean >= 90% idle pre-series, zero competing jobs)"),
    f"- **Harness revision:** `{receipt['harness_revision']}`",
    f"- **Product revision:** `{receipt['product_revision']}`",
    f"- **Backend:** `{receipt['selected_backend']}`",
    f"- **Engine:** `{receipt.get('native_engine', {}).get('engine_tag')}` commit `{receipt.get('native_engine', {}).get('source_commit')}` (`cpython_wasm_sha256={receipt.get('native_engine', {}).get('cpython_wasm_sha256')}`)",
    f"- **Image:** `{receipt['image_id']}` (`manifest_sha256={receipt['manifest_sha256']}`)",
    f"- **Overrides UNSET:** image=`{receipt['image_override_unset']}`, engine=`{receipt['engine_override_unset']}`",
    f"- **Cold cache policy:** `{receipt['cold_cache_policy']}`",
    f"- **Host:** `{pre.get('hostname')}` ({pre.get('logical_cpus')} logical CPUs), pre-series mean idle `{pre['summary']['mean_idle_pct']}%` (min `{pre['summary']['min_idle_pct']}%`), load `{receipt['host_telemetry']['precheck_loadavg_end']}`, during-series competing events `{dur['summary']['competing_process_events']}`",
    "",
    "## Measurements (5 Samples per Workload x Mode)",
    "",
    "| Workload | Mode | Median Construct (ms) | Median Tool (ms) | Median Total (ms) | p95 Total (ms) | Median Fuel | Peak Mem (pages / MiB) | Individual Total Times (ms) |",
    "| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |",
]
for w in receipt["workloads"]:
    for mode_key in ("cold", "warm"):
        m = w.get(mode_key)
        if not m:
            continue
        indiv = ", ".join(f"{s['total_time_ms']:.1f}" for s in m["samples"])
        lines.append(
            f"| `{w['workload']}` | `{mode_key}` | {m['median_service_construction_ms']:.1f} | "
            f"{m['median_tool_latency_ms']:.1f} | {m['median_total_time_ms']:.1f} | {m['p95_total_time_ms']:.1f} | "
            f"{m['median_fuel']:,} | {m['median_peak_memory_pages']} ({m['median_peak_memory_mib']:.1f} MiB) | [{indiv}] |"
        )

sz = receipt["sizes"]
lines += [
    "",
    "## Artifact & Installed Sizes",
    "",
    "| Artifact | Bytes | MiB |",
    "| --- | ---: | ---: |",
    f"| Image archive (`.tar.gz`) | {sz.get('image_archive_bytes', 0):,} | {sz.get('image_archive_bytes', 0)/1048576:.2f} |",
    f"| Engine archive (`.tar.gz`) | {sz.get('engine_archive_bytes', 0):,} | {sz.get('engine_archive_bytes', 0)/1048576:.2f} |",
    f"| Staged image tree (uncompressed) | {sz.get('staged_image_bytes', 0):,} | {sz.get('staged_image_bytes', 0)/1048576:.2f} |",
    f"| Staged native shared library | {sz.get('engine_library_bytes', 0):,} | {sz.get('engine_library_bytes', 0)/1048576:.2f} |",
]
(adir / "paired-baseline-summary.md").write_text("\n".join(lines) + "\n")
sys.exit(1 if contaminated else 0)
PY
}

if [[ "${MODE}" == "baseline" ]]; then
  [[ -n "${ENGINE_ROOT}" ]] || usage
  CLEAN=0
  for ((attempt=1; attempt<=MAX_ATTEMPTS; attempt++)); do
    ATTEMPT_DIR="${OUT_DIR}/attempt-${attempt}"
    if run_one_baseline_attempt "${ATTEMPT_DIR}"; then
      cp -f "${ATTEMPT_DIR}/"* "${OUT_DIR}/"
      rm -rf "${ATTEMPT_DIR}"
      CLEAN=1
      break
    else
      mv "${ATTEMPT_DIR}" "${OUT_DIR}/contaminated-attempt-${attempt}"
      echo "Attempt ${attempt}/${MAX_ATTEMPTS} marked CONTAMINATED (retained in ${OUT_DIR}/contaminated-attempt-${attempt})" >&2
      if [[ "${attempt}" -lt "${MAX_ATTEMPTS}" ]]; then
        echo "Waiting ${RETRY_WAIT}s before retry..." >&2
        sleep "${RETRY_WAIT}"
      fi
    fi
  done

  if [[ "${CLEAN}" != "1" ]]; then
    echo "ERROR: All ${MAX_ATTEMPTS} baseline attempts were contaminated (host did not meet >= ${MIN_IDLE}% idle / zero competing jobs)." >&2
    exit 1
  fi
  echo "PAIRED_BASELINE_CLEAN: ${OUT_DIR}/paired-baseline-receipt.json"
  exit 0
fi

if [[ "${MODE}" == "compare" ]]; then
  [[ -n "${BASELINE_DIR}" && -n "${CANDIDATE_DIR}" ]] || usage
  PRE_RC=0
  python3 "${SCRIPT_DIR}/monitor_host.py" precheck \
    --duration "${PRECHECK_SECONDS}" \
    --min-idle "${MIN_IDLE}" \
    --ignore-pid "$$" \
    --out "${OUT_DIR}/host-precheck.json" \
    2>&1 | tee "${OUT_DIR}/host-precheck.log" || PRE_RC=$?

  STOP_FILE="${OUT_DIR}/.watch.stop"
  rm -f "${STOP_FILE}"
  python3 "${SCRIPT_DIR}/monitor_host.py" watch \
    --stop-file "${STOP_FILE}" \
    --ignore-pid "$$" \
    --out "${OUT_DIR}/host-during.json" &
  WATCH_PID=$!

  # Alternating A/B/A/B... order: for each round r=1..SAMPLES, run baseline (A) then candidate (B).
  mkdir -p "${OUT_DIR}/rounds"
  for ((r=1; r<=SAMPLES; r++)); do
    for side in baseline candidate; do
      ROOT_DIR="${BASELINE_DIR}"
      [[ "${side}" == "candidate" ]] && ROOT_DIR="${CANDIDATE_DIR}"
      (
        cd "${REPO_ROOT}/lycaon/internal/portablepython"
        env -u PW_PORTABLE_PYTHON_IMAGE_DIR -u PW_PORTABLE_PYTHON_ENGINE_DIR \
          PW_QUAL_ENGINE_ROOT="${ROOT_DIR}" \
          PW_PY10_PAIRED_BASELINE=1 \
          PW_PY10_REQUIRE_INSTALLED=1 \
          PW_PAIRED_LABEL="${side}-round-${r}" \
          PW_PAIRED_SAMPLES=1 \
          PW_PAIRED_HARNESS_REV="${HARNESS_REV}" \
          PW_PAIRED_PRODUCT_REV="${PRODUCT_REV}" \
          PW_PAIRED_OUT_JSON="${OUT_DIR}/rounds/round-${r}-${side}.json" \
          "${TEST_BIN}" -test.v -test.count=1 -test.timeout=300s -test.run '^TestPY10_PairedBaseline$' \
          >> "${OUT_DIR}/compare-test.log" 2>&1
      )
    done
  done

  touch "${STOP_FILE}"
  wait "${WATCH_PID}" || true
  rm -f "${STOP_FILE}"

  python3 - "${OUT_DIR}" "${SAMPLES}" "${PRE_RC}" <<'PY'
import json, statistics, sys
from pathlib import Path

out_dir = Path(sys.argv[1])
samples = int(sys.argv[2])
pre_rc = int(sys.argv[3])
pre = json.loads((out_dir / "host-precheck.json").read_text())
dur = json.loads((out_dir / "host-during.json").read_text())
contaminated = bool(pre.get("contaminated")) or bool(dur.get("contaminated")) or (pre_rc != 0)

agg = {"baseline": {}, "candidate": {}}
for r in range(1, samples + 1):
    for side in ("baseline", "candidate"):
        d = json.loads((out_dir / "rounds" / f"round-{r}-{side}.json").read_text())
        for w in d["workloads"]:
            wn = w["workload"]
            for mk in ("cold", "warm"):
                if w.get(mk):
                    agg[side].setdefault((wn, mk), []).extend(w[mk]["samples"])

comparisons = []
regression_flags = []
for key in sorted(agg["baseline"].keys()):
    wn, mk = key
    b_tool = statistics.median([s["tool_latency_ms"] for s in agg["baseline"][key]])
    c_tool = statistics.median([s["tool_latency_ms"] for s in agg["candidate"][key]])
    b_tot = statistics.median([s["total_time_ms"] for s in agg["baseline"][key]])
    c_tot = statistics.median([s["total_time_ms"] for s in agg["candidate"][key]])
    b_con = statistics.median([s["service_construction_ms"] for s in agg["baseline"][key]])
    c_con = statistics.median([s["service_construction_ms"] for s in agg["candidate"][key]])
    delta_tool = c_tool - b_tool
    pct_tool = (delta_tool / b_tool * 100.0) if b_tool > 0 else 0.0
    flagged = (wn == "pass_noop" and delta_tool > 50.0 and pct_tool > 10.0)
    if flagged:
        regression_flags.append(f"{wn}/{mk}: +{delta_tool:.1f}ms (+{pct_tool:.1f}%) exceeds >10% and >50ms")
    comparisons.append({
        "workload": wn,
        "mode": mk,
        "baseline_median_construct_ms": round(b_con, 2),
        "candidate_median_construct_ms": round(c_con, 2),
        "baseline_median_tool_ms": round(b_tool, 2),
        "candidate_median_tool_ms": round(c_tool, 2),
        "delta_tool_ms": round(delta_tool, 2),
        "delta_tool_pct": round(pct_tool, 2),
        "baseline_median_total_ms": round(b_tot, 2),
        "candidate_median_total_ms": round(c_tot, 2),
        "flagged_regression": flagged,
    })

report = {
    "schema_version": "paintedwolf.portable-python.paired-compare/v1",
    "order": "alternating-ABAB",
    "rounds": samples,
    "contaminated": contaminated,
    "comparisons": comparisons,
    "regression_flags": regression_flags,
}
(out_dir / "paired-compare-report.json").write_text(json.dumps(report, indent=2) + "\n")
print(json.dumps(report, indent=2))
sys.exit(1 if (contaminated or regression_flags) else 0)
PY
  exit $?
fi

usage
