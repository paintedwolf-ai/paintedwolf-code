"""GB04 (Citation resolution) full in-process D1 computational runner.

Generates a deterministic JSONL case file covering all 20,018 in-process
instances for a given `--base-seed` and `--platform`:
- R02: 8 fixed slots (`assert_exact_verdicts=True`)
- R03: 10 fixed slots (`assert_exact_verdicts=True`)
- R04: 10,000 generated Markdown citation/look-alike cases (`generate_r04`)
- R06: 5,000 generated must-pass cases (`generate_r06`, zero false rejects)
- R07: 5,000 generated must-not-pass cases (`generate_r07`, zero false accepts)

Executes against verified D1 `--product-root` via `grounding.inprocess_driver`
using the test-only Go overlay `grounding/overlays/gb04_inprocess_test.go`.
Writes structured `gb04-inprocess-summary.json` and
`gb04-inprocess-divergences.jsonl`.
"""
from __future__ import annotations

import argparse
from dataclasses import dataclass
import json
import os
import pathlib
import sys
import time
from typing import Any

from grounding.delivery_log import DeliveryLog
from grounding.generators import (
    BASE_SEEDS,
    GeneratedR04Case,
    generate_r04,
    generate_r06,
    generate_r07,
)
from grounding.inprocess_driver import (
    InProcessPlan,
    run_inprocess_plan,
    write_test_overlay,
)
from grounding.reference_resolver import Citation, PermittedScope, resolve_citation

from .catalog import (
    GENERATED_BY_FAMILY,
    R02_SLOTS,
    R03_SLOTS,
)
from .fixtures import GB04FixedFixture, build_fixed_fixture


OVERLAY_SOURCE_PATH: pathlib.Path = (
    pathlib.Path(__file__).resolve().parents[2] / "overlays" / "gb04_inprocess_test.go"
)
INPROCESS_FAMILIES: tuple[str, ...] = ("R02", "R03", "R04", "R06", "R07")


def default_platform() -> str:
    return "darwin" if sys.platform == "darwin" else "linux"


def _serialize_records(log: DeliveryLog, scope: PermittedScope) -> list[dict[str, Any]]:
    """Serialize in-scope delivery records for D1's in-process ledger builder."""
    out: list[dict[str, Any]] = []
    for rec in log.records:
        if rec.session_id != scope.session_id:
            continue
        if rec.worker_leg != scope.worker_leg:
            continue
        out.append(
            {
                "handle": rec.handle,
                "kind": rec.kind,
                "tool": rec.tool,
                "path": rec.path,
                "url": rec.url,
                "artifact_id": rec.artifact_id,
                "raw_text": rec.raw_bytes.decode("utf-8", errors="replace"),
                "delivered_ranges": [
                    {"start": r.start, "end": r.end} for r in rec.delivered_ranges
                ],
                "line_numbered": rec.line_numbered,
                "printed_other_numbers": rec.printed_other_numbers,
                "superseded_by": rec.superseded_by,
                "model_visible": rec.model_visible,
            }
        )
    return out


def _serialize_citation(cit: Citation) -> dict[str, Any]:
    return {
        "citation_id": cit.citation_id,
        "citation_type": cit.citation_type,
        "raw": cit.raw,
        "handle": cit.handle,
        "path": cit.path,
        "start_line": cit.start_line or 0,
        "end_line": cit.end_line or 0,
        "excerpt": cit.excerpt,
        "url": cit.url,
        "artifact_id": cit.artifact_id,
    }


@dataclass(frozen=True)
class ExpectedCaseMeta:
    """Expected oracle metadata retained in Python for scoring Go results."""

    case_id: str
    family_id: str
    selector: str
    kind: str  # "r04_parse" | "resolve"
    base_seed: str
    instance_index: int | None
    platform: str
    variation_or_mutation: str
    assert_exact_verdicts: bool
    # For "resolve": list of expected citation dicts
    expected_citations: tuple[dict[str, Any], ...] = ()
    # For "r04_parse": expected span records
    r04_spans: tuple[dict[str, Any], ...] = ()


