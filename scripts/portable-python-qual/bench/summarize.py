#!/usr/bin/env python3
"""Summarize PY06 idle-host performance JSON receipts and evaluate acceptance thresholds."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any


def load_receipts(paths: list[Path]) -> list[dict[str, Any]]:
    files: list[Path] = []
    for p in paths:
        if p.is_dir():
            files.extend(sorted(p.rglob("*.json")))
        elif p.is_file() and p.suffix == ".json":
            files.append(p)
    receipts: list[dict[str, Any]] = []
    for f in files:
        try:
            data = json.loads(f.read_text(encoding="utf-8"))
        except Exception:
            continue
        if data.get("schema_version") == "portable-python.py06-perf/v1":
            data["_source_file"] = str(f)
            receipts.append(data)
    return receipts


def fmt_s(ms: float) -> str:
    return f"{ms / 1000.0:.3f} s"


def fmt_ms(ms: float) -> str:
    return f"{ms:.1f} ms"


def fmt_mb(n: int) -> str:
    if not n:
        return "—"
    return f"{n / (1024 * 1024):.2f} MiB"


def render_markdown(receipts: list[dict[str, Any]]) -> str:
    lines: list[str] = [
        "# PY06 Performance Matrix Summary",
        "",
        "## Overview",
        "",
        "| Platform | Config | Backend (Default?) | Image | Cold Init Med | Launch->1st Med | Cold Tool Med | Warm Tool Med | Guest Work | Host Work | Peak Mem | Bin Size | Engine Size | Image Size |",
        "| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for r in receipts:
        s = r["summary"]
        peak_str = f"{s['warm_peak_memory_pages_median']}p ({fmt_mb(s['warm_peak_memory_bytes_median'])})"
        b_label = f"`{r['selected_backend']}`" + (" (default)" if r.get("default_backend_used") else " (control)")
        lines.append(
            f"| {r['platform']} | `{r['config_label']}` | {b_label} | `{r['image_variant']}` "
            f"| {fmt_ms(s['cold_service_init_median_ms'])} | {fmt_s(s['cold_launch_to_first_result_median_ms'])} "
            f"| {fmt_s(s['cold_tool_latency_median_ms'])} | {fmt_s(s['warm_tool_latency_median_ms'])} "
            f"| {s['warm_guest_work_median']:,} | {s['warm_host_work_median']:,} "
            f"| {peak_str} | {fmt_mb(r.get('binary_size_bytes', 0))} | {fmt_mb(r.get('engine_size_bytes', 0))} | {fmt_mb(r.get('image_size_bytes', 0))} |"
        )

    lines.extend([
        "",
        "## Warm Phase Breakdown (Medians)",
        "",
        "| Config | YAML Import | openpyxl Import | YAML Parse | Build & Save | Reopen & Verify |",
        "| --- | ---: | ---: | ---: | ---: | ---: |",
    ])
    for r in receipts:
        p = r["summary"]["warm_phases_median"]
        lines.append(
            f"| `{r['config_label']}` | {fmt_ms(p['yaml_import_ms'])} | {fmt_ms(p['openpyxl_import_ms'])} "
            f"| {fmt_ms(p['yaml_parse_ms'])} | {fmt_ms(p['workbook_build_save_ms'])} | {fmt_ms(p['workbook_reopen_verify_ms'])} |"
        )

    lines.extend([
        "",
        "## Individual Per-Call Timings & Provenance",
        "",
    ])
    for r in receipts:
        cold_vals = ", ".join(fmt_s(c["tool_latency_ms"]) for c in r["cold_runs"])
        warm_vals = ", ".join(fmt_s(c["tool_latency_ms"]) for c in r["warm_runs"])
        init_vals = ", ".join(fmt_ms(c["service_init_ms"]) for c in r["cold_runs"])
        lines.append(f"### `{r['config_label']}` ({r['platform']})")
        lines.append(
            f"- **Revision:** `{r['revision']}` · **Backend:** `{r['selected_backend']}` "
            f"(default_route=`{r.get('default_backend_used', False)}`) · **Image ID:** `{r['image_id']}`"
        )
        if r.get("native_engine"):
            ne = r["native_engine"]
            lines.append(
                f"- **Native Engine:** `{ne.get('engine_tag')}` · commit `{ne.get('source_commit')}` · "
                f"ABI `{ne.get('abi_version')}` · imports `{ne.get('import_count')}` · "
                f"WABT `{ne.get('wabt_version')}` · lib SHA-256 `{(r.get('engine_sha256') or '')[:16]}...`"
            )
        lines.append(f"- **Toolchain:** `{r['toolchain']}` · **Signing:** `{r['signing_flags']}`")
        lines.append(f"- **Host:** `{r['cpu_info']}` · **Load before:** `{r['host_load_before']}`")
        lines.append(f"- **Cold service init (1..{len(r['cold_runs'])}):** {init_vals}")
        lines.append(f"- **Cold tool latency (1..{len(r['cold_runs'])}):** {cold_vals}")
        lines.append(f"- **Warm tool latency (1..{len(r['warm_runs'])}):** {warm_vals}")
        lines.append("")

    # Acceptance-plan threshold evaluations
    lines.extend([
        "## Acceptance Threshold Evaluation",
        "",
    ])

    mac_base = next(
        (
            r
            for r in receipts
            if r["goos"] == "darwin"
            and "wazero" in r["selected_backend"]
            and r["image_variant"] == "original"
        ),
        None,
    )
    mac_cands = [
        r
        for r in receipts
        if r["goos"] == "darwin" and r["selected_backend"] == "native-aot"
    ]
    if mac_base and mac_cands:
        base_warm = mac_base["summary"]["warm_tool_latency_median_ms"]
        for c in mac_cands:
            cand_warm = c["summary"]["warm_tool_latency_median_ms"]
            speedup = base_warm / cand_warm if cand_warm > 0 else 0.0
            pass_5s = cand_warm <= 5000.0
            pass_5x = speedup >= 5.0
            verdict = "PASS" if (pass_5s and pass_5x) else "FAIL"
            lines.append(
                f"- **macOS arm64 (`{c['config_label']}` vs `{mac_base['config_label']}`):** "
                f"warm median `{fmt_s(cand_warm)}` (`<= 5.0s`: {pass_5s}), "
                f"speedup `{speedup:.2f}x` (`>= 5.0x`: {pass_5x}) -> **{verdict}**"
            )
    else:
        lines.append("- **macOS arm64:** Awaiting complete baseline + native-aot receipts for 5.0s / 5x gate evaluation.")

    linux_wazero_by_variant = {
        r["image_variant"]: r
        for r in receipts
        if r["goos"] == "linux" and "wazero" in r["selected_backend"]
    }
    linux_cands = [
        r
        for r in receipts
        if r["goos"] == "linux" and r["selected_backend"] == "native-aot"
    ]
    if linux_cands:
        for c in linux_cands:
            ctrl = linux_wazero_by_variant.get(c["image_variant"])
            if not ctrl:
                lines.append(f"- **Linux amd64 (`{c['config_label']}`):** Missing same-variant wazero control (`{c['image_variant']}`).")
                continue
            cold_ratio = c["summary"]["cold_tool_latency_median_ms"] / ctrl["summary"]["cold_tool_latency_median_ms"]
            warm_ratio = c["summary"]["warm_tool_latency_median_ms"] / ctrl["summary"]["warm_tool_latency_median_ms"]
            parity_ok = all(x["clean_completion"] for x in c["cold_runs"] + c["warm_runs"])
            within_10 = cold_ratio <= 1.10 and warm_ratio <= 1.10
            verdict = "PASS (SHIP_NATIVE_AOT_DEFAULT)" if (parity_ok and within_10) else "FAIL (REVERT_TO_LINUX_WAZERO_DEFAULT)"
            lines.append(
                f"- **Linux amd64 (`{c['config_label']}` vs `{ctrl['config_label']}`):** "
                f"cold `{fmt_s(c['summary']['cold_tool_latency_median_ms'])}` vs `{fmt_s(ctrl['summary']['cold_tool_latency_median_ms'])}` (`{cold_ratio:.3f}x`), "
                f"warm `{fmt_s(c['summary']['warm_tool_latency_median_ms'])}` vs `{fmt_s(ctrl['summary']['warm_tool_latency_median_ms'])}` (`{warm_ratio:.3f}x`), "
                f"within 10%: {within_10}, parity={parity_ok} -> **{verdict}**"
            )
    else:
        lines.append("- **Linux amd64:** Awaiting complete wazero control + native-aot receipts for 10% gate evaluation.")

    lines.append("")
    return "\n".join(lines)


def main() -> None:
    ap = argparse.ArgumentParser(description="Summarize PY06 perf receipts")
    ap.add_argument("inputs", nargs="+", type=Path)
    ap.add_argument("--out", type=Path, default=None)
    args = ap.parse_args()

    receipts = load_receipts(args.inputs)
    md = render_markdown(receipts)
    if args.out:
        args.out.write_text(md, encoding="utf-8")
    else:
        print(md)


if __name__ == "__main__":
    main()
