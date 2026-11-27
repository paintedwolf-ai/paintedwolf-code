"""CLI entry point for CF02 checker controls, live controls, and seeded replay."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys
import tempfile
from typing import Sequence

from .change_runner import generate_change_script
from .checker_types import Verdict
from .controls import run_all_controls, run_broken_reference_proof
from .driver_provenance import collect_run_provenance
from .driver_sidecar import HTTPTransport, SidecarDriver
from .driver_sse import LiveSSESubscriber
from .generators import generate_tree_spec, materialize_tree
from .live_controls import run_three_live_controls
from .queries import S01_QUERY_FORMS, generate_s01_query
from .seeds import BASE_SEED_PRIMARY


def _cmd_controls(args: argparse.Namespace) -> int:
    report = run_all_controls()
    with tempfile.TemporaryDirectory() as tmp:
        proof = run_broken_reference_proof(Path(tmp), rg_bin=args.rg_bin)

    proof_ok = (
        proof.initial_verdict == Verdict.DEFECT
        and proof.replayed_verdict == Verdict.DEFECT
        and proof.shrink_result.shrunk_entry_count == 1
    )
    payload = {
        "controls": report.to_dict(),
        "broken_reference_proof": {
            "passed": proof_ok,
            "initial_verdict": proof.initial_verdict.value,
            "replayed_verdict": proof.replayed_verdict.value,
            "original_entry_count": proof.shrink_result.original_entry_count,
            "shrunk_entry_count": proof.shrink_result.shrunk_entry_count,
            "rg_version": proof.rg_version,
            "replay_cli": " ".join(proof.shrink_result.shrunk.selector.cli_args()),
        },
        "all_passed": report.all_passed and proof_ok,
    }
    if args.out_dir:
        out = Path(args.out_dir)
        out.mkdir(parents=True, exist_ok=True)
        out_file = out / "checker_controls.json"
        out_file.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(payload, indent=2, sort_keys=True))
    return 0 if payload["all_passed"] else 1


def _cmd_replay(args: argparse.Namespace) -> int:
    tree_spec = generate_tree_spec(args.seed, args.family, args.instance)
    change_script = generate_change_script(args.seed, args.family, args.instance, tree_spec.file_paths())
    form = S01_QUERY_FORMS[args.instance % len(S01_QUERY_FORMS)]
    query_spec = generate_s01_query(args.seed, args.family, args.instance, form, paths=tree_spec.file_paths())

    if args.out_dir:
        target = Path(args.out_dir)
        manifest = materialize_tree(tree_spec, target)
    else:
        with tempfile.TemporaryDirectory() as tmp:
            manifest = materialize_tree(tree_spec, Path(tmp))

    payload = {
        "selector": tree_spec.selector.to_dict(),
        "manifest_digest": manifest.digest(),
        "entry_count": len(manifest.entries),
        "change_op_count": len(change_script.ops),
        "query": query_spec.to_dict(),
    }
    print(json.dumps(payload, indent=2, sort_keys=True))
    return 0


def _cmd_live_controls(args: argparse.Namespace) -> int:
    """Run the 3 runbook live controls against a live sidecar using a real /v1/events SSE stream."""
    transport = HTTPTransport(args.url, args.token)
    driver = SidecarDriver(transport)
    repo_root = Path(args.repo_root).resolve() if args.repo_root else Path(__file__).resolve().parents[3]
    out_dir = Path(args.out_dir).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)

    sse_sub = LiveSSESubscriber(args.url, args.token, out_jsonl=out_dir / "sse_events.jsonl")
    with tempfile.TemporaryDirectory() as tmp:
        work_root = Path(tmp) / "fixture"
        provenance = collect_run_provenance(
            harness_repo=repo_root,
            work_dir=Path(tmp),
            binary_path=Path(args.engine_bin) if args.engine_bin else None,
            d1_commit=args.d1_commit,
            d1_tree=args.d1_tree,
            rg_bin=args.rg_bin,
        )
        report = run_three_live_controls(
            driver,
            work_root,
            sse_sub,
            provenance=provenance,
            out_dir=out_dir,
            rg_bin=args.rg_bin,
        )
    print(json.dumps(report.to_dict(), indent=2, sort_keys=True))
    return 0 if report.all_passed else 1


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="catalog_feed_search")
    sub = parser.add_subparsers(dest="command", required=True)

    p_ctrl = sub.add_parser("controls", help="Run all checker controls and broken-reference proof")
    p_ctrl.add_argument("--out-dir", default="", help="Optional directory for JSON output")
    p_ctrl.add_argument("--rg-bin", default="rg", help="Path to ripgrep 15.x executable")
    p_ctrl.set_defaults(func=_cmd_controls)

    p_rep = sub.add_parser("replay", help="Deterministically replay a seeded campaign instance")
    p_rep.add_argument("--seed", default=BASE_SEED_PRIMARY, help="Base seed (e.g. 20261127)")
    p_rep.add_argument("--family", required=True, help="Family ID (e.g. K01, F01, S01)")
    p_rep.add_argument("--instance", type=int, required=True, help="Instance index (0-based)")
    p_rep.add_argument("--out-dir", default="", help="Optional target directory to materialize tree")
    p_rep.set_defaults(func=_cmd_replay)

    p_live = sub.add_parser("live-controls", help="Run the 3 runbook live controls against a live sidecar")
    p_live.add_argument("--url", required=True, help="Sidecar base URL (e.g. http://127.0.0.1:PORT)")
    p_live.add_argument("--token", required=True, help="Sidecar bearer auth token")
    p_live.add_argument("--engine-bin", default="", help="Path to D1 product binary for provenance")
    p_live.add_argument("--d1-commit", default="", help="Pinned D1 source commit SHA")
    p_live.add_argument("--d1-tree", default="", help="Pinned D1 source tree OID")
    p_live.add_argument("--out-dir", required=True, help="Output directory for evidence & manifest")
    p_live.add_argument("--rg-bin", default="rg", help="Path to ripgrep 15.x executable")
    p_live.add_argument("--repo-root", default="", help="Harness repo root for git provenance")
    p_live.set_defaults(func=_cmd_live_controls)

    return parser


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    return int(args.func(args))


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