def build_inprocess_cases(
    *,
    base_seed: str = "20261127",
    platform: str = "linux",
    families: tuple[str, ...] = INPROCESS_FAMILIES,
    limit_generated: int = 0,
    project_dir: str = "/tmp/gb04-inprocess",
) -> tuple[list[dict[str, Any]], dict[str, ExpectedCaseMeta]]:
    """Build all GB04 in-process Go case payloads and Python expected-oracle metadata."""
    if base_seed not in BASE_SEEDS:
        raise ValueError(f"Unsupported base_seed {base_seed!r}; expected one of {BASE_SEEDS}")
    if platform not in ("darwin", "linux"):
        raise ValueError(f"Unsupported platform {platform!r}; expected 'darwin' or 'linux'")
    if limit_generated < 0:
        raise ValueError("limit_generated must be >= 0")

    cases: list[dict[str, Any]] = []
    meta: dict[str, ExpectedCaseMeta] = {}

    def _add_fixed_slot(fx: GB04FixedFixture) -> None:
        cid = fx.spec.selector
        cases.append(
            {
                "case_id": cid,
                "family_id": fx.spec.family_id,
                "selector": fx.spec.selector,
                "kind": "resolve",
                "project_dir": project_dir,
                "records": _serialize_records(fx.delivery_log, fx.scope),
                "permitted_artifacts": sorted(fx.scope.permitted_artifacts or ()),
                "citations": [_serialize_citation(c) for c in fx.citations],
            }
        )
        exp_cits = tuple(
            {
                "citation_id": cit.citation_id,
                "raw": cit.raw,
                "expected_exact_verdict": v.expected_exact_verdict,
                "expected_verdict_class": v.verdict_class,
                "contract_reading_verdict": v.contract_reading_verdict,
                "disputed_expectation": v.disputed_expectation,
                "reason": v.reason,
                "reanchored_line": v.reanchored_line,
            }
            for cit, v in zip(fx.citations, fx.expected_verdicts)
        )
        meta[cid] = ExpectedCaseMeta(
            case_id=cid,
            family_id=fx.spec.family_id,
            selector=fx.spec.selector,
            kind="resolve",
            base_seed=base_seed,
            instance_index=None,
            platform=platform,
            variation_or_mutation=fx.spec.selector,
            assert_exact_verdicts=fx.spec.assert_exact_verdicts,
            expected_citations=exp_cits,
        )

    if "R02" in families:
        for slot in R02_SLOTS:
            _add_fixed_slot(build_fixed_fixture(slot.selector))

    if "R03" in families:
        for slot in R03_SLOTS:
            _add_fixed_slot(build_fixed_fixture(slot.selector))

    if "R04" in families:
        r04_count = limit_generated or GENERATED_BY_FAMILY["R04"].in_process_count
        for idx in range(r04_count):
            gen: GeneratedR04Case = generate_r04(base_seed, idx)
            cid = f"R04-{base_seed}-{idx:05d}"
            cases.append(
                {
                    "case_id": cid,
                    "family_id": "R04",
                    "selector": cid,
                    "kind": "r04_parse",
                    "markdown": gen.markdown,
                }
            )
            meta[cid] = ExpectedCaseMeta(
                case_id=cid,
                family_id="R04",
                selector=cid,
                kind="r04_parse",
                base_seed=base_seed,
                instance_index=idx,
                platform=platform,
                variation_or_mutation="markdown_parse",
                assert_exact_verdicts=False,
                r04_spans=tuple(
                    {
                        "start": s.start,
                        "end": s.end,
                        "text": s.text,
                        "node_kind": s.node_kind,
                        "is_citation_attempt": s.is_citation_attempt,
                        "syntax_valid": s.syntax_valid,
                        "destination_observed": s.destination_observed,
                    }
                    for s in gen.spans
                ),
            )

    if "R06" in families:
        r06_count = limit_generated or GENERATED_BY_FAMILY["R06"].in_process_count
        for idx in range(r06_count):
            gen6 = generate_r06(base_seed, idx, platform=platform)
            v6 = resolve_citation(gen6.delivery_log, gen6.citation, gen6.scope)
            cid = f"R06-{base_seed}-{platform}-{idx:05d}"
            cases.append(
                {
                    "case_id": cid,
                    "family_id": "R06",
                    "selector": cid,
                    "kind": "resolve",
                    "project_dir": project_dir,
                    "records": _serialize_records(gen6.delivery_log, gen6.scope),
                    "permitted_artifacts": sorted(gen6.scope.permitted_artifacts or ()),
                    "citations": [_serialize_citation(gen6.citation)],
                }
            )
            meta[cid] = ExpectedCaseMeta(
                case_id=cid,
                family_id="R06",
                selector=cid,
                kind="resolve",
                base_seed=base_seed,
                instance_index=idx,
                platform=platform,
                variation_or_mutation=gen6.variation,
                assert_exact_verdicts=False,
                expected_citations=(
                    {
                        "citation_id": gen6.citation.citation_id,
                        "raw": gen6.citation.raw,
                        "expected_exact_verdict": v6.expected_exact_verdict,
                        "expected_verdict_class": "grounded",
                        "contract_reading_verdict": v6.contract_reading_verdict,
                        "disputed_expectation": v6.disputed_expectation,
                        "reason": v6.reason,
                        "reanchored_line": v6.reanchored_line,
                    },
                ),
            )

    if "R07" in families:
        r07_count = limit_generated or GENERATED_BY_FAMILY["R07"].in_process_count
        for idx in range(r07_count):
            gen7 = generate_r07(base_seed, idx, platform=platform)
            v7 = resolve_citation(gen7.delivery_log, gen7.citation, gen7.scope)
            cid = f"R07-{base_seed}-{platform}-{idx:05d}"
            cases.append(
                {
                    "case_id": cid,
                    "family_id": "R07",
                    "selector": cid,
                    "kind": "resolve",
                    "project_dir": project_dir,
                    "records": _serialize_records(gen7.delivery_log, gen7.scope),
                    "permitted_artifacts": sorted(gen7.scope.permitted_artifacts or ()),
                    "citations": [_serialize_citation(gen7.citation)],
                }
            )
            meta[cid] = ExpectedCaseMeta(
                case_id=cid,
                family_id="R07",
                selector=cid,
                kind="resolve",
                base_seed=base_seed,
                instance_index=idx,
                platform=platform,
                variation_or_mutation=gen7.mutation,
                assert_exact_verdicts=False,
                expected_citations=(
                    {
                        "citation_id": gen7.citation.citation_id,
                        "raw": gen7.citation.raw,
                        "expected_exact_verdict": v7.expected_exact_verdict,
                        "expected_verdict_class": "ungrounded",
                        "contract_reading_verdict": v7.contract_reading_verdict,
                        "disputed_expectation": v7.disputed_expectation,
                        "reason": v7.reason,
                        "reanchored_line": v7.reanchored_line,
                    },
                ),
            )

    return cases, meta


