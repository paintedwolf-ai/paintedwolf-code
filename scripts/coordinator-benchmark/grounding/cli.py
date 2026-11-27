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
from .inprocess_driver import InProcessPlan, run_inprocess_plan
from .reference_resolver import resolve_citation
from .selectors import validate_manifest
from .shrinker import ShrunkArtifact
from .sidecar_driver import (
    IsolatedSidecarProcess,
    ScriptedTurn,
    inspect_engine_binary,
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
    prov = inspect_engine_binary(args.engine_bin, expected_sha256=args.expected_engine_sha256)
    if args.dry_run:
        print(
            json.dumps(
                {
                    "dry_run": True,
                    "engine_provenance": prov.to_dict(),
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

    out_dir = pathlib.Path(args.out_dir) if args.out_dir else pathlib.Path(tempfile.mkdtemp(prefix="gb02-controls-"))
    workspace = out_dir / "workspace"
    config_dir = out_dir / "config"
    workspace.mkdir(parents=True, exist_ok=True)
    (workspace / "sample.go").write_text("package sample\n\nfunc Hello() string { return \"ok\" }\n", encoding="utf-8")

    with IsolatedSidecarProcess(
        args.engine_bin,
        config_dir=config_dir,
        workspace_dir=workspace,
        expected_sha256=args.expected_engine_sha256,
    ) as client:
        project = client.create_project(workspace)
        session = client.create_session(project["id"])
        sid = session["id"]
        client.send_prompt(sid, "Read sample.go and report line 3.")
        artifact = run_scripted_session(
            client,
            session_id=sid,
            turns=(
                ScriptedTurn(
                    tool_calls=({"name": "read_file", "args": {"path": "sample.go", "start_line": 1, "end_line": 3}},),
                ),
                ScriptedTurn(content="Verified Hello() at [read#1:3]."),
            ),
            db_path=config_dir / "store.db",
            engine_provenance=prov,
        )
    out_file = out_dir / "controls-artifact.json"
    out_file.write_text(json.dumps(artifact.to_dict(), indent=2, sort_keys=True), encoding="utf-8")
    print(json.dumps({"artifact_path": str(out_file), "session_id": sid, "engine_provenance": prov.to_dict()}, indent=2, sort_keys=True))
    return 0


def _cmd_inprocess(args: argparse.Namespace) -> int:
    repo_root = pathlib.Path(args.repo_root).resolve()
    plan = InProcessPlan(
        repo_root=repo_root,
        packages=tuple(args.packages) if args.packages else InProcessPlan(repo_root).packages,
        run_pattern=args.run_pattern or "",
        overlay_path=pathlib.Path(args.overlay).resolve() if args.overlay else None,
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

    p_ctrl = sub.add_parser("controls", help="Run sidecar controls against an explicit D1 --engine-bin")
    p_ctrl.add_argument("--engine-bin", required=True, help="Path to exact D1 lycaon-dev binary")
    p_ctrl.add_argument("--expected-engine-sha256", default="", help="Expected SHA-256 digest of the D1 binary (Decision 009)")
    p_ctrl.add_argument("--out-dir", default="", help="Optional output directory for captured artifacts")
    p_ctrl.add_argument("--dry-run", action="store_true", help="Inspect binary provenance without launching sidecar")
    p_ctrl.set_defaults(func=_cmd_controls)

    p_inp = sub.add_parser("inprocess", help="Invoke D7-compliant in-process Go tests (no shipped binary edits)")
    p_inp.add_argument("--repo-root", default=str(pathlib.Path(__file__).resolve().parents[3]), help="Repository root")
    p_inp.add_argument("--run-pattern", default="", help="Optional go test -run regex")
    p_inp.add_argument("--overlay", default="", help="Optional Go -overlay JSON path")
    p_inp.add_argument("--packages", nargs="*", default=[], help="Go package patterns to test")
    p_inp.add_argument("--dry-run", action="store_true", help="Print command without executing")
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
