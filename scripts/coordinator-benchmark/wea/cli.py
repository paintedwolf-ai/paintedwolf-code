"""CLI entry point for the TB02 WEA shared driver, controls, checker, and replay."""

from __future__ import annotations

import argparse
import json
import pathlib
import sys
from typing import Sequence

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea.checker import check_evidence
from wea.controls import run_qualification_controls
from wea.native_den_macos import run_macos_native_den
from wea.provenance import REPO_ROOT, collect_provenance
from wea.replay import replay_on_fresh_profile
from wea.runner import run_track_control


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="wea",
        description="TB02 shared driver, independent observers, checker, and replay for WEA bug bash.",
    )
    parser.add_argument("--engine", type=pathlib.Path, default=None, help="Path to pinned product lycaon-dev binary.")
    parser.add_argument("--engine-manifest", type=pathlib.Path, default=None, help="Path to engine build manifest JSON.")

    sub = parser.add_subparsers(dest="command", required=True)

    p_ctrl = sub.add_parser("controls", help="Run all 12 checker qualification controls + missing-instrumentation check.")
    p_ctrl.add_argument("--out", type=pathlib.Path, default=None, help="Optional output path for JSON report.")

    p_run = sub.add_parser("run", help="Run per-track sidecar control(s) against the real engine.")
    p_run.add_argument("--track", choices=("workers", "edits", "authority", "all"), required=True)
    p_run.add_argument("--out", type=pathlib.Path, required=True)
    p_run.add_argument("--export", type=pathlib.Path, default=None, help="Export sanitized replay bundle to this dir.")

    p_rep = sub.add_parser("replay", help="Replay a sanitized WEA fixture bundle on a fresh isolated profile.")
    p_rep.add_argument("--bundle", type=pathlib.Path, required=True)
    p_rep.add_argument("--out", type=pathlib.Path, required=True)

    p_chk = sub.add_parser("check", help="Run the deterministic WEA checker against an evidence JSON file.")
    p_chk.add_argument("--evidence", type=pathlib.Path, required=True)

    p_mac = sub.add_parser("native-den-macos", help="Run macOS D12 native Den keyboard-navigation check.")
    p_mac.add_argument("--out", type=pathlib.Path, required=True)
    p_mac.add_argument("--api-url", required=True)
    p_mac.add_argument("--token", required=True)
    p_mac.add_argument("--launch-den", action="store_true")

    return parser


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)

    if args.command == "controls":
        report = run_qualification_controls()
        report["provenance"] = collect_provenance(
            REPO_ROOT, engine_bin=args.engine, engine_manifest=args.engine_manifest, lane="checker-controls"
        )
        text = json.dumps(report, indent=2, sort_keys=True)
        if args.out is not None:
            args.out.parent.mkdir(parents=True, exist_ok=True)
            args.out.write_text(text + "\n", encoding="utf-8")
        print(text)
        return 0 if report["qualified"] else 1

    if args.command == "run":
        tracks = ("workers", "edits", "authority") if args.track == "all" else (args.track,)
        results = {}
        all_ok = True
        for tr in tracks:
            export_sub = (args.export / tr) if args.export is not None else None
            res = run_track_control(
                tr,
                args.out / tr,
                engine_bin=args.engine,
                engine_manifest=args.engine_manifest,
                export_dir=export_sub,
            )
            results[tr] = res["checker"]
            if res["checker"]["verdict"] != "pass":
                all_ok = False
        print(json.dumps(results, indent=2, sort_keys=True))
        return 0 if all_ok else 1

    if args.command == "replay":
        res = replay_on_fresh_profile(
            args.bundle,
            args.out,
            engine_bin=args.engine,
            engine_manifest=args.engine_manifest,
        )
        print(json.dumps(res, indent=2, sort_keys=True))
        return 0 if res["result"]["checker"]["verdict"] == "pass" else 1

    if args.command == "check":
        ev = json.loads(args.evidence.read_text(encoding="utf-8"))
        res = check_evidence(ev)
        print(json.dumps(res, indent=2, sort_keys=True))
        return 0 if res["verdict"] == "pass" else 1

    if args.command == "native-den-macos":
        res = run_macos_native_den(
            args.out,
            api_url=args.api_url,
            token=args.token,
            launch_den=args.launch_den,
            engine_bin=args.engine,
            engine_manifest=args.engine_manifest,
        )
        print(json.dumps(res, indent=2, sort_keys=True))
        return 0 if res["passed"] else 1

    return 2


if __name__ == "__main__":
    raise SystemExit(main())
