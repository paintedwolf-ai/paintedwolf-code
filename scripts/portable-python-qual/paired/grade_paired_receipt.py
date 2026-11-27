#!/usr/bin/env python3
"""Real paired benchmark receipt grader for Portable Python.

Invoked directly by run-paired.sh on every baseline receipt and compare report,
and exercised directly by PY14 contract tests and test_paired_controls.py.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any, Sequence

DEFAULT_WORKLOADS = ("pass_noop", "frozen_workbook")
DEFAULT_MODES = ("cold", "warm")


def _grade_sample_common(prefix: str, sample: dict[str, Any], expected_mode: str) -> list[str]:
    errs: list[str] = []
    if sample.get("mode") != expected_mode:
        errs.append(f"{prefix}: mode={sample.get('mode')!r}, want {expected_mode!r}")
    if sample.get("clean_completion") is not True:
        errs.append(f"{prefix}: clean_completion={sample.get('clean_completion')!r}, want True")
    if sample.get("exit_code") != 0:
        errs.append(f"{prefix}: exit_code={sample.get('exit_code')!r}, want 0")
    if not sample.get("backend"):
        errs.append(f"{prefix}: missing backend")
    if not sample.get("service_instance_id"):
        errs.append(f"{prefix}: missing service_instance_id")
    if not sample.get("invocation_id"):
        errs.append(f"{prefix}: missing invocation_id")
    return errs


def grade_baseline_receipt(
    receipt: dict[str, Any],
    expected_samples: int = 5,
    expected_workloads: Sequence[str] = DEFAULT_WORKLOADS,
    expected_modes: Sequence[str] = DEFAULT_MODES,
) -> list[str]:
    """Validate one paired-baseline-receipt.json produced by run-paired.sh."""
    errs: list[str] = []
    if receipt.get("schema_version") != "paintedwolf.portable-python.paired-baseline/v1":
        errs.append(f"unexpected schema_version={receipt.get('schema_version')!r}")
    if receipt.get("image_override_unset") is not True:
        errs.append("image_override_unset must be True")
    if receipt.get("engine_override_unset") is not True:
        errs.append("engine_override_unset must be True")
    if receipt.get("contaminated") is not False:
        errs.append(f"receipt marked contaminated={receipt.get('contaminated')!r}: {receipt.get('contamination_reasons')}")
    if receipt.get("contamination_reasons"):
        errs.append(f"non-empty contamination_reasons: {receipt.get('contamination_reasons')}")

    for field in ("image_id", "manifest_sha256", "selected_backend", "product_revision", "harness_revision"):
        if not receipt.get(field):
            errs.append(f"missing required identity field {field!r}")
    eng = receipt.get("native_engine") or {}
    for field in ("engine_tag", "library_sha256", "cpython_wasm_sha256"):
        if not eng.get(field):
            errs.append(f"missing native_engine.{field}")

    workloads = receipt.get("workloads") or []
    if not workloads:
        errs.append("receipt contains zero workloads")
        return errs

    by_name: dict[str, dict[str, Any]] = {}
    for w in workloads:
        wname = w.get("workload", "")
        if not wname:
            errs.append("workload entry missing workload name")
            continue
        if wname in by_name:
            errs.append(f"duplicate workload entry {wname!r}")
        by_name[wname] = w

    for req_w in expected_workloads:
        if req_w not in by_name:
            errs.append(f"missing required workload {req_w!r}")

    all_invocations: set[str] = set()

    for wname, w in by_name.items():
        cold_service_ids: set[str] = set()

        if "cold" in expected_modes:
            cold = w.get("cold")
            if not isinstance(cold, dict):
                errs.append(f"{wname}: missing required 'cold' section")
            else:
                samples = cold.get("samples") or []
                if cold.get("mode") != "cold":
                    errs.append(f"{wname}/cold: mode={cold.get('mode')!r}, want 'cold'")
                if cold.get("sample_count") != expected_samples or len(samples) != expected_samples:
                    errs.append(
                        f"{wname}/cold: incomplete samples (sample_count={cold.get('sample_count')}, len={len(samples)}, want {expected_samples})"
                    )
                seen_idx: set[int] = set()
                for i, s in enumerate(samples):
                    prefix = f"{wname}/cold/sample[{i}]"
                    errs.extend(_grade_sample_common(prefix, s, "cold"))
                    idx = s.get("index")
                    if not isinstance(idx, int) or idx < 1 or idx in seen_idx:
                        errs.append(f"{prefix}: invalid or duplicate sample index={idx!r}")
                    else:
                        seen_idx.add(idx)
                    if float(s.get("service_construction_ms", 0)) <= 0:
                        errs.append(f"{prefix}: cold sample must construct a fresh service (service_construction_ms={s.get('service_construction_ms')})")
                    svc_id = s.get("service_instance_id")
                    if svc_id:
                        if svc_id in cold_service_ids:
                            errs.append(f"{prefix}: cold sample reused service_instance_id={svc_id!r} (cold samples must use distinct service instances)")
                        cold_service_ids.add(svc_id)
                    inv_id = s.get("invocation_id")
                    if inv_id:
                        if inv_id in all_invocations:
                            errs.append(f"{prefix}: duplicate invocation_id={inv_id!r}")
                        all_invocations.add(inv_id)

        if "warm" in expected_modes:
            warm = w.get("warm")
            if not isinstance(warm, dict):
                errs.append(f"{wname}: missing required 'warm' section")
            else:
                samples = warm.get("samples") or []
                if warm.get("mode") != "warm":
                    errs.append(f"{wname}/warm: mode={warm.get('mode')!r}, want 'warm'")
                if warm.get("sample_count") != expected_samples or len(samples) != expected_samples:
                    errs.append(
                        f"{wname}/warm: incomplete samples (sample_count={warm.get('sample_count')}, len={len(samples)}, want {expected_samples})"
                    )
                if float(warm.get("init_service_construction_ms", 0)) <= 0:
                    errs.append(f"{wname}/warm: missing init_service_construction_ms > 0")

                priming = warm.get("priming_sample_excluded")
                warm_service_id = ""
                if not isinstance(priming, dict):
                    errs.append(f"{wname}/warm: missing priming_sample_excluded")
                else:
                    errs.extend(_grade_sample_common(f"{wname}/warm/priming", priming, "warm-priming"))
                    if priming.get("index") != 0:
                        errs.append(f"{wname}/warm/priming: index={priming.get('index')!r}, want 0")
                    warm_service_id = str(priming.get("service_instance_id") or "")
                    if warm_service_id and warm_service_id in cold_service_ids:
                        errs.append(f"{wname}/warm/priming: warm service_instance_id={warm_service_id!r} collided with a cold service instance")
                    p_inv = priming.get("invocation_id")
                    if p_inv:
                        if p_inv in all_invocations:
                            errs.append(f"{wname}/warm/priming: duplicate invocation_id={p_inv!r}")
                        all_invocations.add(p_inv)

                seen_idx = set()
                for i, s in enumerate(samples):
                    prefix = f"{wname}/warm/sample[{i}]"
                    if s.get("mode") == "warm-priming" or s.get("index") == 0:
                        errs.append(f"{prefix}: counted priming call inside measured warm samples (index={s.get('index')}, mode={s.get('mode')!r})")
                    errs.extend(_grade_sample_common(prefix, s, "warm"))
                    idx = s.get("index")
                    if not isinstance(idx, int) or idx < 1 or idx in seen_idx:
                        errs.append(f"{prefix}: invalid or duplicate sample index={idx!r}")
                    else:
                        seen_idx.add(idx)
                    if float(s.get("service_construction_ms", 0)) != 0.0:
                        errs.append(
                            f"{prefix}: fresh service mislabeled warm (service_construction_ms={s.get('service_construction_ms')} != 0)"
                        )
                    svc_id = str(s.get("service_instance_id") or "")
                    if warm_service_id and svc_id and svc_id != warm_service_id:
                        errs.append(
                            f"{prefix}: warm service_instance_id changed ({svc_id!r} != priming {warm_service_id!r}); service was replaced instead of reused"
                        )
                    inv_id = s.get("invocation_id")
                    if inv_id:
                        if inv_id in all_invocations:
                            errs.append(f"{prefix}: duplicate invocation_id={inv_id!r}")
                        all_invocations.add(inv_id)

    return errs


def grade_compare_report(
    report: dict[str, Any],
    expected_samples: int | None = None,
    expected_workloads: Sequence[str] = DEFAULT_WORKLOADS,
    expected_modes: Sequence[str] = DEFAULT_MODES,
) -> list[str]:
    """Validate one paired-compare-report.json produced by run-paired.sh --mode compare."""
    errs: list[str] = []
    if report.get("schema_version") != "paintedwolf.portable-python.paired-compare/v2":
        errs.append(f"unexpected schema_version={report.get('schema_version')!r}")
    if report.get("contaminated") is not False:
        errs.append(f"compare report marked contaminated={report.get('contaminated')!r}: {report.get('contamination_reasons')}")
    if report.get("contamination_reasons"):
        errs.append(f"non-empty contamination_reasons: {report.get('contamination_reasons')}")

    want_samples = expected_samples if expected_samples is not None else int(report.get("samples") or 0)
    if want_samples <= 0:
        errs.append(f"invalid samples count: {want_samples}")

    idents = report.get("identities") or {}
    allow_same = bool(idents.get("allow_same_image"))
    base_id = idents.get("baseline") or {}
    cand_id = idents.get("candidate") or {}
    for side_name, side in (("baseline", base_id), ("candidate", cand_id)):
        for field in ("image_id", "image_manifest_sha256", "library_sha256", "product_revision"):
            if not side.get(field):
                errs.append(f"identities.{side_name} missing {field!r}")

    if not allow_same:
        if idents.get("same_root") is True:
            errs.append("intended image-change comparison used same_root=True")
        if idents.get("same_image_and_engine_digest") is True:
            errs.append("intended image-change comparison had same_image_and_engine_digest=True")
        if (
            base_id.get("image_id")
            and base_id.get("image_id") == cand_id.get("image_id")
            and base_id.get("image_manifest_sha256") == cand_id.get("image_manifest_sha256")
        ):
            errs.append(
                f"same actual image ({base_id.get('image_id')}, manifest={base_id.get('image_manifest_sha256')}) on both sides of intended image-change comparison"
            )

    priming = report.get("priming_samples_excluded") or {}
    comparisons = report.get("comparisons") or []
    if not comparisons:
        errs.append("compare report has zero comparisons")
        return errs

    seen_pairs: set[tuple[str, str]] = set()
    for comp in comparisons:
        wn = comp.get("workload", "<unknown>")
        mk = comp.get("mode", "<unknown>")
        seen_pairs.add((wn, mk))
        if comp.get("sample_count") != want_samples:
            errs.append(f"{wn}/{mk}: incomplete sample_count={comp.get('sample_count')}, want {want_samples}")
        for side_name in ("baseline", "candidate"):
            svc_ids = comp.get(f"{side_name}_service_instance_ids")
            inv_ids = comp.get(f"{side_name}_invocation_ids")
            if not isinstance(svc_ids, list) or len(svc_ids) != want_samples or any(not x for x in svc_ids):
                errs.append(f"{wn}/{mk}: missing or incomplete {side_name}_service_instance_ids (want {want_samples})")
            elif mk == "cold":
                if len(set(svc_ids)) != want_samples:
                    errs.append(f"{wn}/cold: {side_name}_service_instance_ids not distinct ({svc_ids})")
            elif mk == "warm":
                p = (priming.get(side_name) or {}).get(wn) or {}
                p_svc = p.get("service_instance_id")
                if not p_svc or any(x != p_svc for x in svc_ids):
                    errs.append(
                        f"{wn}/warm: {side_name} service_instance_id changed across warm samples ({svc_ids} vs priming {p_svc!r})"
                    )
            if not isinstance(inv_ids, list) or len(inv_ids) != want_samples or len(set(inv_ids)) != want_samples or any(not x for x in inv_ids):
                errs.append(f"{wn}/{mk}: {side_name}_invocation_ids missing or non-distinct ({inv_ids})")
        if mk == "warm":
            if float(comp.get("baseline_median_construct_ms", 0)) != 0.0:
                errs.append(
                    f"{wn}/warm: baseline fresh services mislabeled warm (baseline_median_construct_ms={comp.get('baseline_median_construct_ms')})"
                )
            if float(comp.get("candidate_median_construct_ms", 0)) != 0.0:
                errs.append(
                    f"{wn}/warm: candidate fresh services mislabeled warm (candidate_median_construct_ms={comp.get('candidate_median_construct_ms')})"
                )
            for side_name in ("baseline", "candidate"):
                p = (priming.get(side_name) or {}).get(wn)
                if not isinstance(p, dict) or p.get("mode") != "warm-priming" or p.get("index") != 0 or not p.get("service_instance_id") or not p.get("invocation_id"):
                    errs.append(f"{wn}/warm: missing valid excluded priming sample for {side_name}")

    for req_w in expected_workloads:
        for req_m in expected_modes:
            if (req_w, req_m) not in seen_pairs:
                errs.append(f"compare report missing required workload/mode pair ({req_w!r}, {req_m!r})")

    return errs


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Grade Portable Python paired benchmark receipts.")
    parser.add_argument("--kind", choices=("baseline", "compare"), required=True)
    parser.add_argument("--receipt", required=True)
    parser.add_argument("--expected-samples", type=int, default=0)
    args = parser.parse_args(argv)

    data = json.loads(Path(args.receipt).read_text(encoding="utf-8"))
    if args.kind == "baseline":
        samples = args.expected_samples if args.expected_samples > 0 else 5
        errs = grade_baseline_receipt(data, expected_samples=samples)
    else:
        samples = args.expected_samples if args.expected_samples > 0 else None
        errs = grade_compare_report(data, expected_samples=samples)

    if errs:
        for e in errs:
            print(f"RECEIPT_GRADE_FAIL: {e}", file=sys.stderr)
        return 1
    print(f"RECEIPT_GRADE_OK: kind={args.kind} receipt={args.receipt}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
