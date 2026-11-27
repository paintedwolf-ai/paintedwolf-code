"""The three runbook live controls (settled catalog, project-scoped SSE + drain, literal search)."""

from __future__ import annotations

from dataclasses import dataclass
import json
from pathlib import Path
import threading
import time
from typing import Callable

from .checker import ScopedSearchHit, check_catalog, check_search, walk_entries_to_catalog_items
from .checker_types import CheckResult, Verdict
from .driver_adapters import map_search_response_to_answer
from .driver_provenance import RunProvenance, write_evidence_manifest
from .driver_sidecar import SidecarDriver
from .driver_sse import SSESubscription
from .generators import BoundaryConfig, TreeEntrySpec, TreeSpec, materialize_tree
from .live_controls_feed import DRAIN_SENTINEL_REL_PATH, execute_change_feed_control
from .queries import CodeQuerySpec
from .references import reference_search, reference_walk
from .seeds import BASE_SEED_PRIMARY, ReplaySelector


@dataclass(frozen=True)
class LiveControlResult:
    """Outcome of one runbook live control."""

    name: str
    passed: bool
    check: CheckResult
    evidence: dict[str, object]

    def to_dict(self) -> dict[str, object]:
        return {
            "name": self.name,
            "passed": self.passed,
            "verdict": self.check.verdict.value,
            "reason": self.check.reason,
            "evidence": self.evidence,
        }


@dataclass(frozen=True)
class LiveControlsReport:
    """Aggregated report for all 3 runbook live controls."""

    controls: tuple[LiveControlResult, ...]
    manifest_path: Path | None = None

    @property
    def all_passed(self) -> bool:
        return all(c.passed for c in self.controls)

    def to_dict(self) -> dict[str, object]:
        return {
            "all_passed": self.all_passed,
            "total": len(self.controls),
            "passed": sum(1 for c in self.controls if c.passed),
            "controls": [c.to_dict() for c in self.controls],
            "manifest_path": str(self.manifest_path) if self.manifest_path else None,
        }


def _is_root_settled(summary: dict[str, object], root_id: str) -> bool:
    coverage = summary.get("coverage")
    if isinstance(coverage, list) and coverage:
        for item in coverage:
            if not isinstance(item, dict):
                continue
            if str(item.get("root_id") or "") == root_id:
                return (
                    str(item.get("state") or "").lower() == "ready"
                    and item.get("discovery_complete") is True
                    and item.get("refreshing") is False
                )
        return False
    return (
        str(summary.get("state") or "").lower() == "ready"
        and summary.get("refreshing") is False
    )


def wait_catalog_settled(
    driver: SidecarDriver,
    project_id: str,
    root_id: str,
    *,
    max_wait_s: float = 15.0,
    poll_interval_s: float = 0.1,
) -> tuple[bool, int, dict[str, object]]:
    """Poll GET /v1/projects/{id}/source/index until root discovery settles or deadline expires."""
    deadline = time.monotonic() + max_wait_s
    waiter = threading.Event()
    polls = 0
    last_summary: dict[str, object] = {}

    while True:
        polls += 1
        last_summary = driver.get_source_index(project_id)
        if _is_root_settled(last_summary, root_id):
            return True, polls, last_summary
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            return False, polls, last_summary
        waiter.wait(min(poll_interval_s, remaining))


def run_live_control_catalog(
    driver: SidecarDriver,
    root_dir: Path,
    project_id: str,
    root_id: str,
    *,
    boundary_config: BoundaryConfig | None = None,
    settle_wait_s: float = 15.0,
    settle_poll_s: float = 0.1,
) -> LiveControlResult:
    """Control 1: wait for catalog settlement, then verify catalog index equals reference_walk."""
    settled, settle_polls, last_summary = wait_catalog_settled(
        driver,
        project_id,
        root_id,
        max_wait_s=settle_wait_s,
        poll_interval_s=settle_poll_s,
    )
    if not settled:
        return LiveControlResult(
            name="settled_catalog_equals_reference_walk",
            passed=False,
            check=CheckResult(
                Verdict.UNKNOWN,
                "catalog",
                reason="catalog settlement deadline expired before control 1",
            ),
            evidence={
                "settled": False,
                "settle_polls": settle_polls,
                "raw_index_summary": last_summary,
                "last_coverage": last_summary.get("coverage", []),
                "last_state": last_summary.get("state"),
            },
        )

    ref_entries = [
        e
        for e in reference_walk(root_dir, boundary_config=boundary_config, include_hidden=True)
        if e.kind == "file"
    ]
    ref_items = walk_entries_to_catalog_items(ref_entries, root_id=root_id)
    answer = driver.walk_catalog_index(project_id, root_id)
    res = check_catalog(ref_items, answer)
    return LiveControlResult(
        name="settled_catalog_equals_reference_walk",
        passed=(res.verdict == Verdict.PASS),
        check=res,
        evidence={
            "settled": True,
            "settle_polls": settle_polls,
            "projection": answer.projection,
            "generation_revision": answer.generation_revision,
            "reference_count": len(ref_items),
            "product_count": len(answer.items),
            "reference_paths": sorted(i.path for i in ref_items),
            "product_paths": sorted(i.path for i in answer.items),
            "raw_index_summary": last_summary,
        },
    )


