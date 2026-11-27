"""WW09 live mode-parity suite: compare LYCAON_DEV_WATCH_DIAGNOSTICS=0 vs 1 on sidecar cases.

Exercises:
- Direct wait controls: c01-fast-exit, c01-parked-exit
- Real watch collect boundary: ww09-watch-collect (CommitCollectBatch)
- Real watch ambient-wake boundary: ww09-watch-ambient-wake (commitAmbientWakeAdmission)
"""
from __future__ import annotations

import argparse
import json
import pathlib
import sys
import uuid
from typing import Any, Callable

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
REPO_ROOT = BENCHMARK_DIR.parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

import smoke  # noqa: E402
from capture_storage import checkpoint_closed_store  # noqa: E402
from portable_command.harness import SSECollector, create_isolated_session  # noqa: E402
from portable_command.provenance import resolve_engine_binary  # noqa: E402
from wait_watch.barriers import ProcessBarrier, TimerGuard  # noqa: E402
from wait_watch.driver import SUPPORTED_CASES, WaitWatchApplication, WaitWatchDriver, run_case  # noqa: E402
from wait_watch.observers import (  # noqa: E402
    AdmissionObserver,
    RenderedInputObserver,
    SourceObserver,
    StorageObserver,
)

WATCH_PARITY_CASES = ("ww09-watch-collect", "ww09-watch-ambient-wake")
ALL_PARITY_CASES = (*SUPPORTED_CASES, *WATCH_PARITY_CASES)
DEFAULT_PARITY_CASES = ALL_PARITY_CASES


def _make_diag_app_factory(
    diag_enabled: bool,
    diag_file: pathlib.Path,
    engine_bin: pathlib.Path | str | None = None,
) -> Callable[[pathlib.Path], Any]:
    """Return an app factory that injects LYCAON_DEV_WATCH_DIAGNOSTICS env into the sidecar."""
    flag = "1" if diag_enabled else "0"
    resolved_bin: pathlib.Path | None = None
    if engine_bin is not None:
        resolved_bin, _ = resolve_engine_binary(REPO_ROOT, engine_bin)

    def factory(directory: pathlib.Path) -> Any:
        orig_env = smoke.application_environment
        orig_engine = smoke.engine_path

        def patched_env(overrides: dict[str, str] | None = None) -> dict[str, str]:
            merged = dict(overrides or {})
            merged["LYCAON_DEV_WATCH_DIAGNOSTICS"] = flag
            merged["LYCAON_DEV_WATCH_DIAGNOSTICS_FILE"] = str(diag_file)
            return orig_env(merged)

        smoke.application_environment = patched_env
        if resolved_bin is not None:
            smoke.engine_path = lambda _root: resolved_bin
        try:
            return WaitWatchApplication(directory)
        finally:
            smoke.application_environment = orig_env
            smoke.engine_path = orig_engine

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


def _poll_storage_predicate(
    db_path: pathlib.Path,
    session_id: str,
    timeout_s: float,
    phase: str,
    predicate: Callable[[dict[str, Any]], bool],
    dump_state: Callable[[], dict[str, Any]],
) -> dict[str, Any]:
    guard = TimerGuard(timeout_s, phase=phase, verdict="incomplete", dump_state=dump_state)

    def check() -> dict[str, Any] | None:
        snap = StorageObserver(db_path).snapshot(session_id)
        return snap if predicate(snap) else None

    return guard.poll(check, detail=f"storage predicate not satisfied for {phase}")


