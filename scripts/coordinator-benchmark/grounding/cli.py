"""CLI entry point (`python3 -m grounding.cli`) for GB02 selftest, replay, controls, and in-process lane."""
from __future__ import annotations

import argparse
import json
import pathlib
import sys
import tempfile
from typing import Sequence

from .friction_probe import friction_observability_report
from .generators import generate_r06, generate_r07
from .inprocess_driver import D1_EXPECTED_COMMIT, D1_EXPECTED_TREE, InProcessPlan, run_inprocess_plan
from .reference_resolver import Citation, resolve_citation
from .selectors import validate_manifest
from .shrinker import ShrunkArtifact
from .sidecar_driver import (
    IsolatedSidecarProcess,
    ScriptedTurn,
    evaluate_three_ledger_controls,
    run_scripted_session,
)


def _cmd_selftest(_args: argparse.Namespace) -> int:
    summary = validate_manifest()
    r06 = generate_r06("20261127", 0, variation="path_normalization")
    r07 = generate_r07("20261127", 0, mutation="line_outside_range")
    v_ok = resolve_citation(r06.delivery_log, r06.citation, r06.scope)
    v_bad = resolve_citation(r07.delivery_log, r07.citation, r07.scope)
    passed = v_ok.verdict_class == "grounded" and v_bad.verdict_class == "ungrounded"
    out = {
        "passed": passed,
        "manifest": summary,
        "r06_sample_verdict": v_ok.verdict_class,
        "r07_sample_verdict": v_bad.verdict_class,
    }
    print(json.dumps(out, indent=2, sort_keys=True))
    return 0 if passed else 1


def _cmd_replay(args: argparse.Namespace) -> int:
    artifact_path = pathlib.Path(args.artifact)
    artifact = ShrunkArtifact.from_json(artifact_path.read_text(encoding="utf-8"))
    case = artifact.minimized
    verdict = (
        resolve_citation(case.delivery_log, case.citation, case.scope)
        if case.citation is not None
        else None
    )
    out = {
        "artifact_sha256": artifact.sha256,
        "seed": case.seed.to_dict() if case.seed else None,
        "platform": case.scope.platform,
        "record_count": len(case.delivery_log.records),
        "verdict_class": verdict.verdict_class if verdict else None,
        "expected_exact_verdict": verdict.expected_exact_verdict if verdict else None,
        "reason": verdict.reason if verdict else None,
    }
    print(json.dumps(out, indent=2, sort_keys=True))
    return 0


