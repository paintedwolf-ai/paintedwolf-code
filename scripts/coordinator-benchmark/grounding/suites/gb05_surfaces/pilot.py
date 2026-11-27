"""GB05 live D1 sidecar pilot runner with Decision 012 exact-binary D10 friction tracing.

Exercises the first slot of distinct GB05 surfaces across all 3 D5 wordings:
- S01-01-exempt-no-citable-observations (3 wordings): exempt speech, zero-charge control (friction_units == 0)
- S01-03-supported-citation-settles (3 wordings): grounded citation, zero-charge control (friction_units == 0)
- S03-01-valid-citation-repair-replaces-offender (3 wordings): repair loop charges exact friction_units >= 1
- S03-09-retry-exhaustion-host-assembles + S02-01 (confident): exhaustion -> host_assembled with only delivered handles

Runs against exact D1 `lycaon-dev` under Decision 012 GDB/LLDB `recordFrictionLocked` tracing
(`trace_friction=True`) so `d10_before` / `d10_after` capture observed `friction_units`.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import sys
from typing import Any

from grounding.friction_probe import probe_store_db
from grounding.selectors import WORDING_VARIANTS
from grounding.sidecar_driver import (
    IsolatedSidecarProcess,
    ScriptedTurn,
    run_scripted_session,
)
from grounding.suites.gb05_surfaces.fixtures import render_wording


def run_gb05_pilot(
    *,
    engine_bin: str,
    product_root: str,
    expected_engine_sha256: str,
    out_dir: pathlib.Path,
    run_id: str,
    trace_friction: bool = True,
) -> dict[str, Any]:
    out_dir.mkdir(parents=True, exist_ok=True)
    workspace = out_dir / "workspace"
    config_dir = out_dir / "config"
    workspace.mkdir(parents=True, exist_ok=True)
    (workspace / "service.go").write_text(
        "package service\n\nfunc Run() bool { return true }\n",
        encoding="utf-8",
    )

    sidecar = IsolatedSidecarProcess(
        engine_bin,
        product_root=product_root,
        config_dir=config_dir,
        workspace_dir=workspace,
        expected_sha256=expected_engine_sha256,
        trace_friction=trace_friction,
    )

    slots_report: list[dict[str, Any]] = []
    divergences: list[dict[str, Any]] = []

    with sidecar as client:
        manifest = sidecar.build_run_manifest()
        pid = client.create_project(workspace)["id"]
        plat = "macos" if sidecar.provenance.platform == "darwin" else "linux"
        trace_log = sidecar.log_path if trace_friction else None

        def _run_slot(
            selector: str,
            wording: str,
            prompt_text: str,
            turns: tuple[ScriptedTurn, ...],
            *,
            max_repair: int = 0,
            repair_content: str = "",
        ) -> tuple[dict[str, Any], dict[str, Any]]:
            sid = client.create_session(pid)["id"]
            client.wait_session_ready(sid)
            before = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
            p = client.send_prompt(sid, prompt_text)
            art = run_scripted_session(
                client,
                session_id=sid,
                turns=turns,
                db_path=sidecar.store_db_path,
                workspace_dir=workspace,
                submission_id=str(p.get("submission_id") or p.get("id") or ""),
                wait_for_settlement=True,
                max_repair_replies=max_repair,
                repair_reply_content=repair_content,
                engine_provenance=sidecar.provenance,
                run_manifest=manifest,
                poll_timeout=90.0,
            )
            after = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
            (out_dir / f"{selector}__{wording}.json").write_text(
                json.dumps(art.to_dict(), indent=2, sort_keys=True), encoding="utf-8"
            )
            closeout = art.product_closeouts[-1] if art.product_closeouts else {}
            g = (closeout or {}).get("grounding") or {}
            delta = after.delta_observed(before)
            info = {
                "session_id": sid,
                "traced": g.get("traced"),
                "evidence_optional": g.get("evidence_optional"),
                "host_assembled": bool(g.get("host_assembled") is True),
                "retry_count": int(g.get("retry_count") or 0),
                "hint_code": str(g.get("hint_code") or ""),
                "cited_evidence": g.get("cited_evidence") or [],
                "delivered_handles": [r.handle for r in art.delivery_log.records],
                "minted_handles": [r.handle for r in art.product_records],
            }
            return (
                {
                    "selector": selector,
                    "wording": wording,
                    "d10_before": before.to_dict(),
                    "d10_after": after.to_dict(),
                    "d10_delta": delta,
                    "summary": info,
                },
                info,
            )

        # 1. S01-01 Exempt speech (3 D5 wordings, zero-charge control)
        for w in WORDING_VARIANTS:
            rec, info = _run_slot(
                "S01-01-exempt-no-citable-observations",
                w,
                "Reply directly without inspecting files.",
                (ScriptedTurn(content=render_wording("No files were inspected.", w)),),
            )
            f_delta = rec["d10_delta"].get("friction_units")
            passed = (
                info["evidence_optional"] is True
                and len(info["cited_evidence"]) == 0
                and info["retry_count"] == 0
                and (f_delta == 0 if trace_friction else True)
            )
            rec["passed"] = passed
            slots_report.append(rec)
            if not passed:
                divergences.append({"selector": rec["selector"], "wording": w, "detail": info, "d10_delta": rec["d10_delta"]})

        # 2. S01-03 Supported citation settles (3 D5 wordings, zero-charge control)
        for w in WORDING_VARIANTS:
            rec, info = _run_slot(
                "S01-03-supported-citation-settles",
                w,
                "Read service.go and report line 3.",
                (
                    ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n service.go"}},)),
                    ScriptedTurn(content=render_wording("Run() returns true at [read#1:3].", w)),
                ),
            )
            f_delta = rec["d10_delta"].get("friction_units")
            passed = (
                info["traced"] is True
                and info["retry_count"] == 0
                and len(info["cited_evidence"]) >= 1
                and (f_delta == 0 if trace_friction else True)
            )
            rec["passed"] = passed
            slots_report.append(rec)
            if not passed:
                divergences.append({"selector": rec["selector"], "wording": w, "detail": info, "d10_delta": rec["d10_delta"]})

        # 3. S03-01 Valid citation repair replaces offender (3 D5 wordings, exact friction_units >= 1)
        for w in WORDING_VARIANTS:
            rec, info = _run_slot(
                "S03-01-valid-citation-repair-replaces-offender",
                w,
                "Read service.go and cite line 3.",
                (
                    ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n service.go"}},)),
                    ScriptedTurn(content=render_wording("Run() returns true at [read#99:3].", w)),
                ),
                max_repair=2,
                repair_content=render_wording("Run() returns true at [read#1:3].", w),
            )
            f_delta = rec["d10_delta"].get("friction_units")
            passed = (
                info["traced"] is True
                and len(info["cited_evidence"]) >= 1
                and (isinstance(f_delta, int) and f_delta >= 1 if trace_friction else True)
            )
            rec["passed"] = passed
            slots_report.append(rec)
            if not passed:
                divergences.append({"selector": rec["selector"], "wording": w, "detail": info, "d10_delta": rec["d10_delta"]})

        # 4. S03-09 Retry exhaustion -> host_assembled + S02-01 host-attached handles in delivery log
        bad = render_wording("Run() returns true at [read#99:3].", "confident")
        rec, info = _run_slot(
            "S03-09-retry-exhaustion-host-assembles",
            "confident",
            "Read service.go and cite line 3.",
            (
                ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n service.go"}},)),
                ScriptedTurn(content=bad),
            ),
            max_repair=5,
            repair_content=bad,
        )
        f_delta = rec["d10_delta"].get("friction_units")
        delivered_set = set(info["delivered_handles"])
        cited_handles = {c.get("handle") for c in info["cited_evidence"] if isinstance(c, dict) and c.get("handle")}
        all_in_delivery = cited_handles.issubset(delivered_set)
        passed = (
            info["host_assembled"] is True
            and all_in_delivery
            and (isinstance(f_delta, int) and f_delta >= 1 if trace_friction else True)
        )
        rec["passed"] = passed
        slots_report.append(rec)
        if not passed:
            divergences.append({"selector": rec["selector"], "wording": "confident", "detail": info, "d10_delta": rec["d10_delta"]})

    summary = {
        "card": "GB05",
        "run_id": run_id,
        "platform": plat,
        "trace_friction": trace_friction,
        "run_manifest": manifest.to_dict(),
        "slot_count": len(slots_report),
        "passed_count": sum(1 for s in slots_report if s["passed"]),
        "divergences": divergences,
        "slots": slots_report,
    }
    (out_dir / "gb05-pilot-summary.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
    )
    return summary


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="GB05 live D1 sidecar pilot with Decision 012 friction tracing")
    ap.add_argument("--engine-bin", required=True)
    ap.add_argument("--product-root", required=True)
    ap.add_argument("--expected-engine-sha256", default="")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--run-id", default="20261127-3bb1464d-gb05-pilot1")
    ap.add_argument("--no-trace-friction", action="store_true")
    args = ap.parse_args(argv)
    summary = run_gb05_pilot(
        engine_bin=args.engine_bin,
        product_root=args.product_root,
        expected_engine_sha256=args.expected_engine_sha256,
        out_dir=pathlib.Path(args.out_dir),
        run_id=args.run_id,
        trace_friction=not args.no_trace_friction,
    )
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
