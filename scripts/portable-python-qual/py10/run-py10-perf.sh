#!/usr/bin/env bash
# PY10 5-Run Fresh-Guest Performance & Size-Growth Matrix Runner.
# Runs with PW_PORTABLE_PYTHON_IMAGE_DIR and PW_PORTABLE_PYTHON_ENGINE_DIR UNSET.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: run-py10-perf.sh --engine-root <dir> --out-dir <dir> [--repo-root <dir>] [--image-archive <tar.gz>] [--engine-archive <tar.gz>] [--warm-runs <n>]
EOF
  exit 2
}

ENGINE_ROOT=""
OUT_DIR=""
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
IMAGE_ARCHIVE=""
ENGINE_ARCHIVE=""
WARM_RUNS="5"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --engine-root) ENGINE_ROOT="$2"; shift 2 ;;
    --out-dir) OUT_DIR="$2"; shift 2 ;;
    --repo-root) REPO_ROOT="$2"; shift 2 ;;
    --image-archive) IMAGE_ARCHIVE="$2"; shift 2 ;;
    --engine-archive) ENGINE_ARCHIVE="$2"; shift 2 ;;
    --warm-runs) WARM_RUNS="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "${ENGINE_ROOT}" && -n "${OUT_DIR}" ]] || usage
mkdir -p "${OUT_DIR}"

unset PW_PORTABLE_PYTHON_IMAGE_DIR
unset PW_PORTABLE_PYTHON_ENGINE_DIR

REV="$(git -C "${REPO_ROOT}" rev-parse HEAD 2>/dev/null || echo unknown)"
LOAD="$(uptime)"
RECEIPT_JSON="${OUT_DIR}/py10-perf-receipt.json"
PDF_OUT="${OUT_DIR}/py10-office-report.pdf"

(
  cd "${REPO_ROOT}/lycaon"
  env -u PW_PORTABLE_PYTHON_IMAGE_DIR -u PW_PORTABLE_PYTHON_ENGINE_DIR \
    LYCAON_ENGINE_ROOT="${ENGINE_ROOT}" \
    PW_QUAL_ENGINE_ROOT="${ENGINE_ROOT}" \
    PW_PY10_PERF=1 \
    PW_PY10_REQUIRE_INSTALLED=1 \
    PW_PY10_PERF_WARM_RUNS="${WARM_RUNS}" \
    PW_PY10_REVISION="${REV}" \
    PW_PY10_HOST_LOAD="${LOAD}" \
    PW_PY10_IMAGE_ARCHIVE="${IMAGE_ARCHIVE}" \
    PW_PY10_ENGINE_ARCHIVE="${ENGINE_ARCHIVE}" \
    PW_PY10_PERF_OUT="${RECEIPT_JSON}" \
    PW_PY10_PERF_PDF_OUT="${PDF_OUT}" \
    go test -v -count=1 -timeout 600s -run '^TestPY10_PerfAndSizeMatrix$' ./internal/portablepython \
    2>&1 | tee "${OUT_DIR}/py10-perf-test.log"
)

python3 - "${RECEIPT_JSON}" "${OUT_DIR}/py10-perf-summary.md" <<'PY'
import json, sys
from pathlib import Path

rec = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
lines = [
    f"# PY10 5-Run Fresh-Guest Timing & Size Summary ({rec['platform']})",
    "",
    f"- **Revision:** `{rec['revision']}`",
    f"- **Backend:** `{rec['selected_backend']}`",
    f"- **Image ID:** `{rec['image_id']}` (`PW_PORTABLE_PYTHON_IMAGE_DIR` unset: `{rec['image_override_unset']}`, `PW_PORTABLE_PYTHON_ENGINE_DIR` unset: `{rec['engine_override_unset']}`)",
    f"- **Host load before:** `{rec['host_load_before']}`",
    f"- **Workbook warm <= 5.0s gate:** `{'PASS' if rec['workbook_gate_passed'] else 'FAIL'}`",
    "",
    "## Fresh-Guest Timings (5 Warm Runs + Cold Launch)",
    "",
    "| Workload | Cold Launch (ms) | Warm Median (ms) | Warm Fuel (units) | Peak Memory (pages / MiB) |",
    "| --- | ---: | ---: | ---: | ---: |",
]
for w in rec["workloads"]:
    mib = w["warm_peak_memory_bytes_median"] / (1024 * 1024)
    lines.append(
        f"| `{w['name']}` | {w['cold_launch_to_first_result_median_ms']:.1f} | "
        f"{w['warm_tool_latency_median_ms']:.1f} | {w['warm_guest_work_median']:,} | "
        f"{w['warm_peak_memory_pages_median']} ({mib:.1f} MiB) |"
    )

sc = rec["size_comparison"]
lines += [
    "",
    "## Image & Engine Size Growth (PY08 -> PY10)",
    "",
    "| Artifact | PY08 (bytes) | PY10 (bytes) | Delta (bytes) |",
    "| --- | ---: | ---: | ---: |",
    f"| Image archive (`.tar.gz`) | {sc['py08_image_archive_bytes']:,} | {sc.get('py10_image_archive_bytes', 0):,} | {sc.get('image_archive_delta_bytes', 0):+,} |",
    f"| Native engine archive (`.tar.gz`) | {sc['py08_engine_archive_bytes']:,} | {sc.get('py10_engine_archive_bytes', 0):,} | {sc.get('engine_archive_delta_bytes', 0):+,} |",
    f"| Native shared library | {sc['py08_library_bytes']:,} | {sc['py10_library_bytes']:,} | {sc['library_delta_bytes']:+,} |",
    f"| Staged image tree (uncompressed) | — | {sc['py10_staged_image_bytes']:,} | — |",
    "",
]
Path(sys.argv[2]).write_text("\n".join(lines), encoding="utf-8")
print("Wrote", sys.argv[2])
PY
