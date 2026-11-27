"""CLI entrypoint for wait/watch sidecar driver, checker controls, replay, and native Den smoke."""
from __future__ import annotations

import argparse
import json
import pathlib
import sys
from typing import Any

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wait_watch.checker import check_bundle, run_checker_controls  # noqa: E402
from wait_watch.driver import SUPPORTED_CASES, replay_bundle, run_case  # noqa: E402
from wait_watch.native_den import run_native_smoke  # noqa: E402


def _write_json(path: pathlib.Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="wait_watch.cli",
        description="Wait/Watch bug-bash real-host driver, checker controls, replay, and native Den smoke.",
    )
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--case", choices=SUPPORTED_CASES, help="Run a sidecar wait/watch control case.")
    mode.add_argument("--replay", type=pathlib.Path, help="Replay a retained evidence bundle JSON on a fresh profile.")
    mode.add_argument("--controls", action="store_true", help="Run all 10 checker controls + missing-instrumentation check.")
    mode.add_argument("--check", type=pathlib.Path, help="Grade an existing evidence bundle JSON with the deterministic checker.")
    mode.add_argument("--native-smoke", action="store_true", help="Run native Den keyboard-navigation smoke on this host.")

    parser.add_argument("--engine", type=pathlib.Path, default=None, help="Path to built lycaon engine binary.")
    parser.add_argument("--out", type=pathlib.Path, default=None, help="Output directory for evidence and reports.")
    parser.add_argument("--export", type=pathlib.Path, default=None, help="Export replayable evidence bundle JSON to path.")
    parser.add_argument("--seed", type=int, default=1, help="Deterministic fixture seed (default: 1).")
    parser.add_argument("--timeout", type=float, default=25.0, help="Explicit short timer guard in seconds (default: 25).")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)

    if args.controls:
        report = run_checker_controls()
        if args.out is not None:
            _write_json(args.out / "checker-controls.json", report)
        if args.export is not None:
            _write_json(args.export, report)
        print(json.dumps(report, indent=2, sort_keys=True))
        return 0 if report["all_passed"] else 1

    if args.check is not None:
        bundle = json.loads(args.check.read_text(encoding="utf-8"))
        verdict = check_bundle(bundle)
        if args.out is not None:
            _write_json(args.out / "checker-verdict.json", verdict)
        print(json.dumps(verdict, indent=2, sort_keys=True))
        return 0 if verdict["verdict"] == "pass" else 1

    if args.native_smoke:
        out_dir = args.out or pathlib.Path("out/native-smoke")
        result = run_native_smoke(out_dir)
        _write_json(out_dir / "native-den-smoke.json", result)
        print(json.dumps(result, indent=2, sort_keys=True))
        return 0 if result.get("status") in {"passed", "blocked"} else 1

    if args.case is not None:
        out_dir = args.out or pathlib.Path(f"out/{args.case}")
        bundle = run_case(
            args.case,
            out_dir,
            engine_bin=args.engine,
            seed=args.seed,
            timeout_s=args.timeout,
            export_path=args.export,
        )
        verdict = bundle.get("checker_verdict", {})
        print(json.dumps({"run_id": bundle["run_id"], "case_id": args.case, "verdict": verdict}, indent=2, sort_keys=True))
        return 0 if verdict.get("verdict") == "pass" else 1

    if args.replay is not None:
        out_dir = args.out or pathlib.Path("out/replay")
        bundle = replay_bundle(args.replay, out_dir, engine_bin=args.engine)
        if args.export is not None:
            _write_json(args.export, bundle)
        verdict = bundle.get("checker_verdict", {})
        print(json.dumps({"run_id": bundle["run_id"], "replayed": True, "verdict": verdict}, indent=2, sort_keys=True))
        return 0 if verdict.get("verdict") == "pass" else 1

    return 2


if __name__ == "__main__":
    raise SystemExit(main())
