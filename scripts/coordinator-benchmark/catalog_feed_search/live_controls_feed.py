"""Control 2: project-scoped live SSE change-feed control with baseline wait and sentinel drain."""

from __future__ import annotations

from pathlib import Path
from typing import Callable

from .change_runner import ChangeOp, ChangeRunner, ChangeScript, WriteLog, WriteLogEntry
from .checker import walk_entries_to_catalog_items
from .checker_feed import check_change_feed
from .checker_types import CheckResult, Verdict
from .driver_adapters import map_sse_source_changed_to_answer
from .driver_sidecar import SidecarDriver
from .driver_sse import SSESubscription
from .generators import BoundaryConfig
from .references import reference_walk
from .seeds import BASE_SEED_PRIMARY, ReplaySelector

DRAIN_SENTINEL_REL_PATH = "src/.cfs-drain-sentinel.txt"


def _is_initial_inventory_frame(frame: dict[str, object], project_id: str) -> bool:
    if frame.get("topic") != "source_changed":
        return False
    data = frame.get("data")
    if not isinstance(data, dict) or str(data.get("project_id") or "") != project_id:
        return False
    return data.get("resync") is False and data.get("changes") == []


def _has_matching_change(
    frames: list[dict[str, object]],
    project_id: str,
    root_id: str,
    rel_path: str,
) -> bool:
    for frame in frames:
        if frame.get("topic") != "source_changed":
            continue
        data = frame.get("data")
        if not isinstance(data, dict) or str(data.get("project_id") or "") != project_id:
            continue
        if data.get("resync") is True:
            return True
        for ch in data.get("changes") or []:
            if (
                isinstance(ch, dict)
                and str(ch.get("path") or "").removeprefix("./") == rel_path
                and str(ch.get("root_id") or root_id) == root_id
            ):
                return True
    return False


def _retag_write_log(raw_log: WriteLog, root_id: str, seq_offset: int = 0) -> list[WriteLogEntry]:
    return [
        WriteLogEntry(
            seq=e.seq + seq_offset,
            clock_domain=e.clock_domain,
            timestamp_ns=e.timestamp_ns,
            op=e.op,
            path=e.path,
            new_path=e.new_path,
            before_sha256=e.before_sha256,
            after_sha256=e.after_sha256,
            before_mode=e.before_mode,
            after_mode=e.after_mode,
            root_id=root_id,
        )
        for e in raw_log.entries
    ]


