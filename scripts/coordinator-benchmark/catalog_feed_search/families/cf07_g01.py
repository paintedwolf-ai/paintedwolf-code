"""CF07 G01 · Freshness under a moving tree (sidecar lane + G01-INC-03)."""

from __future__ import annotations

from collections import Counter
import json
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
        return "rg --no-heading -n -H alpha ."
    if route == "wc":
        return f"wc -l {sample_file}"
    if route == "summarize":
        return 'summarize --path . --focus "inspect moving tree"'
    raise ValueError(f"unknown G01 route: {route}")


def _parse_find_paths(stdout: str) -> set[str]:
    out: set[str] = set()
    for line in stdout.splitlines():
        p = line.strip().removeprefix("./")
        if p and not p.startswith("."):
            out.add(p)
    return out


def _parse_grep_files(stdout: str) -> set[str]:
    out: set[str] = set()
    for line in stdout.splitlines():
        parts = line.split(":", 2)
        if len(parts) >= 2:
            p = parts[0].strip().removeprefix("./")
            if p:
                out.add(p)
    return out


def _parse_wc_lines(stdout: str) -> int | None:
    for line in stdout.splitlines():
        parts = line.strip().split()
        if parts:
            try:
                return int(parts[0])
            except ValueError:
                continue
    return None


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
    ref_files_before: set[str] | None = None,
    ref_files_after: set[str] | None = None,
    expected_wc_lines: set[int] | None = None,
) -> dict[str, object]:
    """Evaluate one G01 call against route-appropriate reference projection and freshness contract."""
    emitted_warming_code = call.rejection_code == "SURVEY_INVENTORY_WARMING"
    emitted_stale_mark = call.fresh is False or "stale_inventory" in call.declared_gaps
    emitted_refreshing = "catalog_refreshing" in call.declared_gaps or "catalog_warming" in call.declared_gaps
    issues: list[str] = []
    answer_matches_after = True

    if cold_no_complete_gen:
        verdict = "PASS" if emitted_warming_code else "DEFECT"
        reason = (
            "emitted documented SURVEY_INVENTORY_WARMING refusal"
            if emitted_warming_code
            else f"F-CF01-01: cold {route} at completed_generation=0 did not emit SURVEY_INVENTORY_WARMING "
            f"(outcome={call.outcome}, rejection_code={call.rejection_code}, gaps={list(call.declared_gaps)})"
        )
        return {
            "route": route,
            "verdict": verdict,
            "reason": reason,
            "cold_no_complete_gen": True,
            "tree_mutated_during_call": tree_mutated_during_call,
            "emitted_warming_code": emitted_warming_code,
            "emitted_stale_mark": emitted_stale_mark,
            "emitted_refreshing": emitted_refreshing,
            "declared_gaps": list(call.declared_gaps),
            "outcome": call.outcome,
            "rejection_code": call.rejection_code,
            "generation_id": call.generation_id,
            "fresh": call.fresh,
            "issues": [] if verdict == "PASS" else [reason],
        }

    if call.outcome != "completed":
        issues.append(f"{route} failed unexpectedly: outcome={call.outcome}, code={call.rejection_code}")
    else:
        union_ref = (ref_files_before or set()) | (ref_files_after or set())
        if route == "find" and (ref_files_before is not None or ref_files_after is not None):
            observed = _parse_find_paths(call.stdout)
            extra = sorted(observed - union_ref)
            if extra:
                issues.append(f"find claimed paths absent from reference window: {extra[:5]}")
            if ref_files_after is not None and observed != ref_files_after:
                answer_matches_after = False
                if ref_files_before is not None and observed != ref_files_before and not call.declared_gaps:
                    # Under a moving tree, an answer may observe a valid intermediate snapshot
                    # between before and after; only fail if it omits files present in BOTH.
                    always_present = ref_files_before & ref_files_after
                    missing_stable = sorted(always_present - observed)
                    if missing_stable:
                        issues.append(f"find omitted stable files without declared gap: {missing_stable[:5]}")
        elif route == "grep" and (ref_files_before is not None or ref_files_after is not None):
            observed = _parse_grep_files(call.stdout)
            extra = sorted(observed - union_ref)
            if extra:
                issues.append(f"grep claimed files absent from reference window: {extra[:5]}")
            if not observed and union_ref and not call.declared_gaps:
                issues.append("grep returned zero matches on non-empty matching reference without declared gap")
        elif route == "wc" and expected_wc_lines is not None:
            got_lines = _parse_wc_lines(call.stdout)
            if got_lines is None or got_lines not in expected_wc_lines:
                issues.append(f"wc line count {got_lines!r} not in expected set {sorted(expected_wc_lines)}")
        elif route == "summarize":
            try:
                parsed = json.loads(call.stdout.strip()) if call.stdout.strip().startswith("{") else None
            except ValueError:
                parsed = None
            if not isinstance(parsed, dict) or "coverage" not in parsed or "gather" not in parsed:
                issues.append("summarize did not return structured SummarizeResponse with coverage and gather")

        # Freshness invariant: if the tree moved past the answer (answer != post-call reference)
        # AND tree_mutated_during_call is True, contract requires stale/refreshing declaration.
        if tree_mutated_during_call and not answer_matches_after and not (emitted_stale_mark or emitted_refreshing or call.declared_gaps):
            issues.append(f"{route} served stale snapshot under active mutation without declaring inventory.fresh=false or refreshing gap")

    if issues:
        verdict = "DEFECT"
        reason = "; ".join(issues)
    elif call.declared_gaps:
        verdict = "PASS_DECLARED_GAP"
        reason = f"{route} matched reference projection with declared gaps {list(call.declared_gaps)}"
    else:
        verdict = "PASS"
        reason = f"{route} matched reference projection cleanly"

    return {
        "route": route,
        "verdict": verdict,
        "reason": reason,
        "cold_no_complete_gen": False,
        "tree_mutated_during_call": tree_mutated_during_call,
        "emitted_warming_code": emitted_warming_code,
        "emitted_stale_mark": emitted_stale_mark,
        "emitted_refreshing": emitted_refreshing,
        "declared_gaps": list(call.declared_gaps),
        "outcome": call.outcome,
        "rejection_code": call.rejection_code,
        "generation_id": call.generation_id,
        "fresh": call.fresh,
        "issues": issues,
    }