def _span_overlaps(a_start: int, a_end: int, b_start: int, b_end: int) -> bool:
    return a_start < b_end and b_start < a_end


def evaluate_r04_result(
    expected: ExpectedCaseMeta,
    actual: dict[str, Any],
) -> list[dict[str, Any]]:
    """Compare D1 citationmd spans treated as citation attempts against R04 generator spans."""
    divergences: list[dict[str, Any]] = []
    occ_spans = [
        (int(o["node_start"]), int(o["node_end"]), str(o["kind"]), str(o["token"]))
        for o in (actual.get("occurrences") or [])
    ]
    issue_spans = [
        (int(i["span_start"]), int(i["span_end"]), str(i["code"]), str(i["token"]))
        for i in (actual.get("issues") or [])
    ]

    for sp in expected.r04_spans:
        s_start = int(sp["start"])
        s_end = int(sp["end"])
        node_kind = str(sp["node_kind"])
        is_attempt = bool(sp["is_citation_attempt"])
        matched_occ = [
            o for o in occ_spans if _span_overlaps(s_start, s_end, o[0], o[1])
        ]
        matched_iss = [
            i for i in issue_spans if _span_overlaps(s_start, s_end, i[0], i[1])
        ]

        if node_kind == "oversized_inline_node":
            has_too_long = any(i[2] == "node_too_long" for i in matched_iss)
            if matched_occ or not has_too_long:
                divergences.append(
                    {
                        "case_id": expected.case_id,
                        "family_id": "R04",
                        "selector": expected.selector,
                        "base_seed": expected.base_seed,
                        "instance_index": expected.instance_index,
                        "platform": expected.platform,
                        "node_kind": node_kind,
                        "kind": "oversized_inline_node_defect",
                        "expected": {"issue_code": "node_too_long", "occurrences": 0},
                        "actual": {
                            "occurrences": [o[2] for o in matched_occ],
                            "issues": [i[2] for i in matched_iss],
                        },
                        "detail": f"Oversized span [{s_start}:{s_end}] expected node_too_long issue with 0 occurrences",
                    }
                )
            continue

        detected_as_attempt = bool(matched_occ or matched_iss)
        if is_attempt and not detected_as_attempt:
            divergences.append(
                {
                    "case_id": expected.case_id,
                    "family_id": "R04",
                    "selector": expected.selector,
                    "base_seed": expected.base_seed,
                    "instance_index": expected.instance_index,
                    "platform": expected.platform,
                    "node_kind": node_kind,
                    "kind": "missed_citation_attempt",
                    "expected": {"is_citation_attempt": True, "text": sp["text"]},
                    "actual": {"occurrences": [], "issues": []},
                    "detail": f"D1 citationmd missed {node_kind} span {sp['text']!r} at [{s_start}:{s_end}]",
                }
            )
        elif (not is_attempt) and detected_as_attempt:
            divergences.append(
                {
                    "case_id": expected.case_id,
                    "family_id": "R04",
                    "selector": expected.selector,
                    "base_seed": expected.base_seed,
                    "instance_index": expected.instance_index,
                    "platform": expected.platform,
                    "node_kind": node_kind,
                    "kind": "false_citation_attempt",
                    "expected": {"is_citation_attempt": False, "text": sp["text"]},
                    "actual": {
                        "occurrences": [
                            {"kind": o[2], "token": o[3]} for o in matched_occ
                        ],
                        "issues": [
                            {"code": i[2], "token": i[3]} for i in matched_iss
                        ],
                    },
                    "detail": f"D1 citationmd treated literal/prose {node_kind} span {sp['text']!r} as citation attempt",
                }
            )
    return divergences


