"""WW09 live mode-parity suite: compare LYCAON_DEV_WATCH_DIAGNOSTICS=0 vs 1 on sidecar cases."""
from __future__ import annotations

import argparse
import json
import pathlib
import sys
from typing import Any, Callable

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

import smoke  # noqa: E402
from wait_watch.driver import SUPPORTED_CASES, WaitWatchApplication, run_case  # noqa: E402

DEFAULT_PARITY_CASES = ("c01-fast-exit", "c01-parked-exit")


def _make_diag_app_factory(diag_enabled: bool, diag_file: pathlib.Path) -> Callable[[pathlib.Path], Any]:
    """Return an app factory that injects LYCAON_DEV_WATCH_DIAGNOSTICS env into the sidecar."""
    flag = "1" if diag_enabled else "0"

    def factory(directory: pathlib.Path) -> Any:
        orig_env = smoke.application_environment

        def patched_env(overrides: dict[str, str] | None = None) -> dict[str, str]:
            merged = dict(overrides or {})
            merged["LYCAON_DEV_WATCH_DIAGNOSTICS"] = flag
            merged["LYCAON_DEV_WATCH_DIAGNOSTICS_FILE"] = str(diag_file)
            return orig_env(merged)

        smoke.application_environment = patched_env
        try:
            return WaitWatchApplication(directory)
        finally:
            smoke.application_environment = orig_env

    return factory


def _read_diag_jsonl(path: pathlib.Path) -> dict[str, Any]:
    if not path.exists():
        return {"exists": False, "line_count": 0, "contradictions": 0, "overflows": 0, "parse_errors": 0}
    raw_lines = [ln for ln in path.read_text(encoding="utf-8").splitlines() if ln.strip()]
    contradictions = 0
    overflows = 0
    parse_errors = 0
    for line in raw_lines:
        try:
            rec = json.loads(line)
        except json.JSONDecodeError:
            parse_errors += 1
            continue
        disp = rec.get("disposition")
        if disp == "contradiction":
            contradictions += 1
        elif disp == "overflow":
            overflows += 1
    return {
        "exists": True,
        "line_count": len(raw_lines),
        "contradictions": contradictions,
        "overflows": overflows,
        "parse_errors": parse_errors,
    }


def extract_parity_metrics(bundle: dict[str, Any]) -> dict[str, Any]:
    """Extract typed outcome, admission, and work-bound counts from an evidence bundle."""
    verdict = bundle.get("checker_verdict") or {}
    observers = bundle.get("observers") or {}
    source = observers.get("source") or {}
    source_facts = source.get("facts") or {}
    storage = observers.get("storage") or {}
    storage_counts = storage.get("counts") or {}
    storage_tables = storage.get("tables") or {}
    admission = observers.get("admission") or {}
    rendered = observers.get("rendered_input") or {}

    leases = storage_tables.get("wait_leases") or []
    armed_leases = sum(1 for row in leases if isinstance(row, dict) and row.get("status") == "armed")
    phase_records = source_facts.get("phase_records") or []
    pub_phases = sum(
        1 for rec in phase_records if isinstance(rec, dict) and rec.get("phase") == "process_report_published"
    )

    return {
        "verdict": verdict.get("verdict", ""),
        "defect_count": len(verdict.get("defect_codes") or []),
        "new_model_presentation_count": int(rendered.get("new_model_presentation_count", 0)),
        "history_reread_count": int(rendered.get("history_reread_count", 0)),
        "delivery_receipt_count": int(rendered.get("delivery_receipt_count", 0)),
        "admission_event_count": len(admission.get("events") or []),
        "source_event_count": len(source.get("events") or []),
        "process_report_published_count": pub_phases,
        "armed_wait_lease_count": armed_leases,
        "storage_watch_presentation_claims": int(storage_counts.get("watch_presentation_claims", 0)),
        "storage_watch_deliveries": int(storage_counts.get("watch_deliveries", 0)),
        "storage_watch_registrations": int(storage_counts.get("watch_registrations", 0)),
        "storage_wait_leases": int(storage_counts.get("wait_leases", 0)),
        "storage_invocation_receipts": int(storage_counts.get("invocation_receipts", 0)),
    }


