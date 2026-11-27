"""CF07 G04 · Agent and editor agree (command rg/grep vs /v1/search on D1)."""

from __future__ import annotations

from collections import Counter
from pathlib import Path
import shlex
import tempfile

from ..generators import DEFAULT_TEXT_TOKENS, TreeEntrySpec, TreeGenOptions, TreeSpec, generate_tree_spec, materialize_tree
from ..live_controls import wait_catalog_settled
from ..queries import CodeQuerySpec
from ..references import reference_search
from ..seeds import ReplaySelector, seed_rng
from .cf07_harness import AgentToolSession, CapturedToolCall, D1SidecarProcess


def parse_rg_path_lines(stdout: str) -> set[tuple[str, int]]:
    """Parse `rg --no-heading -n -H` / `grep -rnH` lines into normalized (path, line) pairs."""
    pairs: set[tuple[str, int]] = set()
    for raw_line in stdout.splitlines():
        parts = raw_line.split(":", 2)
        if len(parts) < 2:
            continue
        path = parts[0].strip().removeprefix("./")
        try:
            line_no = int(parts[1].strip())
        except ValueError:
            continue
        if path:
            pairs.add((path, line_no))
    return pairs


def extract_search_stage_path_lines(search_resp: dict[str, object]) -> set[tuple[str, int]]:
    """Extract normalized (path, line) pairs for code hits from D1 POST /v1/search."""
    pairs: set[tuple[str, int]] = set()
    for hit in search_resp.get("hits") or []:
        if not isinstance(hit, dict):
            continue
        kind = str(hit.get("hit_kind") or "code").lower()
        if kind not in ("code", "source_code", ""):
            continue
        path = str(hit.get("path") or "").removeprefix("./")
        line_no = int(hit.get("line") or 0)
        if path and line_no > 0:
            pairs.add((path, line_no))
    return pairs


def _apply_agent_exclusions(pairs: set[tuple[str, int]]) -> set[tuple[str, int]]:
    """Apply declared agent exclusions (.paintedwolf and hidden files) for fair comparison."""
    out: set[tuple[str, int]] = set()
    for p, ln in pairs:
        segs = p.split("/")
        if any(s.startswith(".") for s in segs):
            continue
        out.add((p, ln))
    return out


def evaluate_g04_agreement(
    agent_call: CapturedToolCall,
    search_resp: dict[str, object],
    ref_pairs: set[tuple[str, int]],
) -> dict[str, object]:
    """Compare agent command route and /v1/search stage against each other and rg reference."""
    agent_raw = parse_rg_path_lines(agent_call.stdout)
    stage_raw = extract_search_stage_path_lines(search_resp)

    agent_proj = _apply_agent_exclusions(agent_raw)
    stage_proj = _apply_agent_exclusions(stage_raw)
    ref_proj = _apply_agent_exclusions(ref_pairs)

    stage_issues = [
        str(i.get("reason") or "")
        for i in (search_resp.get("issues") or [])
        if isinstance(i, dict) and i.get("reason")
    ]
    declared_gaps = tuple(dict.fromkeys((*agent_call.declared_gaps, *stage_issues)))

    agent_vs_stage_only_agent = sorted(agent_proj - stage_proj)
    agent_vs_stage_only_stage = sorted(stage_proj - agent_proj)
    agent_extra_vs_ref = sorted(agent_proj - ref_proj)
    stage_extra_vs_ref = sorted(stage_proj - ref_proj)

    issues: list[str] = []
    if agent_extra_vs_ref:
        issues.append(f"agent route claimed hits absent from reference: {agent_extra_vs_ref[:5]}")
    if stage_extra_vs_ref:
        issues.append(f"search stage claimed hits absent from reference: {stage_extra_vs_ref[:5]}")
    if agent_vs_stage_only_agent or agent_vs_stage_only_stage:
        if not declared_gaps:
            issues.append(
                f"agent vs search stage disagreement without declared gap: "
                f"only_agent={len(agent_vs_stage_only_agent)} only_stage={len(agent_vs_stage_only_stage)}"
            )

    if issues:
        verdict = "DEFECT"
    elif declared_gaps:
        verdict = "PASS_DECLARED_GAP"
    else:
        verdict = "PASS"

    return {
        "verdict": verdict,
        "issues": issues,
        "agent_count": len(agent_proj),
        "stage_count": len(stage_proj),
        "reference_count": len(ref_proj),
        "only_agent_sample": agent_vs_stage_only_agent[:5],
        "only_stage_sample": agent_vs_stage_only_stage[:5],
        "declared_gaps": list(declared_gaps),
    }