def evaluate_resolve_result(
    expected: ExpectedCaseMeta,
    actual: dict[str, Any],
) -> list[dict[str, Any]]:
    """Compare D1 resolution result(s) against expected oracle verdicts."""
    divergences: list[dict[str, Any]] = []
    actual_list = actual.get("resolutions") or []
    if len(actual_list) != len(expected.expected_citations):
        divergences.append(
            {
                "case_id": expected.case_id,
                "family_id": expected.family_id,
                "selector": expected.selector,
                "base_seed": expected.base_seed,
                "instance_index": expected.instance_index,
                "platform": expected.platform,
                "variation_or_mutation": expected.variation_or_mutation,
                "kind": "resolution_count_mismatch",
                "expected": len(expected.expected_citations),
                "actual": len(actual_list),
                "detail": "Mismatch in number of returned citation resolutions",
            }
        )
        return divergences

    for exp_c, act_c in zip(expected.expected_citations, actual_list):
        act_verdict = str(act_c.get("verdict") or "")
        act_grounded = bool(act_c.get("grounded"))
        act_class = "grounded" if act_grounded else "ungrounded"
        exp_class = str(exp_c["expected_verdict_class"])
        exp_exact = str(exp_c["expected_exact_verdict"])

        if act_class != exp_class:
            div_kind = "false_reject" if exp_class == "grounded" else "false_accept"
            divergences.append(
                {
                    "case_id": expected.case_id,
                    "family_id": expected.family_id,
                    "selector": expected.selector,
                    "citation_id": exp_c["citation_id"],
                    "raw": exp_c["raw"],
                    "base_seed": expected.base_seed,
                    "instance_index": expected.instance_index,
                    "platform": expected.platform,
                    "variation_or_mutation": expected.variation_or_mutation,
                    "disputed_expectation": bool(exp_c.get("disputed_expectation")),
                    "kind": div_kind,
                    "expected": {
                        "verdict_class": exp_class,
                        "exact_verdict": exp_exact,
                        "contract_reading_verdict": exp_c.get("contract_reading_verdict"),
                    },
                    "actual": act_c,
                    "detail": (
                        f"{expected.selector} ({exp_c['citation_id']}): "
                        f"expected {exp_class}/{exp_exact}, got {act_class}/{act_verdict} "
                        f"(outcome={act_c.get('outcome')!r}, code={act_c.get('code')!r}, reason={act_c.get('reason')!r})"
                    ),
                }
            )
            continue

        if expected.assert_exact_verdicts and act_verdict != exp_exact:
            divergences.append(
                {
                    "case_id": expected.case_id,
                    "family_id": expected.family_id,
                    "selector": expected.selector,
                    "citation_id": exp_c["citation_id"],
                    "raw": exp_c["raw"],
                    "base_seed": expected.base_seed,
                    "instance_index": expected.instance_index,
                    "platform": expected.platform,
                    "variation_or_mutation": expected.variation_or_mutation,
                    "disputed_expectation": bool(exp_c.get("disputed_expectation")),
                    "kind": "exact_verdict_mismatch",
                    "expected": {
                        "verdict_class": exp_class,
                        "exact_verdict": exp_exact,
                        "contract_reading_verdict": exp_c.get("contract_reading_verdict"),
                    },
                    "actual": act_c,
                    "detail": (
                        f"{expected.selector} ({exp_c['citation_id']}): "
                        f"exact verdict mismatch expected={exp_exact!r}, actual={act_verdict!r}"
                    ),
                }
            )
    return divergences


