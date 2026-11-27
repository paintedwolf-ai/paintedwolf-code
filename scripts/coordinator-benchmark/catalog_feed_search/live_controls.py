"""The three runbook live controls (settled catalog, single logged write, literal search)."""

from __future__ import annotations

from dataclasses import dataclass
import json
from pathlib import Path
from typing import Callable

from .change_runner import ChangeOp, ChangeRunner, ChangeScript
from .checker import ScopedSearchHit, check_catalog, check_search, walk_entries_to_catalog_items
from .checker_feed import check_change_feed
from .checker_types import CheckResult, Verdict
from .driver_adapters import map_sse_source_changed_to_answer
from .driver_provenance import RunProvenance, write_evidence_manifest
from .driver_sidecar import SidecarDriver
from .generators import BoundaryConfig, TreeEntrySpec, TreeSpec, materialize_tree
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


def run_live_control_catalog(
    driver: SidecarDriver,
    root_dir: Path,
    project_id: str,
    root_id: str,
    *,
    boundary_config: BoundaryConfig | None = None,
) -> LiveControlResult:
    """Control 1: settled tree whose catalog equals reference_walk."""
    ref_entries = reference_walk(root_dir, boundary_config=boundary_config, include_hidden=True)
    ref_items = walk_entries_to_catalog_items(ref_entries, root_id=root_id)
    answer = driver.walk_catalog(project_id, [root_id])
    res = check_catalog(ref_items, answer)
    return LiveControlResult(
        name="settled_catalog_equals_reference_walk",
        passed=(res.verdict == Verdict.PASS),
        check=res,
        evidence={"reference_count": len(ref_items), "product_count": len(answer.items)},
    )


def run_live_control_change_feed(
    root_dir: Path,
    project_id: str,
    root_id: str,
    collect_sse_frames: Callable[[], list[dict[str, object]]],
    *,
    rel_path: str = "src/control_write.txt",
    clock_fn: Callable[[], int] | None = None,
    clock_domain: str = "monotonic_ns",
) -> LiveControlResult:
    """Control 2: single logged write that appears once in the source_changed stream."""
    sel = ReplaySelector(BASE_SEED_PRIMARY, "CONTROL_FEED", 0)
    script = ChangeScript(
        selector=sel,
        ops=(ChangeOp("edit", rel_path, content=b"live control write payload\n"),),
    )
    runner = ChangeRunner(clock_fn=clock_fn, clock_domain=clock_domain)
    write_log = runner.run(root_dir, script)
    # Re-tag root_id on logged entries to match the attached project root.
    if write_log.entries and write_log.entries[0].root_id != root_id:
        from .change_runner import WriteLog, WriteLogEntry

        write_log = WriteLog(
            clock_domain=write_log.clock_domain,
            entries=tuple(
                WriteLogEntry(
                    seq=e.seq,
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
                for e in write_log.entries
            ),
        )

    frames = collect_sse_frames()
    answer = map_sse_source_changed_to_answer(
        frames,
        project_id=project_id,
        default_root_id=root_id,
    )
    res = check_change_feed(write_log, answer)
    return LiveControlResult(
        name="single_logged_write_once_in_change_stream",
        passed=(res.verdict == Verdict.PASS),
        check=res,
        evidence={"write_log": write_log.to_dict(), "observed_event_count": len(answer.events)},
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
    """Control 3: single literal search that equals reference_search."""
    query = CodeQuerySpec(form="literal", pattern=token, case_sensitive=True)
    ref = reference_search(root_dir, query, rg_bin=rg_bin, include_hidden=False)
    ref_hits = tuple(ScopedSearchHit(hit=h, root_id=root_id, leg="code") for h in ref.hits)
    answer = driver.search(
        token,
        origin_project_id=project_id,
        case_sensitive=True,
        regex=False,
        default_root_id=root_id,
        default_leg="code",
    )
    res = check_search(ref_hits, answer)
    return LiveControlResult(
        name="literal_search_equals_reference_search",
        passed=(res.verdict == Verdict.PASS),
        check=res,
        evidence={
            "rg_version": ref.rg_version,
            "reference_hit_count": len(ref_hits),
            "product_hit_count": len(answer.hits),
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
            TreeEntrySpec("docs/readme.md", "file", b"control fixture readme\n"),
        ),
    )
    materialize_tree(spec, root_dir)


def run_three_live_controls(
    driver: SidecarDriver,
    root_dir: Path,
    collect_sse_frames: Callable[[], list[dict[str, object]]],
    *,
    provenance: RunProvenance | None = None,
    out_dir: Path | None = None,
    boundary_config: BoundaryConfig | None = None,
    rg_bin: str = "rg",
    clock_fn: Callable[[], int] | None = None,
    clock_domain: str = "monotonic_ns",
) -> LiveControlsReport:
    """Run all 3 runbook live controls and optionally emit a SHA-256 evidence manifest."""
    build_live_control_fixture(root_dir)
    project_id, root_ids = driver.attach_project([root_dir])
    root_id = root_ids[0] if root_ids else "root-0"

    c1 = run_live_control_catalog(driver, root_dir, project_id, root_id, boundary_config=boundary_config)
    c2 = run_live_control_change_feed(
        root_dir,
        project_id,
        root_id,
        collect_sse_frames,
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
        manifest_path = write_evidence_manifest(
            out,
            provenance,
            {"live_controls": report_path},
            extra={"all_passed": all(c.passed for c in controls)},
        )

    return LiveControlsReport(controls=controls, manifest_path=manifest_path)
