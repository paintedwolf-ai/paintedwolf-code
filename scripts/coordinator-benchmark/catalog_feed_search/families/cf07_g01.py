"""CF07 G01 · Freshness under a moving tree (sidecar lane + G01-INC-03)."""

from __future__ import annotations

from collections import Counter
from pathlib import Path
import tempfile
import threading
import time

from ..generators import BoundaryConfig, TreeGenOptions, generate_tree_spec, materialize_tree
from ..live_controls import wait_catalog_settled
from ..references import reference_walk
from ..seeds import ReplaySelector, seed_rng
from .cf07_harness import AgentToolSession, CapturedToolCall, D1SidecarProcess

G01_COMMANDS = ("find", "grep", "wc", "summarize")


def _command_for_route(route: str, sample_file: str) -> str:
    if route == "find":
        return "find . -type f"
    if route == "grep":
        return "rg --no-heading -n alpha ."
    if route == "wc":
        return f"wc -l {sample_file}"
    if route == "summarize":
        return 'summarize --path . --focus "inspect moving tree"'
    raise ValueError(f"unknown G01 route: {route}")


class _MovingTreeWriter:
    """Continuous background writer mutating a tree during G01 probes."""

    def __init__(self, root_dir: Path, seed_int: int) -> None:
        self._root = root_dir
        self._seed = seed_int
        self._stop = threading.Event()
        self.writes: list[dict[str, object]] = []
        self._thread = threading.Thread(target=self._run, daemon=True)

    def start(self) -> None:
        self._thread.start()

    def stop(self) -> None:
        self._stop.set()
        self._thread.join(timeout=5.0)

    def _run(self) -> None:
        step = 0
        while not self._stop.is_set():
            rel = f"moving/churn_{step % 12}.txt"
            target = self._root / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            content = f"alpha moving_step_{step} seed_{self._seed}\n"
            target.write_text(content, encoding="utf-8")
            self.writes.append({"step": step, "op": "write", "path": rel, "ts_ns": time.monotonic_ns()})
            step += 1
            self._stop.wait(0.015)


def evaluate_g01_observation(
    route: str,
    call: CapturedToolCall,
    *,
    cold_no_complete_gen: bool,
    tree_mutated_during_call: bool,
) -> dict[str, object]:
    """Evaluate one G01 call against documented contract and record F-CF01-01 facts."""
    emitted_warming_code = call.rejection_code == "SURVEY_INVENTORY_WARMING"
    emitted_stale_mark = call.fresh is False or "stale_inventory" in call.declared_gaps
    emitted_refreshing = "catalog_refreshing" in call.declared_gaps or "catalog_warming" in call.declared_gaps

    if cold_no_complete_gen:
        # Documented contract: refusal with SURVEY_INVENTORY_WARMING when no generation complete.
        verdict = "PASS" if emitted_warming_code else "DEFECT"
        reason = (
            "emitted documented SURVEY_INVENTORY_WARMING refusal"
            if emitted_warming_code
            else f"F-CF01-01: cold {route} at completed_generation=0 did not emit SURVEY_INVENTORY_WARMING "
            f"(outcome={call.outcome}, rejection_code={call.rejection_code}, gaps={list(call.declared_gaps)})"
        )
    else:
        if call.outcome == "completed":
            if call.declared_gaps:
                verdict = "PASS_DECLARED_GAP"
                reason = f"{route} completed with declared gaps {list(call.declared_gaps)}"
            else:
                verdict = "PASS"
                reason = f"{route} completed cleanly"
        else:
            verdict = "DEFECT"
            reason = f"{route} failed unexpectedly: outcome={call.outcome}, code={call.rejection_code}"

    return {
        "route": route,
        "verdict": verdict,
        "reason": reason,
        "cold_no_complete_gen": cold_no_complete_gen,
        "tree_mutated_during_call": tree_mutated_during_call,
        "emitted_warming_code": emitted_warming_code,
        "emitted_stale_mark": emitted_stale_mark,
        "emitted_refreshing": emitted_refreshing,
        "declared_gaps": list(call.declared_gaps),
        "outcome": call.outcome,
        "rejection_code": call.rejection_code,
        "generation_id": call.generation_id,
        "fresh": call.fresh,
    }