def run_gb04_inprocess(
    *,
    product_root: str | pathlib.Path,
    out_dir: str | pathlib.Path,
    base_seed: str = "20261127",
    platform: str | None = None,
    families: tuple[str, ...] = INPROCESS_FAMILIES,
    limit_generated: int = 0,
    dry_run: bool = False,
    timeout: float = 600.0,
) -> dict[str, Any]:
    """Generate cases, invoke D1 in-process Go overlay, and write summary + divergences."""
    t0 = time.monotonic()
    plat = platform or default_platform()
    out_path = pathlib.Path(out_dir).resolve()
    out_path.mkdir(parents=True, exist_ok=True)
    prod_root = pathlib.Path(product_root).resolve()

    cases, expected_meta = build_inprocess_cases(
        base_seed=base_seed,
        platform=plat,
        families=families,
        limit_generated=limit_generated,
        project_dir=str(out_path / "virtual-project"),
    )

    cases_file = out_path / "gb04-inprocess-cases.jsonl"
    results_file = out_path / "gb04-inprocess-results.jsonl"
    overlay_file = out_path / "gb04-inprocess-overlay.json"
    summary_file = out_path / "gb04-inprocess-summary.json"
    divergences_file = out_path / "gb04-inprocess-divergences.jsonl"

    with cases_file.open("w", encoding="utf-8") as fh:
        for item in cases:
            fh.write(json.dumps(item, ensure_ascii=False) + "\n")

    virtual_target = prod_root / "lycaon" / "internal" / "guidance" / "gb04_inprocess_test.go"
    write_test_overlay(
        overlay_file,
        {str(virtual_target): str(OVERLAY_SOURCE_PATH.resolve())},
        product_root=prod_root,
    )

    plan = InProcessPlan(
        product_root=prod_root,
        packages=("./internal/guidance",),
        run_pattern="^TestGB04InProcess$",
        overlay_path=overlay_file,
        count=1,
    )

    prev_cases = os.environ.get("GB04_CASES_FILE")
    prev_results = os.environ.get("GB04_RESULTS_FILE")
    try:
        os.environ["GB04_CASES_FILE"] = str(cases_file)
        os.environ["GB04_RESULTS_FILE"] = str(results_file)
        go_run = run_inprocess_plan(plan, dry_run=dry_run, timeout=timeout)
    finally:
        if prev_cases is None:
            os.environ.pop("GB04_CASES_FILE", None)
        else:
            os.environ["GB04_CASES_FILE"] = prev_cases
        if prev_results is None:
            os.environ.pop("GB04_RESULTS_FILE", None)
        else:
            os.environ["GB04_RESULTS_FILE"] = prev_results

    family_stats: dict[str, dict[str, int]] = {}
    for fam in families:
        family_stats[fam] = {"total": 0, "passed": 0, "diverged": 0}
    for m in expected_meta.values():
        family_stats.setdefault(m.family_id, {"total": 0, "passed": 0, "diverged": 0})
        family_stats[m.family_id]["total"] += 1

    all_divergences: list[dict[str, Any]] = []
    divergence_kind_counts: dict[str, int] = {}
    executed_cases = 0

    if not dry_run and results_file.is_file():
        seen_ids: set[str] = set()
        with results_file.open("r", encoding="utf-8") as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                res = json.loads(line)
                cid = str(res["case_id"])
                seen_ids.add(cid)
                executed_cases += 1
                exp = expected_meta[cid]
                if res.get("error"):
                    divs = [
                        {
                            "case_id": cid,
                            "family_id": exp.family_id,
                            "selector": exp.selector,
                            "kind": "go_runner_error",
                            "detail": str(res["error"]),
                        }
                    ]
                elif exp.kind == "r04_parse":
                    divs = evaluate_r04_result(exp, res)
                else:
                    divs = evaluate_resolve_result(exp, res)

                if divs:
                    family_stats[exp.family_id]["diverged"] += 1
                    all_divergences.extend(divs)
                    for d in divs:
                        k = str(d.get("kind") or "unknown")
                        divergence_kind_counts[k] = divergence_kind_counts.get(k, 0) + 1
                else:
                    family_stats[exp.family_id]["passed"] += 1

        missing_ids = set(expected_meta.keys()) - seen_ids
        for mid in sorted(missing_ids):
            exp = expected_meta[mid]
            family_stats[exp.family_id]["diverged"] += 1
            div = {
                "case_id": mid,
                "family_id": exp.family_id,
                "selector": exp.selector,
                "kind": "missing_go_result",
                "detail": "Case was not present in gb04-inprocess-results.jsonl",
            }
            all_divergences.append(div)
            divergence_kind_counts["missing_go_result"] = (
                divergence_kind_counts.get("missing_go_result", 0) + 1
            )

    with divergences_file.open("w", encoding="utf-8") as fh:
        for d in all_divergences:
            fh.write(json.dumps(d, ensure_ascii=False, sort_keys=True) + "\n")

    summary: dict[str, Any] = {
        "card": "GB04",
        "lane": "in_process",
        "base_seed": base_seed,
        "platform": plat,
        "families": list(families),
        "limit_generated": limit_generated,
        "dry_run": dry_run,
        "total_cases": len(cases),
        "executed_cases": executed_cases,
        "total_divergences": len(all_divergences),
        "divergence_kind_counts": dict(sorted(divergence_kind_counts.items())),
        "family_stats": family_stats,
        "go_returncode": int(go_run.get("returncode", -1)),
        "verified": go_run.get("verified"),
        "command": go_run.get("command"),
        "elapsed_seconds": round(time.monotonic() - t0, 3),
        "artifacts": {
            "cases_jsonl": str(cases_file),
            "results_jsonl": str(results_file),
            "overlay_json": str(overlay_file),
            "summary_json": str(summary_file),
            "divergences_jsonl": str(divergences_file),
        },
    }
    if go_run.get("returncode", 0) != 0:
        summary["go_stderr_tail"] = str(go_run.get("stderr") or "")[-2000:]
        summary["go_stdout_tail"] = str(go_run.get("stdout") or "")[-2000:]

    summary_file.write_text(json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8")
    return summary


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="GB04 full in-process D1 runner")
    ap.add_argument("--product-root", required=True, help="Verified D1 checkout path")
    ap.add_argument("--out-dir", required=True, help="Output directory for artifacts")
    ap.add_argument("--base-seed", default="20261127", choices=list(BASE_SEEDS))
    ap.add_argument("--platform", default=default_platform(), choices=["darwin", "linux"])
    ap.add_argument(
        "--families",
        default=",".join(INPROCESS_FAMILIES),
        help="Comma-separated families (default: R02,R03,R04,R06,R07)",
    )
    ap.add_argument(
        "--limit-generated",
        type=int,
        default=0,
        help="Cap generated cases per family (0 = full ledger counts 10000/5000/5000)",
    )
    ap.add_argument("--timeout", type=float, default=600.0)
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args(argv)

    families = tuple(f.strip() for f in args.families.split(",") if f.strip())
    summary = run_gb04_inprocess(
        product_root=args.product_root,
        out_dir=args.out_dir,
        base_seed=args.base_seed,
        platform=args.platform,
        families=families,
        limit_generated=args.limit_generated,
        dry_run=args.dry_run,
        timeout=args.timeout,
    )
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if summary["go_returncode"] == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