def _cmd_controls(args: argparse.Namespace) -> int:
    out_dir = (
        pathlib.Path(args.out_dir)
        if args.out_dir
        else pathlib.Path(tempfile.mkdtemp(prefix="gb02-controls-"))
    )
    workspace = out_dir / "workspace"
    config_dir = out_dir / "config"
    sidecar = IsolatedSidecarProcess(
        args.engine_bin,
        product_root=args.product_root,
        config_dir=config_dir,
        workspace_dir=workspace,
        expected_sha256=args.expected_engine_sha256,
        config_root=args.config_root or None,
        engine_root=args.engine_root or None,
    )
    if args.dry_run:
        manifest = sidecar.build_run_manifest()
        print(
            json.dumps(
                {
                    "dry_run": True,
                    "command": sidecar.build_command(),
                    "run_manifest": manifest.to_dict(),
                    "engine_provenance": sidecar.provenance.to_dict(),
                    "controls": [
                        "control_1_supported_citation",
                        "control_2_unsupported_citation",
                        "control_3_minted_record_equals_delivery_log",
                    ],
                },
                indent=2,
                sort_keys=True,
            )
        )
        return 0

    workspace.mkdir(parents=True, exist_ok=True)
    (workspace / "sample.go").write_text(
        'package sample\n\nfunc Hello() string { return "ok" }\n',
        encoding="utf-8",
    )

    with sidecar as client:
        manifest = sidecar.build_run_manifest()
        project = client.create_project(workspace)

        # Control 1 + Control 3: Supported citation & minting parity
        sess_sup = client.create_session(project["id"])
        sid_sup = sess_sup["id"]
        client.wait_session_ready(sid_sup)
        prompt_sup = client.send_prompt(sid_sup, "Read sample.go and report line 3.")
        sub_sup = str(prompt_sup.get("submission_id") or prompt_sup.get("id") or "")
        sup_artifact = run_scripted_session(
            client,
            session_id=sid_sup,
            turns=(
                ScriptedTurn(
                    tool_calls=(
                        {"name": "read_file", "args": {"path": "sample.go", "start_line": 1, "end_line": 3}},
                    ),
                ),
                ScriptedTurn(content="Verified Hello() at [read#1:3]."),
            ),
            db_path=sidecar.store_db_path,
            workspace_dir=workspace,
            submission_id=sub_sup,
            wait_for_settlement=True,
            engine_provenance=sidecar.provenance,
            run_manifest=manifest,
        )

        # Control 2: Unsupported / negative control on the same launch path
        sess_unsup = client.create_session(project["id"])
        sid_unsup = sess_unsup["id"]
        client.wait_session_ready(sid_unsup)
        prompt_unsup = client.send_prompt(sid_unsup, "Read sample.go and report line 99.")
        sub_unsup = str(prompt_unsup.get("submission_id") or prompt_unsup.get("id") or "")
        unsup_artifact = run_scripted_session(
            client,
            session_id=sid_unsup,
            turns=(
                ScriptedTurn(
                    tool_calls=(
                        {"name": "read_file", "args": {"path": "sample.go", "start_line": 1, "end_line": 3}},
                    ),
                ),
                ScriptedTurn(content="Claimed unobserved line [read#1:99]."),
            ),
            db_path=sidecar.store_db_path,
            workspace_dir=workspace,
            submission_id=sub_unsup,
            wait_for_settlement=True,
            engine_provenance=sidecar.provenance,
            run_manifest=manifest,
        )

    evaluation = evaluate_three_ledger_controls(
        sup_artifact,
        unsup_artifact,
        supported_citation=Citation(
            citation_type="handle", raw="[read#1:3]", handle="read#1", start_line=3, citation_id="ctrl-1"
        ),
        unsupported_citation=Citation(
            citation_type="handle", raw="[read#1:99]", handle="read#1", start_line=99, citation_id="ctrl-2"
        ),
        platform="macos" if sidecar.provenance.platform == "darwin" else "linux",
    )

    (out_dir / "control-supported-artifact.json").write_text(
        json.dumps(sup_artifact.to_dict(), indent=2, sort_keys=True), encoding="utf-8"
    )
    (out_dir / "control-unsupported-artifact.json").write_text(
        json.dumps(unsup_artifact.to_dict(), indent=2, sort_keys=True), encoding="utf-8"
    )
    (out_dir / "run-manifest.json").write_text(
        json.dumps(manifest.to_dict(), indent=2, sort_keys=True), encoding="utf-8"
    )
    (out_dir / "controls-evaluation.json").write_text(
        json.dumps(evaluation, indent=2, sort_keys=True), encoding="utf-8"
    )
    print(
        json.dumps(
            {
                "passed": evaluation["passed"],
                "out_dir": str(out_dir),
                "run_manifest": manifest.to_dict(),
                "evaluation": evaluation,
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0 if evaluation["passed"] else 1


def _cmd_inprocess(args: argparse.Namespace) -> int:
    product_root = pathlib.Path(args.product_root).resolve()
    plan = InProcessPlan(
        product_root=product_root,
        packages=tuple(args.packages) if args.packages else InProcessPlan(product_root).packages,
        run_pattern=args.run_pattern or "",
        overlay_path=pathlib.Path(args.overlay).resolve() if args.overlay else None,
        expected_commit=args.expected_commit,
        expected_tree=args.expected_tree,
    )
    result = run_inprocess_plan(plan, dry_run=args.dry_run)
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0 if result["returncode"] == 0 else 1


def _cmd_friction_report(_args: argparse.Namespace) -> int:
    print(json.dumps(friction_observability_report(), indent=2, sort_keys=True))
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="python3 -m grounding.cli",
        description="GB02 independent grounding harness CLI",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    p_self = sub.add_parser("selftest", help="Validate selector manifest and synthetic reference self-checks")
    p_self.set_defaults(func=_cmd_selftest)

    p_rep = sub.add_parser("replay", help="Replay a shrunk failing artifact JSON deterministically")
    p_rep.add_argument("artifact", help="Path to ShrunkArtifact JSON file")
    p_rep.set_defaults(func=_cmd_replay)

    p_ctrl = sub.add_parser("controls", help="Run all 3 ledger controls against an explicit D1 --engine-bin and --product-root")
    p_ctrl.add_argument("--engine-bin", required=True, help="Path to exact D1 lycaon-dev binary")
    p_ctrl.add_argument("--product-root", required=True, help="Path to exact D1 product checkout root (Decision 009/010)")
    p_ctrl.add_argument("--config-root", default="", help="Optional override for LYCAON_CONFIG_ROOT inside --product-root")
    p_ctrl.add_argument("--engine-root", default="", help="Optional override for LYCAON_ENGINE_ROOT inside --product-root or D1 artifact dir")
    p_ctrl.add_argument("--expected-engine-sha256", default="", help="Expected SHA-256 digest of the D1 binary (Decision 009)")
    p_ctrl.add_argument("--out-dir", default="", help="Optional output directory for captured artifacts")
    p_ctrl.add_argument("--dry-run", action="store_true", help="Inspect binary provenance and run manifest without launching sidecar")
    p_ctrl.set_defaults(func=_cmd_controls)

    p_inp = sub.add_parser("inprocess", help="Invoke D7-compliant in-process Go tests against verified D1 --product-root")
    p_inp.add_argument("--product-root", required=True, help="Explicit verified D1 product checkout root (Decision 010)")
    p_inp.add_argument("--expected-commit", default=D1_EXPECTED_COMMIT, help="Expected D1 commit OID")
    p_inp.add_argument("--expected-tree", default=D1_EXPECTED_TREE, help="Expected D1 tree OID")
    p_inp.add_argument("--run-pattern", default="", help="Optional go test -run regex")
    p_inp.add_argument("--overlay", default="", help="Optional test-only (*_test.go) Go -overlay JSON path")
    p_inp.add_argument("--packages", nargs="*", default=[], help="Go package patterns to test")
    p_inp.add_argument("--dry-run", action="store_true", help="Verify D1 identity and print command without executing")
    p_inp.set_defaults(func=_cmd_inprocess)

    p_fric = sub.add_parser("friction-report", help="Print D10 friction observability report and D7 proposal")
    p_fric.set_defaults(func=_cmd_friction_report)

    return parser


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(list(argv) if argv is not None else None)
    return int(args.func(args))


if __name__ == "__main__":
    sys.exit(main())
