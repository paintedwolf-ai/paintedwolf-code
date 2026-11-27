#!/usr/bin/env bash
# Reusable Paired Performance Runner for Portable Python (PY10 baseline & PY11/PY12/PY13 A/B comparisons).
#
# Modes:
#   --mode baseline:
#     Stages (or uses --engine-root) the frozen product pair with PW_PORTABLE_PYTHON_IMAGE_DIR and
#     PW_PORTABLE_PYTHON_ENGINE_DIR UNSET, enforces 30s >= 90% CPU idle pre-series gate and during-series
#     host monitoring, and runs 2 workloads (pass_noop, frozen_workbook) x 2 modes:
#       - cold: 5 newly started services with 1 fresh guest each (normal OS page cache, no purge)
#       - warm: 1 initialized service + 1 excluded priming call + 5 subsequent fresh guests on that SAME service
#
#   --mode compare --baseline-dir <engine-root-A> --candidate-dir <engine-root-B>:
#     Verifies distinct per-side identities (failing closed on same-root or identical image+engine digests
#     unless --allow-same-image is explicitly passed), enforces 30s >= 90% idle gate and during-series
#     monitoring, alternates cold rounds (A_cold_1, B_cold_1, ..., A_cold_5, B_cold_5) AND runs true
#     reused-service warm series (1 excluded priming call + 5 subsequent fresh guests on 1 reused service
#     per side, alternating side order across workloads). Flags frozen_workbook > 5.0s and any pass_noop
#     tool latency increase exceeding BOTH 10% and 50 ms.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: run-paired.sh --mode <baseline|compare> --repo-root <dir> --out-dir <dir>
       [--engine-root <dir>] [--product-rev <sha>] [--image-archive <tar.gz>] [--engine-archive <tar.gz>]
       [--baseline-dir <dir>] [--candidate-dir <dir>]
       [--baseline-product-rev <sha>] [--candidate-product-rev <sha>]
       [--baseline-image-archive <tar.gz>] [--baseline-engine-archive <tar.gz>]
       [--candidate-image-archive <tar.gz>] [--candidate-engine-archive <tar.gz>]
       [--allow-same-image] [--check-identities-only]
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
BASELINE_PRODUCT_REV=""
CANDIDATE_PRODUCT_REV=""
IMAGE_ARCHIVE=""
ENGINE_ARCHIVE=""
BASELINE_IMAGE_ARCHIVE=""
BASELINE_ENGINE_ARCHIVE=""
CANDIDATE_IMAGE_ARCHIVE=""
CANDIDATE_ENGINE_ARCHIVE=""
ALLOW_SAME_IMAGE=0
CHECK_IDENTITIES_ONLY=0
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
    --baseline-product-rev) BASELINE_PRODUCT_REV="$2"; shift 2 ;;
    --candidate-product-rev) CANDIDATE_PRODUCT_REV="$2"; shift 2 ;;
    --image-archive) IMAGE_ARCHIVE="$2"; shift 2 ;;
    --engine-archive) ENGINE_ARCHIVE="$2"; shift 2 ;;
    --baseline-image-archive) BASELINE_IMAGE_ARCHIVE="$2"; shift 2 ;;
    --baseline-engine-archive) BASELINE_ENGINE_ARCHIVE="$2"; shift 2 ;;
    --candidate-image-archive) CANDIDATE_IMAGE_ARCHIVE="$2"; shift 2 ;;
    --candidate-engine-archive) CANDIDATE_ENGINE_ARCHIVE="$2"; shift 2 ;;
    --allow-same-image) ALLOW_SAME_IMAGE=1; shift 1 ;;
    --check-identities-only) CHECK_IDENTITIES_ONLY=1; shift 1 ;;
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

HARNESS_REV="${PW_PAIRED_HARNESS_REV:-$(git -C "${REPO_ROOT}" rev-parse HEAD 2>/dev/null || echo unknown)}"
if [[ -z "${PRODUCT_REV}" ]]; then
  PRODUCT_REV="${HARNESS_REV}"
fi
if [[ -z "${BASELINE_PRODUCT_REV}" ]]; then
  BASELINE_PRODUCT_REV="${PRODUCT_REV}"
fi
if [[ -z "${CANDIDATE_PRODUCT_REV}" ]]; then
  CANDIDATE_PRODUCT_REV="${PRODUCT_REV}"
fi

# In compare mode, verify per-side exact identities BEFORE compilation or timing.
# Fails closed on same staged root OR identical (image_manifest_sha256, library_sha256) unless --allow-same-image.
if [[ "${MODE}" == "compare" ]]; then
  [[ -n "${BASELINE_DIR}" && -n "${CANDIDATE_DIR}" ]] || usage
  python3 - "${BASELINE_DIR}" "${CANDIDATE_DIR}" "${BASELINE_PRODUCT_REV}" "${CANDIDATE_PRODUCT_REV}" "${HARNESS_REV}" "${ALLOW_SAME_IMAGE}" "${OUT_DIR}/compare-identities.json" <<'PY'