def _walk_visible_files(root_dir: Path, boundary_config: BoundaryConfig | None) -> set[str]:
    return {
        e.path
        for e in reference_walk(
            root_dir,
            boundary_config=boundary_config,
            include_hidden=False,
            agent_exclusions=(".paintedwolf", ".paintedwolf/**"),
        )
        if e.kind == "file"
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
    mutated_instances = 0

    with tempfile.TemporaryDirectory(prefix="cf07-g01-") as tmp:
        tmp_path = Path(tmp)
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
                        inst_rel = f"moving/inst_{idx % 25}.txt"
                        inst_abs = tree_dir / inst_rel
                        inst_abs.parent.mkdir(parents=True, exist_ok=True)
                        line_cnt = 1 + (idx % 3)
                        inst_abs.write_text(
                            "".join(f"alpha inst_{idx} line_{ln} r={rng.randint(1, 9999)}\n" for ln in range(line_cnt)),
                            encoding="utf-8",
                        )

                        ref_before = _walk_visible_files(tree_dir, boundary_config)
                        writes_before = len(writer.writes)
                        cmd = _command_for_route(route, inst_rel if route == "wc" else fallback_file)
                        call = session.invoke_tool("command", {"command": cmd})
                        writes_after = len(writer.writes)
                        ref_after = _walk_visible_files(tree_dir, boundary_config)

                        # Strictly > 0 background writes proves concurrent tree mutation during call.
                        mutated = writes_after > writes_before
                        if mutated:
                            mutated_instances += 1

                        ev = evaluate_g01_observation(
                            route,
                            call,
                            cold_no_complete_gen=False,
                            tree_mutated_during_call=mutated,
                            ref_files_before=ref_before,
                            ref_files_after=ref_after,
                            expected_wc_lines={line_cnt} if route == "wc" else None,
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

        # 4 hand-written edges + G01-INC-03 cold boundary.
        cold_dir = tmp_path / "cold_2000_repo"
        cold_dir.mkdir(parents=True, exist_ok=True)
        for d in range(20):
            sub = cold_dir / f"pkg_{d:02d}"
            sub.mkdir(parents=True, exist_ok=True)
            for f in range(100):
                (sub / f"file_{f:03d}.txt").write_text(f"alpha cold_{d}_{f}\n", encoding="utf-8")
        cold_ref_files = _walk_visible_files(cold_dir, boundary_config)
        cold_pid, _ = sidecar.driver.attach_project([cold_dir])
        cold_index_before = sidecar.driver.get_source_index(cold_pid)
        cold_session = AgentToolSession(sidecar, cold_pid)
        edge_results: list[dict[str, object]] = []
        inc03_routes: dict[str, dict[str, object]] = {}
        any_route_warming = False
        try:
            for route in G01_COMMANDS:
                idx_snap = sidecar.driver.get_source_index(cold_pid)
                cov_list = idx_snap.get("coverage") or []
                is_warming = any(
                    isinstance(c, dict) and str(c.get("state") or "").lower() == "warming"
                    for c in cov_list
                ) or str(idx_snap.get("state") or "").lower() == "warming"
                if is_warming:
                    any_route_warming = True
                cmd = _command_for_route(route, "pkg_00/file_000.txt")
                call = cold_session.invoke_tool("command", {"command": cmd})
                ev = evaluate_g01_observation(
                    route,
                    call,
                    cold_no_complete_gen=is_warming,
                    tree_mutated_during_call=False,
                    ref_files_before=cold_ref_files,
                    ref_files_after=cold_ref_files,
                    expected_wc_lines={1} if route == "wc" else None,
                )
                disposition = (
                    "confirmed_cold_boundary"
                    if is_warming
                    else "unconfirmed_settled_before_invocation"
                )
                edge_rec = {
                    "edge": f"G01-edge-cold-{route}",
                    "disposition": disposition,
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
        "mutated_instances": mutated_instances,
        "verdict_counts": dict(verdict_counts),
        "route_stats": {k: dict(v) for k, v in route_stats.items()},
        "declared_gap_counts": dict(gap_counts),
        "declared_gap_samples": gap_samples,
        "defects": defects,
        "edges": edge_results,
        "incident_replay": {
            "slot": "G01-INC-03",
            "sidecar_disposition": (
                "confirmed_cold_boundary"
                if any_route_warming
                else "unconfirmed_sidecar_settled_during_session_boot_see_inprocess_evidence"
            ),
            "limitation": (
                "Sidecar session boot and turn admission take ~1.5-2s, allowing a 2,000-file "
                "tree to finish initial scan before tool dispatch; see typed in-process G01 "
                "barrier and real-route evidence for deterministic completed_generation=0 proof."
            ),
            "cold_tree_files": len(cold_ref_files),
            "initial_index_snapshot": cold_index_before,
            "routes": inc03_routes,
        },
    }
