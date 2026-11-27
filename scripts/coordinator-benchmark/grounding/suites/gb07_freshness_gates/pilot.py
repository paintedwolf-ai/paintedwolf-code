"""GB07 live D1 sidecar pilot with Decision 012 exact-binary D10 friction tracing."""
from __future__ import annotations

import argparse
import json
import pathlib
import sys
from typing import Any

from grounding.friction_probe import probe_store_db
from grounding.sidecar_driver import (
    IsolatedSidecarProcess,
    ScriptedTurn,
    run_scripted_session,
)


def run_gb07_pilot(
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
    (workspace / "gate.go").write_text(
        "package gate\n\nfunc Verify() bool { return true }\n",
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

    slots: list[dict[str, Any]] = []
    with sidecar as client:
        manifest = sidecar.build_run_manifest()
        plat = "macos" if sidecar.provenance.platform == "darwin" else "linux"
        pid = client.create_project(workspace)["id"]
        trace_log = sidecar.log_path if trace_friction else None

        cases = [
            (
                "F01-01-fresh-read-zero-friction",
                "Read gate.go and report line 3.",
                (
                    ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n gate.go"}},)),
                    ScriptedTurn(content="Fresh read verified at [read#1:3]."),
                ),
                0,
                "",
                0,
                False,
            ),
            (
                "F02-01-re-read-after-mutation-grounded",
                "Read gate.go, check line count, and report line 3.",
                (
                    ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n gate.go"}},)),
                    ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "wc -l gate.go"}},)),
                    ScriptedTurn(content="Verified line 3 [read#1:3] and line count [command#2]."),
                ),
                0,
                "",
                0,
                False,
            ),
            (
                "G01-01-closeout-gate-refusal-and-repair",
                "Read gate.go and report line 3.",
                (
                    ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n gate.go"}},)),
                    ScriptedTurn(content="Claimed [read#99:99]."),
                ),
                1,
                "[cite: c1 => read#1:3]",
                1,
                False,
            ),
            (
                "G02-01-closeout-gate-exhaustion-host-assembles",
                "Read gate.go and report line 3.",
                (
                    ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n gate.go"}},)),
                    ScriptedTurn(content="Claimed [read#99:99]."),
                ),
                3,
                "Still claiming [read#99:99].",
                2,
                True,
            ),
        ]

        for selector, prompt, turns, max_repairs, repair_reply, min_friction, expect_host_assembled in cases:
            sid = client.create_session(pid)["id"]
            client.wait_session_ready(sid)
            before = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
            p = client.send_prompt(sid, prompt)
            art = run_scripted_session(
                client,
                session_id=sid,
                turns=turns,
                db_path=sidecar.store_db_path,
                workspace_dir=workspace,
                submission_id=str(p.get("submission_id") or p.get("id") or ""),
                wait_for_settlement=True,
                max_repair_replies=max_repairs,
                repair_reply_content=repair_reply,
                engine_provenance=sidecar.provenance,
                run_manifest=manifest,
                poll_timeout=90.0,
            )
            after = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
            delta = after.delta_observed(before)
            (out_dir / f"{selector}.json").write_text(
                json.dumps(art.to_dict(), indent=2, sort_keys=True), encoding="utf-8"
            )
            fu = delta.get("friction_units")
            ha = delta.get("host_assembled_closeouts") or 0
            slot_ok = (
                after.is_complete()
                and fu is not None
                and (fu == 0 if min_friction == 0 else fu >= min_friction)
                and (ha >= 1 if expect_host_assembled else ha == 0)
            )
            slots.append(
                {
                    "selector": selector,
                    "passed": slot_ok,
                    "d10_delta": delta,
                    "d10_after_complete": after.is_complete(),
                }
            )

    passed_count = sum(1 for s in slots if s["passed"])
    summary = {
        "card": "GB07",
        "run_id": run_id,
        "platform": plat,
        "trace_friction": trace_friction,
        "slot_count": len(slots),
        "passed_count": passed_count,
        "passed": passed_count == len(slots),
        "run_manifest": manifest.to_dict(),
        "slots": slots,
    }
    (out_dir / "gb07-pilot-summary.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
    )
    return summary


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="GB07 live D1 sidecar pilot")
    ap.add_argument("--engine-bin", required=True)
    ap.add_argument("--product-root", required=True)
    ap.add_argument("--expected-engine-sha256", default="")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--run-id", default="20261127-3bb1464d-gb07-pilot1")
    ap.add_argument("--no-trace-friction", action="store_true")
    args = ap.parse_args(argv)
    summary = run_gb07_pilot(
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