import hashlib, json, os, sys
from pathlib import Path

b_dir, c_dir, b_rev, c_rev, h_rev, allow_same_raw, out_path = sys.argv[1:]
allow_same = allow_same_raw == "1"

def sha256_file(p: Path) -> str:
    h = hashlib.sha256()
    with p.open("rb") as fh:
        for chunk in iter(lambda: fh.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()

def inspect_side(label: str, raw_dir: str, prod_rev: str) -> dict:
    root = Path(raw_dir).resolve()
    img_mf = root / "portable-python" / "image" / "image-manifest.json"
    eng_dir = root / "portable-python" / "engine"
    eng_mf = eng_dir / "artifact-manifest.json"
    if not img_mf.is_file():
        raise SystemExit(f"ERROR [{label}]: missing staged image manifest: {img_mf}")
    if not eng_mf.is_file():
        raise SystemExit(f"ERROR [{label}]: missing staged engine manifest: {eng_mf}")
    img_data = json.loads(img_mf.read_text(encoding="utf-8"))
    eng_data = json.loads(eng_mf.read_text(encoding="utf-8"))
    lib_candidates = [eng_dir / "libpw_cpython_wasi.dylib", eng_dir / "libpw_cpython_wasi.so"]
    lib_path = next((p for p in lib_candidates if p.is_file()), None)
    if lib_path is None:
        raise SystemExit(f"ERROR [{label}]: missing staged native engine library in {eng_dir}")
    return {
        "label": label,
        "engine_root_realpath": str(root),
        "harness_revision": h_rev,
        "product_revision": prod_rev,
        "image_id": img_data.get("image_id", ""),
        "image_manifest_sha256": sha256_file(img_mf),
        "image_cpython_wasm_sha256": img_data.get("cpython_wasm_sha256", ""),
        "engine_tag": eng_data.get("engine_tag", ""),
        "engine_source_commit": eng_data.get("source_commit", ""),
        "engine_cpython_wasm_sha256": eng_data.get("cpython_wasm_sha256", ""),
        "library_path": str(lib_path),
        "library_sha256": sha256_file(lib_path),
        "library_bytes": lib_path.stat().st_size,
    }

b_id = inspect_side("baseline", b_dir, b_rev)
c_id = inspect_side("candidate", c_dir, c_rev)

same_root = b_id["engine_root_realpath"] == c_id["engine_root_realpath"]
same_pair = (
    b_id["image_manifest_sha256"] == c_id["image_manifest_sha256"]
    and b_id["library_sha256"] == c_id["library_sha256"]
)

payload = {
    "harness_revision": h_rev,
    "allow_same_image": allow_same,
    "same_root": same_root,
    "same_image_and_engine_digest": same_pair,
    "baseline": b_id,
    "candidate": c_id,
}
Path(out_path).write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")

if not allow_same:
    if same_root:
        raise SystemExit(
            f"ERROR: baseline and candidate resolve to the SAME staged engine root ({b_id['engine_root_realpath']}). "
            "Refusing same-root comparison (pass --allow-same-image only for explicit self-checks)."
        )
    if same_pair:
        raise SystemExit(
            f"ERROR: baseline and candidate have IDENTICAL image_manifest_sha256 ({b_id['image_manifest_sha256']}) "
            f"and engine library_sha256 ({b_id['library_sha256']}). "
            "Refusing identical-pair comparison (pass --allow-same-image only for explicit self-checks)."
        )
print(f"COMPARE_IDENTITIES_OK: baseline={b_id['image_id']} ({b_id['product_revision'][:10]}) vs candidate={c_id['image_id']} ({c_id['product_revision'][:10]})")
PY
  if [[ "${CHECK_IDENTITIES_ONLY}" == "1" ]]; then
    exit 0
  fi
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
import json, sys
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
    f"- **Warm protocol:** 1 initialized service + 1 excluded priming call + {receipt['workloads'][0]['warm']['sample_count'] if receipt['workloads'] and receipt['workloads'][0].get('warm') else 5} subsequent fresh guests on the same reused service",
    f"- **Host:** `{pre.get('hostname')}` ({pre.get('logical_cpus')} logical CPUs), pre-series mean idle `{pre['summary']['mean_idle_pct']}%` (min `{pre['summary']['min_idle_pct']}%`), load `{receipt['host_telemetry']['precheck_loadavg_end']}`, during-series competing events `{dur['summary']['competing_process_events']}`",
    "",
    "## Measurements (5 Samples per Workload x Mode)",
    "",
    "| Workload | Mode | Priming Excluded (ms) | Median Construct (ms) | Median Tool (ms) | Median Total (ms) | p95 Total (ms) | Median Fuel | Peak Mem (pages / MiB) | Individual Total Times (ms) |",
    "| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |",
]
for w in receipt["workloads"]:
    for mode_key in ("cold", "warm"):
        m = w.get(mode_key)
        if not m:
            continue
        indiv = ", ".join(f"{s['total_time_ms']:.1f}" for s in m["samples"])
        prim = f"{m['priming_sample_excluded']['tool_latency_ms']:.1f}" if m.get("priming_sample_excluded") else "—"
        lines.append(
            f"| `{w['workload']}` | `{mode_key}` | {prim} | {m['median_service_construction_ms']:.1f} | "
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
  python3 "${SCRIPT_DIR}/grade_paired_receipt.py" \
    --kind baseline \
    --receipt "${attempt_dir}/paired-baseline-receipt.json" \
    --expected-samples "${SAMPLES}"
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

  mkdir -p "${OUT_DIR}/cold-rounds" "${OUT_DIR}/warm-series"

  run_side() {
    local side="$1" workloads="$2" modes="$3" n_samples="$4" out_json="$5" label="$6"
    local root_dir="${BASELINE_DIR}" prod_rev="${BASELINE_PRODUCT_REV}" img_arc="${BASELINE_IMAGE_ARCHIVE}" eng_arc="${BASELINE_ENGINE_ARCHIVE}"
    if [[ "${side}" == "candidate" ]]; then
      root_dir="${CANDIDATE_DIR}"
      prod_rev="${CANDIDATE_PRODUCT_REV}"
      img_arc="${CANDIDATE_IMAGE_ARCHIVE}"
      eng_arc="${CANDIDATE_ENGINE_ARCHIVE}"
    fi
    (
      cd "${REPO_ROOT}/lycaon/internal/portablepython"
      env -u PW_PORTABLE_PYTHON_IMAGE_DIR -u PW_PORTABLE_PYTHON_ENGINE_DIR \
        PW_QUAL_ENGINE_ROOT="${root_dir}" \
        PW_PY10_PAIRED_BASELINE=1 \
        PW_PY10_REQUIRE_INSTALLED=1 \
        PW_PAIRED_LABEL="${label}" \
        PW_PAIRED_WORKLOADS="${workloads}" \
        PW_PAIRED_MODES="${modes}" \
        PW_PAIRED_SAMPLES="${n_samples}" \
        PW_PAIRED_HARNESS_REV="${HARNESS_REV}" \
        PW_PAIRED_PRODUCT_REV="${prod_rev}" \
        PW_PAIRED_IMAGE_ARCHIVE="${img_arc}" \
        PW_PAIRED_ENGINE_ARCHIVE="${eng_arc}" \
        PW_PAIRED_OUT_JSON="${out_json}" \
        "${TEST_BIN}" -test.v -test.count=1 -test.timeout=300s -test.run '^TestPY10_PairedBaseline$' \
        >> "${OUT_DIR}/compare-test.log" 2>&1
    )
  }

  # Phase 1: Alternating cold rounds (A_cold_1, B_cold_1, ..., A_cold_N, B_cold_N) — each is a brand-new service + 1 fresh guest.
  for ((r=1; r<=SAMPLES; r++)); do
    run_side baseline "pass_noop,frozen_workbook" "cold" 1 "${OUT_DIR}/cold-rounds/round-${r}-baseline.json" "baseline-cold-${r}"
    run_side candidate "pass_noop,frozen_workbook" "cold" 1 "${OUT_DIR}/cold-rounds/round-${r}-candidate.json" "candidate-cold-${r}"
  done

  # Phase 2: Reused-service warm series (1 initialized service + 1 excluded priming call + N subsequent fresh guests
  # on that SAME service), alternating which side runs first across workloads.
  run_side baseline "pass_noop" "warm" "${SAMPLES}" "${OUT_DIR}/warm-series/pass_noop-baseline.json" "baseline-warm-pass_noop"
  run_side candidate "pass_noop" "warm" "${SAMPLES}" "${OUT_DIR}/warm-series/pass_noop-candidate.json" "candidate-warm-pass_noop"
  run_side candidate "frozen_workbook" "warm" "${SAMPLES}" "${OUT_DIR}/warm-series/frozen_workbook-candidate.json" "candidate-warm-frozen_workbook"
  run_side baseline "frozen_workbook" "warm" "${SAMPLES}" "${OUT_DIR}/warm-series/frozen_workbook-baseline.json" "baseline-warm-frozen_workbook"

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
idents = json.loads((out_dir / "compare-identities.json").read_text())
contaminated = bool(pre.get("contaminated")) or bool(dur.get("contaminated")) or (pre_rc != 0)
reasons = list(pre.get("contamination_reasons", [])) + list(dur.get("contamination_reasons", []))

agg = {"baseline": {}, "candidate": {}}
priming = {"baseline": {}, "candidate": {}}

# Collect alternating cold rounds
for r in range(1, samples + 1):
    for side in ("baseline", "candidate"):
        d = json.loads((out_dir / "cold-rounds" / f"round-{r}-{side}.json").read_text())
        for w in d["workloads"]:
            if w.get("cold"):
                agg[side].setdefault((w["workload"], "cold"), []).extend(w["cold"]["samples"])

# Collect true reused-service warm series (1 excluded priming call + N subsequent fresh guests)
for wn in ("pass_noop", "frozen_workbook"):
    for side in ("baseline", "candidate"):
        d = json.loads((out_dir / "warm-series" / f"{wn}-{side}.json").read_text())
        for w in d["workloads"]:
            if w.get("warm"):
                agg[side][(w["workload"], "warm")] = w["warm"]["samples"]
                priming[side][w["workload"]] = w["warm"].get("priming_sample_excluded")

comparisons = []
regression_flags = []
for key in sorted(agg["baseline"].keys()):
    wn, mk = key
    b_samps = agg["baseline"][key]
    c_samps = agg["candidate"][key]
    b_tool = statistics.median([s["tool_latency_ms"] for s in b_samps])
    c_tool = statistics.median([s["tool_latency_ms"] for s in c_samps])
    b_tot = statistics.median([s["total_time_ms"] for s in b_samps])
    c_tot = statistics.median([s["total_time_ms"] for s in c_samps])
    b_con = statistics.median([s["service_construction_ms"] for s in b_samps])
    c_con = statistics.median([s["service_construction_ms"] for s in c_samps])
    delta_tool = c_tool - b_tool
    pct_tool = (delta_tool / b_tool * 100.0) if b_tool > 0 else 0.0
    delta_con = c_con - b_con
    delta_tot = c_tot - b_tot

    flagged = (wn == "pass_noop" and delta_tool > 50.0 and pct_tool > 10.0)
    if flagged:
        regression_flags.append(f"{wn}/{mk}: +{delta_tool:.1f}ms (+{pct_tool:.1f}%) exceeds >10% and >50ms")
    if wn == "frozen_workbook" and mk == "warm" and c_tool > 5000.0:
        regression_flags.append(f"frozen_workbook/warm candidate median {c_tool:.1f}ms exceeds 5000.0ms limit")

    comparisons.append({
        "workload": wn,
        "mode": mk,
        "sample_count": len(c_samps),
        "baseline_service_instance_ids": [s.get("service_instance_id", "") for s in b_samps],
        "candidate_service_instance_ids": [s.get("service_instance_id", "") for s in c_samps],
        "baseline_invocation_ids": [s.get("invocation_id", "") for s in b_samps],
        "candidate_invocation_ids": [s.get("invocation_id", "") for s in c_samps],
        "baseline_median_construct_ms": round(b_con, 2),
        "candidate_median_construct_ms": round(c_con, 2),
        "delta_construct_ms": round(delta_con, 2),
        "baseline_median_tool_ms": round(b_tool, 2),
        "candidate_median_tool_ms": round(c_tool, 2),
        "delta_tool_ms": round(delta_tool, 2),
        "delta_tool_pct": round(pct_tool, 2),
        "baseline_median_total_ms": round(b_tot, 2),
        "candidate_median_total_ms": round(c_tot, 2),
        "delta_total_ms": round(delta_tot, 2),
        "flagged_regression": flagged,
    })

report = {
    "schema_version": "paintedwolf.portable-python.paired-compare/v2",
    "cold_order": "alternating-ABAB-fresh-service",
    "warm_protocol": "reused-service-1-excluded-priming-plus-5-fresh-guests-alternating-workload-order",
    "samples": samples,
    "contaminated": contaminated,
    "contamination_reasons": reasons,
    "identities": idents,
    "priming_samples_excluded": priming,
    "comparisons": comparisons,
    "regression_flags": regression_flags,
}
(out_dir / "paired-compare-report.json").write_text(json.dumps(report, indent=2) + "\n")
print(json.dumps(report, indent=2))
sys.exit(1 if (contaminated or regression_flags) else 0)
PY
  cmp_rc=$?
  python3 "${SCRIPT_DIR}/grade_paired_receipt.py" \
    --kind compare \
    --receipt "${OUT_DIR}/paired-compare-report.json" \
    --expected-samples "${SAMPLES}" || exit 1
  exit "${cmp_rc}"
fi

usage