def run_watch_boundary_case(
    case_id: str,
    out_dir: pathlib.Path | str,
    *,
    diag_enabled: bool,
    diag_file: pathlib.Path,
    engine_bin: pathlib.Path | str | None = None,
    seed: int = 1,
    timeout_s: float = 25.0,
) -> dict[str, Any]:
    """Exercise real watch collect (CommitCollectBatch) or ambient-wake (commitAmbientWakeAdmission) on lycaon-dev."""
    if case_id not in WATCH_PARITY_CASES:
        raise ValueError(f"unsupported watch boundary case {case_id!r}")

    out_path = pathlib.Path(out_dir).resolve()
    app_dir = out_path / "app"
    project_dir = out_path / "project"
    barrier_dir = project_dir / ".ww09-barrier"
    raw_dir = out_path / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text(f"# WW09 watch parity {case_id} seed={seed}\n", encoding="utf-8")

    marker = f"ww09-{case_id}-seed-{seed}"
    barrier = ProcessBarrier(barrier_dir, exit_code=0, marker=marker, payload=f"{marker}:ok\n")
    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")

    application = _make_diag_app_factory(diag_enabled, diag_file, engine_bin)(app_dir)
    sse_path = raw_dir / "events.jsonl"
    sse = None
    session_id = ""
    delivery_mode = "collect" if case_id == "ww09-watch-collect" else "wake"
    expected_dest_kind = "request" if delivery_mode == "collect" else "ambient_wake"
    verdict = "pass"
    defect_codes: list[str] = []

    try:
        application.ready()
        sse = SSECollector(application.url, application.token, sse_path)
        sse.start()
        session_id, _, _, _ = create_isolated_session(application, project_dir, {"profile": "implement", "turns": [1, 2, 3]})
        submission_id = str(uuid.uuid4())
        application.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": submission_id, "text": f"Run WW09 {case_id} watch parity check."},
        )
        driver = WaitWatchDriver(
            application,
            session_id,
            rendered_observer=rendered_obs,
            source_observer=source_obs,
            timeout_s=timeout_s,
            sse_path=sse_path,
        )

        # 1. Launch gated background command and register a real watch (collect or wake).
        # For ambient-wake, mark the progress item complete ([x]) so implement posture settles the turn to idle.
        cmd_str = barrier.command_string(relative_to=project_dir, background=True)
        todo_mark = "x" if delivery_mode == "wake" else " "
        _, _, handle = driver.call_command(
            cmd_str,
            progress_update=f"- [{todo_mark}] Run WW09 {case_id} watch parity check",
        )
        barrier.wait_reached(timeout_s=min(10.0, timeout_s), dump_state=driver._dump_state)
        source_obs.record_event("command_launched", handle=handle)
        _, _, watch_receipt = driver.call_watch(
            {
                "conditions": [{"kind": "process_done", "handles": [handle]}],
                "delivery": delivery_mode,
            }
        )
        watch_id = str(watch_receipt.get("watch_id") or "")
        if not watch_id:
            verdict = "fail"
            defect_codes.append("watch_registration_missing")

        db_path = app_dir / "store.db"
        if delivery_mode == "collect":
            # Release barrier so match is recorded, then issue a tool call so the engine builds
            # a fresh model request AFTER watch_matches >= 1 (triggering ReserveCollectBatch -> CommitCollectBatch).
            barrier.release()
            _poll_storage_predicate(
                db_path,
                session_id,
                timeout_s,
                "ww09.collect.match_recorded",
                lambda s: int((s.get("counts") or {}).get("watch_matches", 0)) >= 1,
                driver._dump_state,
            )
            driver.call_watch_control({"operation": "list"})
            next_req = driver.await_pending("ww09.collect.model_turn")
            driver.finish_turn(next_req, "Observed collected watch batch.")
        else:
            # Finish current turn and wait for prompt_submissions to reach completed (idle session),
            # then release barrier to trigger ambient wake turn (commitAmbientWakeAdmission).
            idle_req = driver.await_pending("ww09.wake.finish_registration_turn")
            driver.finish_turn(idle_req, "Waiting for ambient watch wake.")
            _poll_storage_predicate(
                db_path,
                session_id,
                timeout_s,
                "ww09.wake.idle",
                lambda s: bool((s.get("tables") or {}).get("prompt_submissions"))
                and all(
                    isinstance(row, dict) and row.get("status") in {"completed", "settled", "interrupted"}
                    for row in ((s.get("tables") or {}).get("prompt_submissions") or [])
                ),
                driver._dump_state,
            )
            barrier.release()
            wake_req = driver.await_pending("ww09.wake.ambient_turn")
            driver.finish_turn(wake_req, "Observed ambient wake watch batch.")

        # Confirm the committed watch_deliveries row of the expected destination_kind.
        _poll_storage_predicate(
            db_path,
            session_id,
            timeout_s,
            f"ww09.{delivery_mode}.delivery_committed",
            lambda s: any(
                isinstance(row, dict)
                and row.get("destination_kind") == expected_dest_kind
                and row.get("state") == "committed"
                for row in ((s.get("tables") or {}).get("watch_deliveries") or [])
            ),
            driver._dump_state,
        )
    except Exception as exc:
        verdict = "fail"
        defect_codes.append(f"exception:{type(exc).__name__}:{exc}")
    finally:
        if sse is not None:
            sse.close()
        application.close(cleanup=False)

    proc = getattr(application, "process", None)
    if proc is not None:
        checkpoint_closed_store(app_dir / "store.db", proc)
    storage_snap = StorageObserver(app_dir / "store.db").snapshot(session_id)
    committed_matching = sum(
        1
        for row in ((storage_snap.get("tables") or {}).get("watch_deliveries") or [])
        if isinstance(row, dict)
        and row.get("destination_kind") == expected_dest_kind
        and row.get("state") == "committed"
    )
    if committed_matching < 1 and verdict == "pass":
        verdict = "fail"
        defect_codes.append(f"missing_committed_{expected_dest_kind}_delivery")

    return {
        "run_id": f"{case_id}-{'on' if diag_enabled else 'off'}-s{seed}-{uuid.uuid4().hex[:8]}",
        "case_id": case_id,
        "checker_verdict": {"verdict": verdict, "defect_codes": defect_codes},
        "observers": {
            "source": source_obs.snapshot(),
            "storage": storage_snap,
            "admission": admission_obs.snapshot(),
            "rendered_input": rendered_obs.snapshot(),
        },
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
    deliveries = storage_tables.get("watch_deliveries") or []
    committed_collect = sum(
        1 for row in deliveries if isinstance(row, dict) and row.get("destination_kind") == "request" and row.get("state") == "committed"
    )
    committed_ambient_wake = sum(
        1 for row in deliveries if isinstance(row, dict) and row.get("destination_kind") == "ambient_wake" and row.get("state") == "committed"
    )
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
        "process_report_published_count": pub_phases,
        "armed_wait_lease_count": armed_leases,
        "committed_collect_deliveries": committed_collect,
        "committed_ambient_wake_deliveries": committed_ambient_wake,
        "storage_watch_presentation_claims": int(storage_counts.get("watch_presentation_claims", 0)),
        "storage_watch_deliveries": int(storage_counts.get("watch_deliveries", 0)),
        "storage_watch_registrations": int(storage_counts.get("watch_registrations", 0)),
        "storage_wait_leases": int(storage_counts.get("wait_leases", 0)),
        "storage_invocation_receipts": int(storage_counts.get("invocation_receipts", 0)),
    }


