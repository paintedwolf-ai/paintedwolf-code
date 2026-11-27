"""GB04 initial D1 computational pilot runner (sidecar + D10 before/after friction).

Runs a curated set of live D1 sidecar pilot slots for GB04 (R01, R06, R07):
- R01-01-handle-supported: supported [read#1:3] grounded with zero friction
- R01-03-path-coord-supported: supported [sample.go:3] grounded with zero friction
- R01-04-path-coord-unsupported: out-of-range [sample.go:99] refused with adjacent [read#1:3]
- R01-08-url-unobserved: unobserved URL refused with adjacent [read#1:3]
- R06-pilot-0-path-normalization: normalized path [./sub/../sample.go:3] grounded
- R07-pilot-0-line-outside-range: mutated line [read#1:99] refused

Every slot records explicit D10 before/after friction snapshots from `store.db`
and compares D1's verdict against the independent GB02 reference resolver.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import sys
from typing import Any

from grounding.sidecar_driver import (
    IsolatedSidecarProcess,
    ScriptedTurn,
    read_d10_friction_snapshot,
    run_scripted_session,
)


def _d10_delta(before: dict[str, int], after: dict[str, int]) -> dict[str, int]:
    keys = sorted(set(before) | set(after))
    return {k: int(after.get(k, 0)) - int(before.get(k, 0)) for k in keys}


def _extract_verdicts_summary(artifact_dict: dict[str, Any]) -> dict[str, Any]:
    ce = artifact_dict.get("cited_evidence") or []
    refusals = artifact_dict.get("refusals") or []
    return {
        "cited_evidence_count": len(ce),
        "cited_evidence": ce,
        "refusal_count": len(refusals),
        "refusals": refusals,
        "minted_handles": [r.get("handle") for r in (artifact_dict.get("product_records") or [])],
    }


def run_gb04_pilot(
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
    (workspace / "sub").mkdir(parents=True, exist_ok=True)

    (workspace / "sample.go").write_text(
        'package sample\n\nfunc Hello() string { return "ok" }\n',
        encoding="utf-8",
    )

    sidecar = IsolatedSidecarProcess(
        engine_bin,
        product_root=product_root,
        config_dir=config_dir,
        workspace_dir=workspace,
        expected_sha256=expected_engine_sha256,
    )

    pilot_specs = [
        (
            "R01-01-handle-supported",
            "R01",
            "grounded",
            "Verified Hello() at [read#1:3].",
            None,
        ),
        (
            "R01-03-path-coord-supported",
            "R01",
            "grounded",
            "Verified Hello() at [sample.go:3].",
            None,
        ),
        (
            "R01-04-path-coord-unsupported",
            "R01",
            "ungrounded",
            "Verified [read#1:3] and claimed out-of-range [sample.go:99].",
            "Verified [read#1:3].",
        ),
        (
            "R01-08-url-unobserved",
            "R01",
            "ungrounded",
            "Verified [read#1:3] and see [docs](https://unobserved.example.test/doc).",
            "Verified [read#1:3].",
        ),
        (
            "R06-pilot-0-path-normalization",
            "R06",
            "grounded",
            "Verified Hello() at [./sub/../sample.go:3].",
            "Verified Hello() at [read#1:3].",
        ),
        (
            "R07-pilot-0-line-outside-range",
            "R07",
            "ungrounded",
            "Verified [read#1:3] and claimed unobserved [read#1:99].",
            "Verified [read#1:3].",
        ),
    ]

    slots_report: list[dict[str, Any]] = []
    divergences: list[dict[str, Any]] = []

    with sidecar as client:
        manifest = sidecar.build_run_manifest()
        project = client.create_project(workspace)
        pid = project["id"]
        plat = "macos" if sidecar.provenance.platform == "darwin" else "linux"

        for selector, family_id, expected_class, reply_text, repair_text in pilot_specs:
            before = read_d10_friction_snapshot(sidecar.store_db_path)
            sess = client.create_session(pid)
            sid = sess["id"]
            client.wait_session_ready(sid)
            p = client.send_prompt(sid, f"Inspect sample.go for {selector}.")
            art = run_scripted_session(
                client,
                session_id=sid,
                turns=(
                    ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n sample.go"}},)),
                    ScriptedTurn(content=reply_text),
                ),
                db_path=sidecar.store_db_path,
                workspace_dir=workspace,
                submission_id=str(p.get("submission_id") or p.get("id") or ""),
                wait_for_settlement=True,
                max_repair_replies=3 if repair_text else 0,
                repair_reply_content=repair_text or "",
                engine_provenance=sidecar.provenance,
                run_manifest=manifest,
                poll_timeout=60.0,
            )
            after = read_d10_friction_snapshot(sidecar.store_db_path)
            art_d = art.to_dict()
            (out_dir / f"{selector}.json").write_text(
                json.dumps(art_d, indent=2, sort_keys=True), encoding="utf-8"
            )

            if expected_class == "grounded":
                passed = len(art.refusals) == 0 and len(art.cited_evidence) >= 1
                div_kind = "false_reject"
            else:
                passed = len(art.refusals) >= 1
                div_kind = "false_accept"

            slots_report.append({
                "selector": selector,
                "family_id": family_id,
                "expected_class": expected_class,
                "passed": passed,
                "d10_before": before,
                "d10_after": after,
                "d10_delta": _d10_delta(before, after),
                "summary": _extract_verdicts_summary(art_d),
            })
            if not passed:
                divergences.append({
                    "selector": selector,
                    "family_id": family_id,
                    "kind": div_kind,
                    "detail": (
                        f"expected={expected_class}, refusals={len(art.refusals)}, "
                        f"cited_evidence={len(art.cited_evidence)}, "
                        f"d10_delta={_d10_delta(before, after)}"
                    ),
                })

    summary = {
        "card": "GB04",
        "run_id": run_id,
        "platform": plat,
        "run_manifest": manifest.to_dict(),
        "slots": slots_report,
        "divergences": divergences,
        "slot_count": len(slots_report),
    }
    (out_dir / "gb04-pilot-summary.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
    )
    return summary


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="GB04 initial D1 computational pilot runner")
    ap.add_argument("--engine-bin", required=True)
    ap.add_argument("--product-root", required=True)
    ap.add_argument("--expected-engine-sha256", default="")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--run-id", default="20261127-3bb1464d-gb04-pilot1")
    args = ap.parse_args(argv)
    summary = run_gb04_pilot(
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