def run_g04_sidecar(
    sidecar: D1SidecarProcess,
    *,
    base_seed: str,
    instances: int = 100,
    rg_bin: str = "rg",
) -> dict[str, object]:
    """Run G04 sidecar instances (100 generated corpora & literals) + 2 hand-written edges."""
    assert sidecar.driver is not None
    verdict_counts: Counter[str] = Counter()
    gap_counts: Counter[str] = Counter()
    gap_samples: list[dict[str, object]] = []
    defects: list[dict[str, object]] = []

    with tempfile.TemporaryDirectory(prefix="cf07-g04-") as tmp:
        tmp_path = Path(tmp)
        for idx in range(instances):
            selector = ReplaySelector(base_seed=base_seed, family_id="G04", instance_index=idx)
            rng = seed_rng(base_seed, "G04", idx)
            spec = generate_tree_spec(
                base_seed,
                "G04",
                idx,
                TreeGenOptions(
                    entry_count=10 + (idx % 16),
                    max_depth=4,
                    include_hidden=True,
                    include_boundaries=False,
                    include_nested_checkouts=False,
                    include_paintedwolf=(idx % 4 == 0),
                ),
            )
            tree_dir = tmp_path / f"g04_{idx:03d}"
            materialize_tree(spec, tree_dir)
            literal = DEFAULT_TEXT_TOKENS[rng.randint(0, len(DEFAULT_TEXT_TOKENS) - 1)]

            pid, rids = sidecar.driver.attach_project([tree_dir])
            wait_catalog_settled(sidecar.driver, pid, rids[0], max_wait_s=15.0)
            session = AgentToolSession(sidecar, pid)
            try:
                cmd = f"rg --no-heading -n -H -F -- {shlex.quote(literal)} ."
                agent_call = session.invoke_tool("command", {"command": cmd})
                search_resp = sidecar.driver.search_raw(
                    literal,
                    origin_project_id=pid,
                    regex=False,
                    case_sensitive=True,
                    limit=500,
                )
            finally:
                session.close()

            ref_res = reference_search(
                tree_dir,
                CodeQuerySpec(pattern=literal, form="literal", case_sensitive=True),
                include_hidden=False,
                rg_bin=rg_bin,
            )
            ref_pairs = {(h.path.removeprefix("./"), h.line_number) for h in ref_res.hits}
            ev = evaluate_g04_agreement(agent_call, search_resp, ref_pairs)
            verdict_counts[str(ev["verdict"])] += 1
            for g in ev["declared_gaps"]:
                gap_counts[str(g)] += 1
            if ev["declared_gaps"] and len(gap_samples) < 10:
                gap_samples.append(
                    {
                        "instance": idx,
                        "literal": literal,
                        "agent_call": agent_call.to_dict(),
                        "search_resp": search_resp,
                    }
                )
            if ev["verdict"] == "DEFECT" and len(defects) < 10:
                defects.append(
                    {
                        "instance": idx,
                        "selector": selector.to_dict(),
                        "literal": literal,
                        "evaluation": ev,
                        "agent_call": agent_call.to_dict(),
                        "search_resp": search_resp,
                    }
                )

        # 2 hand-written edges: hidden file exclusion & .paintedwolf exclusion.
        edge_specs = [
            (
                "G04-edge-hidden-exclusion",
                (
                    TreeEntrySpec("src/visible.txt", "file", b"needle_edge_hidden visible\n"),
                    TreeEntrySpec(".hidden_file.txt", "file", b"needle_edge_hidden hidden\n"),
                ),
                "needle_edge_hidden",
            ),
            (
                "G04-edge-paintedwolf-exclusion",
                (
                    TreeEntrySpec("src/visible.txt", "file", b"needle_edge_pw visible\n"),
                    TreeEntrySpec(".paintedwolf/notes.txt", "file", b"needle_edge_pw pw\n"),
                ),
                "needle_edge_pw",
            ),
        ]
        edge_results: list[dict[str, object]] = []
        for edge_name, entries, literal in edge_specs:
            edir = tmp_path / edge_name
            spec = TreeSpec(ReplaySelector(base_seed, "G04", 9000 + len(edge_results)), entries)
            materialize_tree(spec, edir)
            pid, rids = sidecar.driver.attach_project([edir])
            wait_catalog_settled(sidecar.driver, pid, rids[0], max_wait_s=15.0)
            session = AgentToolSession(sidecar, pid)
            try:
                cmd = f"rg --no-heading -n -H -F -- {shlex.quote(literal)} ."
                agent_call = session.invoke_tool("command", {"command": cmd})
                search_resp = sidecar.driver.search_raw(
                    literal,
                    origin_project_id=pid,
                    regex=False,
                    case_sensitive=True,
                    limit=500,
                )
            finally:
                session.close()
            ref_res = reference_search(
                edir,
                CodeQuerySpec(pattern=literal, form="literal", case_sensitive=True),
                include_hidden=False,
                rg_bin=rg_bin,
            )
            ref_pairs = {(h.path.removeprefix("./"), h.line_number) for h in ref_res.hits}
            ev = evaluate_g04_agreement(agent_call, search_resp, ref_pairs)
            edge_results.append(
                {
                    "edge": edge_name,
                    "literal": literal,
                    "evaluation": ev,
                    "agent_call": agent_call.to_dict(),
                    "search_resp": search_resp,
                }
            )

    return {
        "family": "G04",
        "lane": "sidecar",
        "base_seed": base_seed,
        "planned_instances": instances,
        "executed_instances": sum(verdict_counts.values()),
        "verdict_counts": dict(verdict_counts),
        "declared_gap_counts": dict(gap_counts),
        "declared_gap_samples": gap_samples,
        "defects": defects,
        "edges": edge_results,
    }