def extract_informational_metrics(bundle: dict[str, Any]) -> dict[str, Any]:
    """Extract non-gating driver-fixture telemetry (e.g. fixture source event count)."""
    source = (bundle.get("observers") or {}).get("source") or {}
    return {
        "source_event_count": len(source.get("events") or []),
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
        "disabled": {
            "run_id": off_bundle.get("run_id", ""),
            "metrics": off_metrics,
            "informational": extract_informational_metrics(off_bundle),
            "diag_file": off_diag,
        },
        "enabled": {
            "run_id": on_bundle.get("run_id", ""),
            "metrics": on_metrics,
            "informational": extract_informational_metrics(on_bundle),
            "diag_file": on_diag,
        },
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

        if case_id in WATCH_PARITY_CASES:
            off_bundle = run_watch_boundary_case(
                case_id,
                off_dir,
                diag_enabled=False,
                diag_file=off_diag_file,
                engine_bin=engine_bin,
                seed=seed,
                timeout_s=timeout_s,
            )
            on_bundle = run_watch_boundary_case(
                case_id,
                on_dir,
                diag_enabled=True,
                diag_file=on_diag_file,
                engine_bin=engine_bin,
                seed=seed,
                timeout_s=timeout_s,
            )
        else:
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
        choices=ALL_PARITY_CASES,
        default=list(DEFAULT_PARITY_CASES),
        help="Control cases to run in both disabled and enabled modes.",
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