def run_g01_sidecar(
    sidecar: D1SidecarProcess,
    *,
    base_seed: str,
    instances: int = 200,
    boundary_config: BoundaryConfig | None = None,
) -> dict[str, object]:
    """Run G01 sidecar instances (200) + 4 hand-written edges + G01-INC-03 incident replay."""
    assert sidecar.driver is not None
    verdict_counts: Counter[str] = Counter()
    gap_counts: Counter[str] = Counter()
    route_stats: dict[str, Counter[str]] = {r: Counter() for r in G01_COMMANDS}
    gap_samples: list[dict[str, object]] = []
    defects: list[dict[str, object]] = []

    with tempfile.TemporaryDirectory(prefix="cf07-g01-") as tmp:
        tmp_path = Path(tmp)
        # Share one project/session across small batches of moving-tree instances to keep
        # sidecar runtime brisk while still testing 200 distinct seeded mutations/routes.
        tree_dir = tmp_path / "moving_repo"
        selector0 = ReplaySelector(base_seed=base_seed, family_id="G01", instance_index=0)
        spec0 = generate_tree_spec(
            base_seed,
            "G01",
            0,
            TreeGenOptions(entry_count=16, max_depth=3, include_vcs=False, include_boundaries=False, include_nested_checkouts=False),
        )
        materialize_tree(spec0, tree_dir)
        project_id, root_ids = sidecar.driver.attach_project([tree_dir])
        wait_catalog_settled(sidecar.driver, project_id, root_ids[0], max_wait_s=15.0)
        writer = _MovingTreeWriter(tree_dir, seed_int=selector0.derived_seed)
        writer.start()
        try:
            sample_files = [e.path for e in spec0.entries if e.kind == "file"]
            fallback_file = sample_files[0] if sample_files else "moving/churn_0.txt"
            batch_size = 20
            for batch_start in range(0, instances, batch_size):
                batch_end = min(instances, batch_start + batch_size)
                session = AgentToolSession(sidecar, project_id)
                try:
                    for idx in range(batch_start, batch_end):
                        rng = seed_rng(base_seed, "G01", idx)
                        route = G01_COMMANDS[idx % len(G01_COMMANDS)]
                        # Perform an instance-specific mutation before each probe.
                        inst_rel = f"moving/inst_{idx % 25}.txt"
                        inst_abs = tree_dir / inst_rel
                        inst_abs.parent.mkdir(parents=True, exist_ok=True)
                        inst_abs.write_text(f"alpha inst_{idx} r={rng.randint(1, 9999)}\n", encoding="utf-8")

                        cmd = _command_for_route(route, inst_rel if route == "wc" else fallback_file)
                        writes_before = len(writer.writes)
                        call = session.invoke_tool("command", {"command": cmd})
                        mutated = len(writer.writes) >= writes_before
                        ev = evaluate_g01_observation(
                            route,
                            call,
                            cold_no_complete_gen=False,
                            tree_mutated_during_call=mutated,
                        )
                        verdict_counts[str(ev["verdict"])] += 1
                        route_stats[route][str(ev["verdict"])] += 1
                        for g in call.declared_gaps:
                            gap_counts[g] += 1
                        if call.declared_gaps and len(gap_samples) < 12:
                            gap_samples.append({"instance": idx, "route": route, "call": call.to_dict()})
                        if ev["verdict"] == "DEFECT" and len(defects) < 10:
                            defects.append({"instance": idx, "evaluation": ev, "call": call.to_dict()})
                finally:
                    session.close()
        finally:
            writer.stop()

        # 4 hand-written edges + G01-INC-03 cold boundary (2,000-entry tree queried immediately at completed_generation=0).
        cold_dir = tmp_path / "cold_2000_repo"
        cold_dir.mkdir(parents=True, exist_ok=True)
        for d in range(20):
            sub = cold_dir / f"pkg_{d:02d}"
            sub.mkdir(parents=True, exist_ok=True)
            for f in range(100):
                (sub / f"file_{f:03d}.txt").write_text(f"alpha cold_{d}_{f}\n", encoding="utf-8")
        cold_ref_count = len(
            [e for e in reference_walk(cold_dir, boundary_config=boundary_config, include_hidden=False) if e.kind == "file"]
        )
        cold_pid, cold_rids = sidecar.driver.attach_project([cold_dir])
        cold_index_before = sidecar.driver.get_source_index(cold_pid)
        cold_session = AgentToolSession(sidecar, cold_pid)
        edge_results: list[dict[str, object]] = []
        inc03_routes: dict[str, dict[str, object]] = {}
        try:
            for route in G01_COMMANDS:
                idx_snap = sidecar.driver.get_source_index(cold_pid)
                cov_list = idx_snap.get("coverage") or []
                is_warming = any(
                    isinstance(c, dict) and str(c.get("state") or "").lower() == "warming"
                    for c in cov_list
                ) or str(idx_snap.get("state") or "").lower() == "warming"
                cmd = _command_for_route(route, "pkg_00/file_000.txt")
                call = cold_session.invoke_tool("command", {"command": cmd})
                ev = evaluate_g01_observation(
                    route,
                    call,
                    cold_no_complete_gen=is_warming,
                    tree_mutated_during_call=False,
                )
                edge_rec = {
                    "edge": f"G01-edge-cold-{route}",
                    "index_before": idx_snap,
                    "observed_warming_state": is_warming,
                    "evaluation": ev,
                    "captured_call": call.to_dict(),
                }
                edge_results.append(edge_rec)
                inc03_routes[route] = edge_rec
                for g in call.declared_gaps:
                    gap_counts[g] += 1
        finally:
            cold_session.close()

    return {
        "family": "G01",
        "lane": "sidecar",
        "base_seed": base_seed,
        "planned_instances": instances,
        "executed_instances": sum(verdict_counts.values()),
        "verdict_counts": dict(verdict_counts),
        "route_stats": {k: dict(v) for k, v in route_stats.items()},
        "declared_gap_counts": dict(gap_counts),
        "declared_gap_samples": gap_samples,
        "defects": defects,
        "edges": edge_results,
        "incident_replay": {
            "slot": "G01-INC-03",
            "cold_tree_files": cold_ref_count,
            "initial_index_snapshot": cold_index_before,
            "routes": inc03_routes,
        },
    }