def run_live_control_change_feed(
    root_dir: Path,
    project_id: str,
    root_id: str,
    sse_subscription: SSESubscription,
    *,
    driver: SidecarDriver | None = None,
    boundary_config: BoundaryConfig | None = None,
    rel_path: str = "src/control_write.txt",
    sentinel_path: str = DRAIN_SENTINEL_REL_PATH,
    ready_wait_s: float = 10.0,
    init_wait_s: float = 5.0,
    observe_wait_s: float = 10.0,
    drain_wait_s: float = 10.0,
    clock_fn: Callable[[], int] | None = None,
    clock_domain: str = "monotonic_ns",
) -> LiveControlResult:
    """Control 2: project-scoped /v1/events stream, baseline wait, primary write, sentinel drain."""
    passed, check, evidence = execute_change_feed_control(
        root_dir,
        project_id,
        root_id,
        sse_subscription,
        driver=driver,
        wait_settled_fn=wait_catalog_settled,
        boundary_config=boundary_config,
        rel_path=rel_path,
        sentinel_path=sentinel_path,
        ready_wait_s=ready_wait_s,
        init_wait_s=init_wait_s,
        observe_wait_s=observe_wait_s,
        drain_wait_s=drain_wait_s,
        clock_fn=clock_fn,
        clock_domain=clock_domain,
    )
    return LiveControlResult(
        name="single_logged_write_once_in_change_stream",
        passed=passed,
        check=check,
        evidence=evidence,
    )


def run_live_control_literal_search(
    driver: SidecarDriver,
    root_dir: Path,
    project_id: str,
    root_id: str,
    *,
    token: str = "PaintedWolfControlToken",
    rg_bin: str = "rg",
) -> LiveControlResult:
    """Control 3: single literal search compared on documented D1 path/line projection."""
    query = CodeQuerySpec(form="literal", pattern=token, case_sensitive=True)
    ref = reference_search(root_dir, query, rg_bin=rg_bin, include_hidden=False)
    ref_hits = tuple(ScopedSearchHit(hit=h, root_id=root_id, leg="code") for h in ref.hits)
    raw_resp = driver.search_raw(
        token,
        origin_project_id=project_id,
        case_sensitive=True,
        regex=False,
    )
    answer = map_search_response_to_answer(raw_resp, default_root_id=root_id, default_leg="code")
    res = check_search(ref_hits, answer, projection="path_line")
    return LiveControlResult(
        name="literal_search_equals_reference_search",
        passed=(res.verdict == Verdict.PASS),
        check=res,
        evidence={
            "projection": "path_line",
            "rg_version": ref.rg_version,
            "reference_hit_count": len(ref_hits),
            "product_hit_count": len(answer.hits),
            "projected_reference_hits": [list(h.key("path_line")) for h in ref_hits],
            "projected_product_hits": [list(h.key("path_line")) for h in answer.hits],
            "raw_search_response": raw_resp,
        },
    )


def build_live_control_fixture(root_dir: Path) -> None:
    """Materialize a minimal deterministic fixture for the 3 runbook live controls."""
    sel = ReplaySelector(BASE_SEED_PRIMARY, "CONTROL", 0)
    spec = TreeSpec(
        selector=sel,
        entries=(
            TreeEntrySpec("src/alpha.txt", "file", b"PaintedWolfControlToken line one\n"),
            TreeEntrySpec("src/control_write.txt", "file", b"initial content\n"),
            TreeEntrySpec(DRAIN_SENTINEL_REL_PATH, "file", b"drain sentinel initial\n"),
            TreeEntrySpec("docs/readme.md", "file", b"control fixture readme\n"),
        ),
    )
    materialize_tree(spec, root_dir)


def run_three_live_controls(
    driver: SidecarDriver,
    root_dir: Path,
    sse_factory: Callable[[str], SSESubscription],
    *,
    provenance: RunProvenance | None = None,
    out_dir: Path | None = None,
    extra_artifacts: dict[str, Path] | None = None,
    replay_command: list[str] | None = None,
    launch_receipt: dict[str, object] | None = None,
    boundary_config: BoundaryConfig | None = None,
    rg_bin: str = "rg",
    clock_fn: Callable[[], int] | None = None,
    clock_domain: str = "monotonic_ns",
) -> LiveControlsReport:
    """Run all 3 runbook live controls after catalog settlement, writing SHA-256 manifest."""
    build_live_control_fixture(root_dir)
    project_id, root_ids = driver.attach_project([root_dir])
    root_id = root_ids[0] if root_ids else "root-0"

    c1 = run_live_control_catalog(driver, root_dir, project_id, root_id, boundary_config=boundary_config)
    c2 = run_live_control_change_feed(
        root_dir,
        project_id,
        root_id,
        sse_factory(project_id),
        driver=driver,
        boundary_config=boundary_config,
        clock_fn=clock_fn,
        clock_domain=clock_domain,
    )
    c3 = run_live_control_literal_search(driver, root_dir, project_id, root_id, rg_bin=rg_bin)

    controls = (c1, c2, c3)
    manifest_path: Path | None = None
    if out_dir is not None and provenance is not None:
        out = Path(out_dir)
        out.mkdir(parents=True, exist_ok=True)
        report_path = out / "live_controls.json"
        report_path.write_text(
            json.dumps([c.to_dict() for c in controls], indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
        artifacts: dict[str, Path] = {"live_controls": report_path}
        for name, apath in (extra_artifacts or {}).items():
            if Path(apath).is_file():
                artifacts[name] = Path(apath)
        manifest_path = write_evidence_manifest(
            out,
            provenance,
            artifacts,
            extra={"all_passed": all(c.passed for c in controls)},
            replay_command=replay_command,
            launch_receipt=launch_receipt,
        )

    return LiveControlsReport(controls=controls, manifest_path=manifest_path)