def execute_change_feed_control(
    root_dir: Path,
    project_id: str,
    root_id: str,
    sse_subscription: SSESubscription,
    *,
    driver: SidecarDriver | None = None,
    wait_settled_fn: Callable[[SidecarDriver, str, str], tuple[bool, int, dict[str, object]]] | None = None,
    boundary_config: BoundaryConfig | None = None,
    rel_path: str = "src/control_write.txt",
    sentinel_path: str = DRAIN_SENTINEL_REL_PATH,
    ready_wait_s: float = 10.0,
    init_wait_s: float = 5.0,
    observe_wait_s: float = 10.0,
    drain_wait_s: float = 10.0,
    clock_fn: Callable[[], int] | None = None,
    clock_domain: str = "monotonic_ns",
) -> tuple[bool, CheckResult, dict[str, object]]:
    """Run Control 2: readiness barrier, baseline wait, primary write, sentinel drain, D4 check."""
    sse_subscription.start()
    try:
        if not sse_subscription.wait_ready(max_wait_s=ready_wait_s):
            return (
                False,
                CheckResult(Verdict.UNKNOWN, "feed", reason="SSE readiness deadline expired before write"),
                {"sse_ready": False},
            )

        baseline_frame_count = 0
        init_baseline_seen = False
        if init_wait_s > 0:
            init_frames, init_baseline_seen = sse_subscription.wait_for_frames(
                lambda fs: any(_is_initial_inventory_frame(f, project_id) for f in fs),
                max_wait_s=init_wait_s,
            )
            if init_baseline_seen:
                baseline_frame_count = len(init_frames)

        runner = ChangeRunner(clock_fn=clock_fn, clock_domain=clock_domain)
        primary_log = runner.run(
            root_dir,
            ChangeScript(
                selector=ReplaySelector(BASE_SEED_PRIMARY, "CONTROL_FEED", 0),
                ops=(ChangeOp("edit", rel_path, content=b"live control write payload\n"),),
            ),
        )
        logged_entries = _retag_write_log(primary_log, root_id, seq_offset=0)

        frames, arrived = sse_subscription.wait_for_frames(
            lambda fs: _has_matching_change(fs[baseline_frame_count:], project_id, root_id, rel_path),
            max_wait_s=observe_wait_s,
        )
        if not arrived:
            return (
                False,
                CheckResult(
                    Verdict.UNKNOWN,
                    "feed",
                    reason="harness observation deadline expired before target change arrived",
                ),
                {
                    "sse_ready": True,
                    "init_baseline_seen": init_baseline_seen,
                    "baseline_frame_count": baseline_frame_count,
                    "primary_arrived": False,
                    "observed_frame_count": len(frames) - baseline_frame_count,
                },
            )

        before_sentinel_idx = len(frames)
        sentinel_log = runner.run(
            root_dir,
            ChangeScript(
                selector=ReplaySelector(BASE_SEED_PRIMARY, "CONTROL_FEED_SENTINEL", 0),
                ops=(ChangeOp("edit", sentinel_path, content=b"drain sentinel updated\n"),),
            ),
        )
        logged_entries.extend(_retag_write_log(sentinel_log, root_id, seq_offset=len(logged_entries)))

        frames, drained = sse_subscription.wait_for_frames(
            lambda fs: _has_matching_change(fs[before_sentinel_idx:], project_id, root_id, sentinel_path),
            max_wait_s=drain_wait_s,
        )
    finally:
        sse_subscription.close()

    post_write_frames = frames[baseline_frame_count:]
    write_log = WriteLog(clock_domain=clock_domain, entries=tuple(logged_entries))
    answer = map_sse_source_changed_to_answer(
        post_write_frames,
        project_id=project_id,
        default_root_id=root_id,
    )
    if not drained:
        return (
            False,
            CheckResult(
                Verdict.UNKNOWN,
                "feed",
                reason="primary change arrived but sentinel drain boundary expired; cannot qualify exactly-once",
            ),
            {
                "sse_ready": True,
                "init_baseline_seen": init_baseline_seen,
                "baseline_frame_count": baseline_frame_count,
                "primary_arrived": True,
                "drain_boundary": "sentinel_deadline_expired",
                "observed_event_count": len(answer.events),
            },
        )

    if driver is not None and any(g.reason == "resync" for g in answer.declared_gaps):
        if wait_settled_fn is not None:
            wait_settled_fn(driver, project_id, root_id)
        reconciled = driver.walk_catalog_index(project_id, root_id)
        ref_post_entries = [
            e
            for e in reference_walk(root_dir, boundary_config=boundary_config, include_hidden=True)
            if e.kind == "file"
        ]
        ref_post_walk = walk_entries_to_catalog_items(ref_post_entries, root_id=root_id)
        answer = map_sse_source_changed_to_answer(
            post_write_frames,
            project_id=project_id,
            default_root_id=root_id,
            reconciled_walk=reconciled.items,
            reference_post_walk=ref_post_walk,
        )

    res = check_change_feed(write_log, answer)
    return (
        res.verdict in (Verdict.PASS, Verdict.PASS_DECLARED_GAP),
        res,
        {
            "sse_ready": True,
            "init_baseline_seen": init_baseline_seen,
            "baseline_frame_count": baseline_frame_count,
            "primary_arrived": True,
            "drain_boundary": "sentinel_write",
            "write_log": write_log.to_dict(),
            "post_write_frames": post_write_frames,
            "observed_event_count": len(answer.events),
            "batch_ids": sorted({e.batch_id for e in answer.events}),
        },
    )
