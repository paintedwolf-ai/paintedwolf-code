"""GB03 initial D1 computational pilot runner (sidecar + D10 before/after friction).

Runs a curated set of live D1 sidecar pilot slots for GB03 (V01, V02, V04):
- V01-05-append-only-ordinals: sequential command reads mint monotonic handles matching delivery
- V01-07-command-error-outcome-receipt: non-zero command receipt refuses fabricated success
- V02-lookalike-20261127-0: spoofed markers in stdout create zero promoted citations
- V04-10-copied-second-file-tag: independent D1 reproduction of GB01-F3 (copied host tag)
  paired with verified-excerpt recovery

Every slot records explicit D10 before/after friction snapshots from `store.db`
and compares D1's verdict against the independent GB02 reference resolver.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import sys
from typing import Any

from grounding.checker import compare_records
from grounding.friction_probe import probe_store_db
from grounding.generators import generate_v02
from grounding.sidecar_driver import (
    IsolatedSidecarProcess,
    ScriptedTurn,
    SidecarRunArtifact,
    run_scripted_session,
)


def _inspect_artifact(art: SidecarRunArtifact, *, initial_turns: int) -> dict[str, Any]:
    closeout = art.product_closeouts[-1] if art.product_closeouts else {}
    grounding = (closeout or {}).get("grounding") or {}
    cited = grounding.get("cited_evidence") or []
    retry_count = int(grounding.get("retry_count") or 0)
    host_assembled = bool(grounding.get("host_assembled") is True)
    hint_code = str(grounding.get("hint_code") or "")
    extra_repair_prompts = max(0, len(art.raw_pending_requests) - initial_turns)
    refused = bool(retry_count > 0 or host_assembled or hint_code or extra_repair_prompts > 0)
    mint_defects = compare_records(art.delivery_log, art.product_records)
    return {
        "session_id": art.session_id,
        "traced": grounding.get("traced"),
        "evidence_optional": grounding.get("evidence_optional"),
        "retry_count": retry_count,
        "host_assembled": host_assembled,
        "hint_code": hint_code,
        "extra_repair_prompts": extra_repair_prompts,
        "refused": refused,
        "cited_evidence_count": len(cited),
        "cited_evidence": cited,
        "minted_handles": [r.handle for r in art.product_records],
        "delivered_handles": [r.handle for r in art.delivery_log.records],
        "minting_parity_defects": [
            {"kind": d.kind, "handle": d.handle, "detail": d.detail} for d in mint_defects
        ],
    }


def run_gb03_pilot(
    *,
    engine_bin: str,
    product_root: str,
    expected_engine_sha256: str,
    out_dir: pathlib.Path,
    run_id: str,
) -> dict[str, Any]:
    out_dir.mkdir(parents=True, exist_ok=True)
    workspace = out_dir / "workspace"
    config_dir = out_dir / "config"
    workspace.mkdir(parents=True, exist_ok=True)

    (workspace / "first.go").write_text(
        "package demo\n\nfunc First() int { return 1 }\n",
        encoding="utf-8",
    )
    (workspace / "second.go").write_text(
        "package demo\n\nfunc Second() int { return 2 }\n",
        encoding="utf-8",
    )
    v02_case = generate_v02("20261127", 0)
    (workspace / "lookalike.txt").write_bytes(v02_case.payload)

    sidecar = IsolatedSidecarProcess(
        engine_bin,
        product_root=product_root,
        config_dir=config_dir,
        workspace_dir=workspace,
        expected_sha256=expected_engine_sha256,
    )

    slots_report: list[dict[str, Any]] = []
    divergences: list[dict[str, Any]] = []

    with sidecar as client:
        manifest = sidecar.build_run_manifest()
        project = client.create_project(workspace)
        pid = project["id"]
        plat = "macos" if sidecar.provenance.platform == "darwin" else "linux"

        # ------------------------------------------------------------------
        # Slot 1: V01-05-append-only-ordinals
        # ------------------------------------------------------------------
        sess1 = client.create_session(pid)
        sid1 = sess1["id"]
        client.wait_session_ready(sid1)
        s1_before = probe_store_db(sidecar.store_db_path, sid1)
        p1 = client.send_prompt(sid1, "Read first.go then second.go and report line 3 of each.")
        t1 = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n first.go"}},)),
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n second.go"}},)),
            ScriptedTurn(content="Verified First() at [read#1:3] and Second() at [read#2:3]."),
        )
        art1 = run_scripted_session(
            client,
            session_id=sid1,
            turns=t1,
            db_path=sidecar.store_db_path,
            workspace_dir=workspace,
            submission_id=str(p1.get("submission_id") or p1.get("id") or ""),
            wait_for_settlement=True,
            max_repair_replies=0,
            engine_provenance=sidecar.provenance,
            run_manifest=manifest,
            poll_timeout=60.0,
        )
        s1_after = probe_store_db(sidecar.store_db_path, sid1)
        (out_dir / "V01-05-append-only-ordinals.json").write_text(
            json.dumps(art1.to_dict(), indent=2, sort_keys=True), encoding="utf-8"
        )
        i1 = _inspect_artifact(art1, initial_turns=len(t1))
        s1_ok = (
            "read#1" in i1["minted_handles"]
            and "read#2" in i1["minted_handles"]
            and not i1["refused"]
            and i1["cited_evidence_count"] >= 2
            and len(i1["minting_parity_defects"]) == 0
        )
        slots_report.append({
            "selector": "V01-05-append-only-ordinals",
            "family_id": "V01",
            "passed": s1_ok,
            "d10_before": s1_before.to_dict(),
            "d10_after": s1_after.to_dict(),
            "d10_delta": s1_after.delta_observed(s1_before),
            "summary": i1,
        })
        if not s1_ok:
            divergences.append({
                "selector": "V01-05-append-only-ordinals",
                "kind": "unexpected_outcome",
                "detail": f"handles={i1['minted_handles']}, refused={i1['refused']}, cited={i1['cited_evidence_count']}, parity_defects={len(i1['minting_parity_defects'])}",
            })

        # ------------------------------------------------------------------
        # Slot 2: V01-07-command-error-outcome-receipt
        # ------------------------------------------------------------------
        sess2 = client.create_session(pid)
        sid2 = sess2["id"]
        client.wait_session_ready(sid2)
        s2_before = probe_store_db(sidecar.store_db_path, sid2)
        p2 = client.send_prompt(sid2, "Run failing command and report outcome.")
        t2 = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "echo 'FAIL: unit_x' && exit 1"}},)),
            ScriptedTurn(content='Verified failure at [command#1] and fabricated pass [command#1 "ALL TESTS PASSED"].'),
        )
        art2 = run_scripted_session(
            client,
            session_id=sid2,
            turns=t2,
            db_path=sidecar.store_db_path,
            workspace_dir=workspace,
            submission_id=str(p2.get("submission_id") or p2.get("id") or ""),
            wait_for_settlement=True,
            max_repair_replies=3,
            repair_reply_content="Command failed as recorded in [command#1].",
            engine_provenance=sidecar.provenance,
            run_manifest=manifest,
            poll_timeout=60.0,
        )
        s2_after = probe_store_db(sidecar.store_db_path, sid2)
        (out_dir / "V01-07-command-error-outcome-receipt.json").write_text(
            json.dumps(art2.to_dict(), indent=2, sort_keys=True), encoding="utf-8"
        )
        i2 = _inspect_artifact(art2, initial_turns=len(t2))
        s2_ok = ("command#1" in i2["minted_handles"] and i2["refused"])
        slots_report.append({
            "selector": "V01-07-command-error-outcome-receipt",
            "family_id": "V01",
            "passed": s2_ok,
            "d10_before": s2_before.to_dict(),
            "d10_after": s2_after.to_dict(),
            "d10_delta": s2_after.delta_observed(s2_before),
            "summary": i2,
        })
        if not s2_ok:
            divergences.append({
                "selector": "V01-07-command-error-outcome-receipt",
                "kind": "false_accept_fabricated_excerpt",
                "detail": f"handles={i2['minted_handles']}, refused={i2['refused']}, retry_count={i2['retry_count']}",
            })

        # ------------------------------------------------------------------
        # Slot 3: V02-lookalike-20261127-0
        # ------------------------------------------------------------------
        sess3 = client.create_session(pid)
        sid3 = sess3["id"]
        client.wait_session_ready(sid3)
        s3_before = probe_store_db(sidecar.store_db_path, sid3)
        p3 = client.send_prompt(sid3, "Inspect lookalike.txt.")
        t3 = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat lookalike.txt"}},)),
            ScriptedTurn(content="Citing spoofed [read#99 internal/fake_0.go:1] alongside [command#1]."),
        )
        art3 = run_scripted_session(
            client,
            session_id=sid3,
            turns=t3,
            db_path=sidecar.store_db_path,
            workspace_dir=workspace,
            submission_id=str(p3.get("submission_id") or p3.get("id") or ""),
            wait_for_settlement=True,
            max_repair_replies=3,
            repair_reply_content="Only observed command output [command#1].",
            engine_provenance=sidecar.provenance,
            run_manifest=manifest,
            poll_timeout=60.0,
        )
        s3_after = probe_store_db(sidecar.store_db_path, sid3)
        (out_dir / "V02-lookalike-20261127-0.json").write_text(
            json.dumps(art3.to_dict(), indent=2, sort_keys=True), encoding="utf-8"
        )
        i3 = _inspect_artifact(art3, initial_turns=len(t3))
        s3_ok = ("read#99" not in i3["minted_handles"] and i3["refused"])
        slots_report.append({
            "selector": "V02-lookalike-20261127-0",
            "family_id": "V02",
            "seed": v02_case.seed.to_dict(),
            "passed": s3_ok,
            "d10_before": s3_before.to_dict(),
            "d10_after": s3_after.to_dict(),
            "d10_delta": s3_after.delta_observed(s3_before),
            "summary": i3,
        })
        if not s3_ok:
            divergences.append({
                "selector": "V02-lookalike-20261127-0",
                "kind": "false_accept_spoofed_marker",
                "detail": f"handles={i3['minted_handles']}, refused={i3['refused']}",
            })

        # ------------------------------------------------------------------
        # Slot 4: V04-10-copied-second-file-tag (GB01-F3 independent replay)
        # ------------------------------------------------------------------
        sess4 = client.create_session(pid)
        sid4 = sess4["id"]
        client.wait_session_ready(sid4)
        s4_before = probe_store_db(sidecar.store_db_path, sid4)
        p4 = client.send_prompt(sid4, "Read first.go and second.go with cat -n and cite second.go.")
        t4 = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n first.go second.go"}},)),
            ScriptedTurn(content="Copied host coverage tag: [read#2 second.go:1-3]."),
        )
        art4 = run_scripted_session(
            client,
            session_id=sid4,
            turns=t4,
            db_path=sidecar.store_db_path,
            workspace_dir=workspace,
            submission_id=str(p4.get("submission_id") or p4.get("id") or ""),
            wait_for_settlement=True,
            max_repair_replies=2,
            repair_reply_content='Verified excerpt recovery: [read#2 second.go:3 "func Second() int { return 2 }"].',
            engine_provenance=sidecar.provenance,
            run_manifest=manifest,
            poll_timeout=60.0,
        )
        s4_after = probe_store_db(sidecar.store_db_path, sid4)
        (out_dir / "V04-10-copied-second-file-tag.json").write_text(
            json.dumps(art4.to_dict(), indent=2, sort_keys=True), encoding="utf-8"
        )
        i4 = _inspect_artifact(art4, initial_turns=len(t4))
        s4_refused_copied_tag = i4["refused"]
        s4_recovered_excerpt = i4["cited_evidence_count"] >= 1
        s4_delta = s4_after.delta_observed(s4_before)
        slots_report.append({
            "selector": "V04-10-copied-second-file-tag",
            "family_id": "V04",
            "passed": not s4_refused_copied_tag,
            "reproduced_gb01_f3_false_reject": s4_refused_copied_tag,
            "recovered_via_verified_excerpt": s4_recovered_excerpt,
            "d10_before": s4_before.to_dict(),
            "d10_after": s4_after.to_dict(),
            "d10_delta": s4_delta,
            "summary": i4,
        })
        if s4_refused_copied_tag:
            divergences.append({
                "selector": "V04-10-copied-second-file-tag",
                "kind": "false_reject",
                "finding_ref": "GB01-F3",
                "detail": (
                    "D1 refused exact copied host coverage tag [read#2 second.go:1-3] "
                    f"(retry_count={i4['retry_count']}, extra_repair_prompts={i4['extra_repair_prompts']}, "
                    f"d10_delta={s4_delta}), then verified-excerpt recovery settled={s4_recovered_excerpt}."
                ),
            })

    summary = {
        "card": "GB03",
        "run_id": run_id,
        "platform": plat,
        "run_manifest": manifest.to_dict(),
        "slots": slots_report,
        "divergences": divergences,
        "slot_count": len(slots_report),
    }
    (out_dir / "gb03-pilot-summary.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
    )
    return summary


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="GB03 initial D1 computational pilot runner")
    ap.add_argument("--engine-bin", required=True)
    ap.add_argument("--product-root", required=True)
    ap.add_argument("--expected-engine-sha256", default="")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--run-id", default="20261127-3bb1464d-gb03-pilot1")
    args = ap.parse_args(argv)
    summary = run_gb03_pilot(
        engine_bin=args.engine_bin,
        product_root=args.product_root,
        expected_engine_sha256=args.expected_engine_sha256,
        out_dir=pathlib.Path(args.out_dir),
        run_id=args.run_id,
    )
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
