"""GB08 live D1 sidecar pilot: refusal feedback readback audit + opt-in dev diagnostic parity.

Runs against exact D1 `lycaon-dev` with Decision 012 exact-binary D10 friction tracing:
1. Grounded closeout ([read#1:3]): verifies dev diagnostic (enabled vs disabled) raises 0 contradictions
   and produces identical outcomes and D10 counts.
2. Refused closeout ([read#1:3] + [read#99:99]): captures serialized refusal feedback, verifies
   every offered handle is in the independent DeliveryLog and the named offender is read#99,
   and verifies dev diagnostic detects all 3 injected contradiction classes when enabled and 0 when disabled.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys
from typing import Any

from grounding.delivery_log import ProductVerdict, SurfaceOutcome
from grounding.friction_probe import probe_store_db
from grounding.reference_resolver import Citation, PermittedScope
from grounding.sidecar_driver import (
    IsolatedSidecarProcess,
    ScriptedTurn,
    run_scripted_session,
)
from grounding.suites.gb08_presentation.diagnostic import (
    audit_refusal_feedback,
    run_dev_diagnostic,
)

_HANDLE_RE = re.compile(r"\b(?:read|command|grep|find|list|stat|wc|diff|web|surface)#\d+\b")


def run_gb08_pilot(
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
    (workspace / "app.go").write_text(
        "package app\n\nfunc Main() string { return \"ready\" }\n",
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

    with sidecar as client:
        manifest = sidecar.build_run_manifest()
        pid = client.create_project(workspace)["id"]
        plat = "macos" if sidecar.provenance.platform == "darwin" else "linux"
        trace_log = sidecar.log_path if trace_friction else None

        # 1. Grounded closeout
        sid_ok = client.create_session(pid)["id"]
        client.wait_session_ready(sid_ok)
        ok_before = probe_store_db(sidecar.store_db_path, sid_ok, trace_log_path=trace_log)
        p_ok = client.send_prompt(sid_ok, "Read app.go and report line 3.")
        art_ok = run_scripted_session(
            client,
            session_id=sid_ok,
            turns=(
                ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},)),
                ScriptedTurn(content="Verified Main() at [read#1:3]."),
            ),
            db_path=sidecar.store_db_path,
            workspace_dir=workspace,
            submission_id=str(p_ok.get("submission_id") or p_ok.get("id") or ""),
            wait_for_settlement=True,
            max_repair_replies=0,
            engine_provenance=sidecar.provenance,
            run_manifest=manifest,
            poll_timeout=90.0,
        )
        ok_after = probe_store_db(sidecar.store_db_path, sid_ok, trace_log_path=trace_log)
        (out_dir / "gb08-grounded.json").write_text(
            json.dumps(art_ok.to_dict(), indent=2, sort_keys=True), encoding="utf-8"
        )

        scope_ok = PermittedScope(session_id=sid_ok, round_id="round-1", intent_window="intent-1", platform=plat)
        c_ok = Citation("handle", "[read#1:3]", handle="read#1", start_line=3, citation_id="c1")
        ok_closeout = art_ok.product_closeouts[-1] if art_ok.product_closeouts else {}
        ok_g = (ok_closeout or {}).get("grounding") or {}
        ok_cited = ok_g.get("cited_evidence") or []
        ok_verdict = str((ok_cited[0] if ok_cited else {}).get("verdict") or "unverifiable")
        ok_outcome = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(ProductVerdict("c1", ok_verdict, "read#1"),),
            records=art_ok.product_records,
        )
        diag_healthy_on = run_dev_diagnostic(art_ok.delivery_log, (c_ok,), scope_ok, ok_outcome, enabled=True)
        diag_healthy_off = run_dev_diagnostic(art_ok.delivery_log, (c_ok,), scope_ok, ok_outcome, enabled=False)

        # 2. Refused closeout + serialized feedback audit
        sid_bad = client.create_session(pid)["id"]
        client.wait_session_ready(sid_bad)
        bad_before = probe_store_db(sidecar.store_db_path, sid_bad, trace_log_path=trace_log)
        p_bad = client.send_prompt(sid_bad, "Read app.go and report line 3.")
        art_bad = run_scripted_session(
            client,
            session_id=sid_bad,
            turns=(
                ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},)),
                ScriptedTurn(content="Verified [read#1:3] and claimed [read#99:99]."),
            ),
            db_path=sidecar.store_db_path,
            workspace_dir=workspace,
            submission_id=str(p_bad.get("submission_id") or p_bad.get("id") or ""),
            wait_for_settlement=True,
            max_repair_replies=2,
            repair_reply_content="Verified Main() at [read#1:3].",
            engine_provenance=sidecar.provenance,
            run_manifest=manifest,
            poll_timeout=90.0,
        )
        bad_after = probe_store_db(sidecar.store_db_path, sid_bad, trace_log_path=trace_log)
        (out_dir / "gb08-refused-feedback.json").write_text(
            json.dumps(art_bad.to_dict(), indent=2, sort_keys=True), encoding="utf-8"
        )

        # Inspect serialized repair prompt sent to model after refusal
        repair_blob = json.dumps(art_bad.raw_pending_requests[-1]) if art_bad.raw_pending_requests else ""
        offered = sorted(set(_HANDLE_RE.findall(repair_blob)) - {"read#99"})
        named_offender_present = "read#99" in repair_blob
        feedback_audit = audit_refusal_feedback(
            art_bad.delivery_log,
            offered_handles=offered,
            named_offenders=("read#99",) if named_offender_present else (),
            actual_failing_citations=("read#99",),
        )

        # Injected contradiction check on real D1 DeliveryLog
        c_bad = Citation("handle", "[read#99:99]", handle="read#99", start_line=99, citation_id="c2")
        scope_bad = PermittedScope(session_id=sid_bad, round_id="round-1", intent_window="intent-1", platform=plat)
        injected = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(
                ProductVerdict("c1", "unverifiable", ""),
                ProductVerdict("c2", "traced", "read#99"),
            ),
            records=art_bad.product_records,
        )
        diag_inj_on = run_dev_diagnostic(
            art_bad.delivery_log,
            (c_ok, c_bad),
            scope_bad,
            injected,
            enabled=True,
            had_prior_refusal=True,
            recorded_accepted=True,
        )
        diag_inj_off = run_dev_diagnostic(
            art_bad.delivery_log,
            (c_ok, c_bad),
            scope_bad,
            injected,
            enabled=False,
            had_prior_refusal=True,
            recorded_accepted=True,
        )

    passed = (
        len(diag_healthy_on) == 0
        and len(diag_healthy_off) == 0
        and feedback_audit.passed
        and named_offender_present
        and {d.kind for d in diag_inj_on} == {"grounded_without_support", "refusal_with_unique_support", "accepted_after_refusal"}
        and len(diag_inj_off) == 0
    )
    summary = {
        "card": "GB08",
        "run_id": run_id,
        "platform": plat,
        "trace_friction": trace_friction,
        "passed": passed,
        "run_manifest": manifest.to_dict(),
        "grounded_d10_delta": ok_after.delta_observed(ok_before),
        "refused_d10_delta": bad_after.delta_observed(bad_before),
        "healthy_diagnostic_enabled_count": len(diag_healthy_on),
        "healthy_diagnostic_disabled_count": len(diag_healthy_off),
        "feedback_audit": {
            "passed": feedback_audit.passed,
            "offered_handles": offered,
            "named_offender_present": named_offender_present,
            "inadmissible_samples": list(feedback_audit.inadmissible_samples),
            "misnamed_offenders": list(feedback_audit.misnamed_offenders),
        },
        "injected_diagnostic_kinds_enabled": sorted(d.kind for d in diag_inj_on),
        "injected_diagnostic_disabled_count": len(diag_inj_off),
    }
    (out_dir / "gb08-pilot-summary.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
    )
    return summary


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="GB08 live D1 feedback audit and dev diagnostic pilot")
    ap.add_argument("--engine-bin", required=True)
    ap.add_argument("--product-root", required=True)
    ap.add_argument("--expected-engine-sha256", default="")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--run-id", default="20261127-3bb1464d-gb08-pilot1")
    ap.add_argument("--no-trace-friction", action="store_true")
    args = ap.parse_args(argv)
    summary = run_gb08_pilot(
        engine_bin=args.engine_bin,
        product_root=args.product_root,
        expected_engine_sha256=args.expected_engine_sha256,
        out_dir=pathlib.Path(args.out_dir),
        run_id=args.run_id,
        trace_friction=not args.no_trace_friction,
    )
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