def compare_parity_runs(
    case_id: str,
    off_bundle: dict[str, Any],
    on_bundle: dict[str, Any],
    off_diag: dict[str, Any],
    on_diag: dict[str, Any],
) -> dict[str, Any]:
    """Compare disabled vs enabled runs for exact outcome, work-bound, and admission parity."""
    off_metrics = extract_parity_metrics(off_bundle)
    on_metrics = extract_parity_metrics(on_bundle)
    mismatches: list[dict[str, Any]] = []

    for key, off_val in off_metrics.items():
        on_val = on_metrics.get(key)
        if off_val != on_val:
            mismatches.append({"metric": key, "disabled": off_val, "enabled": on_val})

    if off_metrics["verdict"] != "pass":
        mismatches.append({"metric": "disabled_verdict_pass", "disabled": off_metrics["verdict"], "enabled": "pass"})
    if on_metrics["verdict"] != "pass":
        mismatches.append({"metric": "enabled_verdict_pass", "disabled": "pass", "enabled": on_metrics["verdict"]})

    if off_diag.get("exists"):
        mismatches.append({"metric": "disabled_mode_wrote_diag_file", "disabled": True, "enabled": False})
    if on_diag.get("parse_errors", 0) > 0:
        mismatches.append({"metric": "enabled_diag_parse_errors", "disabled": 0, "enabled": on_diag["parse_errors"]})
    if on_diag.get("contradictions", 0) > 0:
        mismatches.append({"metric": "enabled_diag_contradictions", "disabled": 0, "enabled": on_diag["contradictions"]})
    if on_diag.get("overflows", 0) > 0:
        mismatches.append({"metric": "enabled_diag_overflows", "disabled": 0, "enabled": on_diag["overflows"]})

    return {
        "case_id": case_id,
        "passed": len(mismatches) == 0,
        "disabled": {"run_id": off_bundle.get("run_id", ""), "metrics": off_metrics, "diag_file": off_diag},
        "enabled": {"run_id": on_bundle.get("run_id", ""), "metrics": on_metrics, "diag_file": on_diag},
        "mismatches": mismatches,
    }


def run_parity_suite(
    out_dir: pathlib.Path | str,
    *,
    cases: tuple[str, ...] = DEFAULT_PARITY_CASES,
    engine_bin: pathlib.Path | str | None = None,
    seed: int = 1,
    timeout_s: float = 25.0,
) -> dict[str, Any]:
    out_path = pathlib.Path(out_dir).resolve()
    out_path.mkdir(parents=True, exist_ok=True)

    results: list[dict[str, Any]] = []
    for case_id in cases:
        off_dir = out_path / case_id / "disabled"
        on_dir = out_path / case_id / "enabled"
        off_diag_file = off_dir / "watch-diagnostics.jsonl"
        on_diag_file = on_dir / "watch-diagnostics.jsonl"

        off_bundle = run_case(
            case_id,
            off_dir,
            engine_bin=engine_bin,
            seed=seed,
            timeout_s=timeout_s,
            app_factory=_make_diag_app_factory(False, off_diag_file),
        )
        on_bundle = run_case(
            case_id,
            on_dir,
            engine_bin=engine_bin,
            seed=seed,
            timeout_s=timeout_s,
            app_factory=_make_diag_app_factory(True, on_diag_file),
        )
        results.append(
            compare_parity_runs(
                case_id,
                off_bundle,
                on_bundle,
                _read_diag_jsonl(off_diag_file),
                _read_diag_jsonl(on_diag_file),
            )
        )

    report = {
        "suite": "ww09_mode_parity",
        "cases": list(cases),
        "all_passed": all(item["passed"] for item in results),
        "results": results,
    }
    report_path = out_path / "ww09-mode-parity-report.json"
    report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return report


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="wait_watch.suite_ww09_parity",
        description="Run WW09 enabled vs disabled watch diagnostics mode-parity checks against lycaon-dev.",
    )
    parser.add_argument(
        "--cases",
        nargs="+",
        choices=SUPPORTED_CASES,
        default=list(DEFAULT_PARITY_CASES),
        help="Control cases to run in both disabled and enabled modes (default: c01-fast-exit c01-parked-exit).",
    )
    parser.add_argument("--engine", type=pathlib.Path, default=None, help="Path to built lycaon engine binary.")
    parser.add_argument("--out", type=pathlib.Path, default=pathlib.Path("out/ww09-parity"), help="Output directory.")
    parser.add_argument("--seed", type=int, default=1, help="Deterministic fixture seed (default: 1).")
    parser.add_argument("--timeout", type=float, default=25.0, help="Explicit short timer guard in seconds.")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    report = run_parity_suite(
        args.out,
        cases=tuple(args.cases),
        engine_bin=args.engine,
        seed=args.seed,
        timeout_s=args.timeout,
    )
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0 if report["all_passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
