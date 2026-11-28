"""CF07 G04 · Agent and editor agree (independent route-vs-reference + common generation)."""

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


def extract_search_stage_path_lines(
    search_resp: dict[str, object],
    *,
    project_id: str | None = None,
) -> set[tuple[str, int]]:
    """Extract normalized (path, line) pairs for code hits from D1 POST /v1/search."""
    pairs: set[tuple[str, int]] = set()
    for hit in search_resp.get("hits") or []:
        if not isinstance(hit, dict):
            continue
        if project_id and str(hit.get("project_id") or "") not in ("", project_id):
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


def is_async_detached_handle(call: CapturedToolCall) -> bool:
    """Return True if D1 `command` detached into a background handle (`state: running`)."""
    blob = call.model_facing_message + str(call.tool_result_json.get("content") or "")
    return ('"state":"running"' in blob) and ('"handle":' in blob)


def _stage_code_gaps(search_resp: dict[str, object]) -> tuple[str, ...]:
    """Return ONLY typed gaps scoped to the code leg (symbol_pending never excuses code omissions)."""
    gaps: list[str] = []
    for item in search_resp.get("issues") or []:
        if not isinstance(item, dict):
            continue
        reason = str(item.get("reason") or "")
        executor = str(item.get("executor") or "").lower()
        if not reason:
            continue
        if executor in ("", "code", "code_search") and reason not in ("symbol_pending", "symbol_budget"):
            gaps.append(reason)
    return tuple(dict.fromkeys(gaps))


def evaluate_g04_agreement(
    agent_call: CapturedToolCall,
    search_resp: dict[str, object],
    ref_pairs: set[tuple[str, int]],
    *,
    project_id: str | None = None,
    require_nonempty_reference: bool = True,
    generation_before: int | None = None,
    generation_after: int | None = None,
) -> dict[str, object]:
    """Compare agent route and /v1/search INDEPENDENTLY against projected reference with strict route-scoped gaps."""
    agent_raw = parse_rg_path_lines(agent_call.stdout)
    stage_raw = extract_search_stage_path_lines(search_resp, project_id=project_id)

    agent_proj = _apply_agent_exclusions(agent_raw)
    stage_proj = _apply_agent_exclusions(stage_raw)
    ref_proj = _apply_agent_exclusions(ref_pairs)

    agent_gaps = tuple(agent_call.declared_gaps)
    stage_gaps = _stage_code_gaps(search_resp)
    all_stage_issues = tuple(
        dict.fromkeys(
            str(i.get("reason") or "")
            for i in (search_resp.get("issues") or [])
            if isinstance(i, dict) and i.get("reason")
        )
    )
    declared_gaps = tuple(dict.fromkeys((*agent_gaps, *all_stage_issues)))

    issues: list[str] = []
    if require_nonempty_reference and not ref_proj:
        issues.append("positive control violated: reference hit set is empty")

    generation_stable = (
        generation_before is None
        or generation_after is None
        or generation_before == generation_after
    )
    if not generation_stable:
        issues.append(f"catalog generation drifted during G04 pair: {generation_before} -> {generation_after}")

    # 1. Agent route vs reference (only agent_gaps may excuse agent omissions).
    agent_extra = sorted(agent_proj - ref_proj)
    agent_missing = sorted(ref_proj - agent_proj)
    if agent_extra:
        issues.append(f"agent route claimed hits absent from reference: {agent_extra[:5]}")
    if agent_missing and not agent_gaps:
        issues.append(f"agent route omitted {len(agent_missing)} reference hits without agent declared gap: {agent_missing[:5]}")

    # 2. Search stage vs reference (only code-leg stage_gaps may excuse stage omissions).
    stage_extra = sorted(stage_proj - ref_proj)
    stage_missing = sorted(ref_proj - stage_proj)
    if stage_extra:
        issues.append(f"search stage claimed hits absent from reference: {stage_extra[:5]}")
    if stage_missing and not stage_gaps:
        issues.append(f"search stage omitted {len(stage_missing)} reference hits without code-leg declared gap: {stage_missing[:5]}")

    if issues:
        verdict = "DEFECT"
    elif (agent_missing and agent_gaps) or (stage_missing and stage_gaps) or declared_gaps:
        verdict = "PASS_DECLARED_GAP"
    else:
        verdict = "PASS"

    return {
        "verdict": verdict,
        "issues": issues,
        "agent_count": len(agent_proj),
        "stage_count": len(stage_proj),
        "reference_count": len(ref_proj),
        "agent_missing_count": len(agent_missing),
        "stage_missing_count": len(stage_missing),
        "agent_gaps": list(agent_gaps),
        "stage_code_gaps": list(stage_gaps),
        "declared_gaps": list(declared_gaps),
        "generation_before": generation_before,
        "generation_after": generation_after,
        "generation_stable": generation_stable,
        "async_detached_handle": is_async_detached_handle(agent_call),
    }


def _pick_positive_literal(tree_dir: Path, rng, rg_bin: str) -> tuple[str, set[tuple[str, int]]]:
    """Select a literal guaranteed to have >=1 visible hit in tree_dir."""
    candidates = list(DEFAULT_TEXT_TOKENS)
    rng.shuffle(candidates)
    for tok in candidates:
        res = reference_search(
            tree_dir,
            CodeQuerySpec(pattern=tok, form="literal", case_sensitive=True),
            include_hidden=False,
            rg_bin=rg_bin,
        )
        pairs = _apply_agent_exclusions({(h.path.removeprefix("./"), h.line_number) for h in res.hits})
        if pairs:
            return tok, pairs
    # Deterministic fallback: guarantee a visible positive anchor file.
    anchor = tree_dir / "src/positive_anchor.txt"
    anchor.parent.mkdir(parents=True, exist_ok=True)
    anchor.write_text("alpha positive_anchor_line\n", encoding="utf-8")
    res = reference_search(
        tree_dir,
        CodeQuerySpec(pattern="alpha", form="literal", case_sensitive=True),
        include_hidden=False,
        rg_bin=rg_bin,
    )
    pairs = _apply_agent_exclusions({(h.path.removeprefix("./"), h.line_number) for h in res.hits})
    return "alpha", pairs


def run_g04_sidecar(
    sidecar: D1SidecarProcess,
    *,
    base_seed: str,
    instances: int = 100,
    rg_bin: str = "rg",
) -> dict[str, object]:
    """Run G04 sidecar instances (100 positive corpora & literals) + 2 hand-written edges."""
    assert sidecar.driver is not None
    verdict_counts: Counter[str] = Counter()
    gap_counts: Counter[str] = Counter()
    gap_samples: list[dict[str, object]] = []
    defects: list[dict[str, object]] = []
    detached_replays: list[dict[str, object]] = []

    with tempfile.TemporaryDirectory(prefix="cf07-g04-") as tmp:
        tmp_path = Path(tmp)
        batch_size = 20
        for batch_start in range(0, instances, batch_size):
            batch_end = min(instances, batch_start + batch_size)
            batch_dir = tmp_path / f"batch_{batch_start:03d}"
            batch_dir.mkdir(parents=True, exist_ok=True)
            batch_meta: list[tuple[int, ReplaySelector, str, str, set[tuple[str, int]]]] = []
            for idx in range(batch_start, batch_end):
                selector = ReplaySelector(base_seed=base_seed, family_id="G04", instance_index=idx)
                rng = seed_rng(base_seed, "G04", idx)
                spec = generate_tree_spec(
                    base_seed,
                    "G04",
                    idx,
                    TreeGenOptions(
                        entry_count=10 + (idx % 12),
                        max_depth=4,
                        include_hidden=True,
                        include_ignored=False,
                        include_vcs=False,
                        include_boundaries=False,
                        include_nested_checkouts=False,
                        include_paintedwolf=(idx % 4 == 0),
                    ),
                )
                sub_name = f"corp_{idx:03d}"
                tree_dir = batch_dir / sub_name
                materialize_tree(spec, tree_dir)
                literal, local_pairs = _pick_positive_literal(tree_dir, rng, rg_bin)
                scoped_pairs = {(f"{sub_name}/{p}", ln) for p, ln in local_pairs}
                batch_meta.append((idx, selector, sub_name, literal, scoped_pairs))

            pid, rids = sidecar.driver.attach_project([batch_dir])
            wait_catalog_settled(sidecar.driver, pid, rids[0], max_wait_s=25.0)
            session = AgentToolSession(sidecar, pid)
            try:
                for idx, selector, sub_name, literal, ref_pairs in batch_meta:
                    idx_before = sidecar.driver.get_source_index(pid)
                    rev_before = int(idx_before["revision"]) if idx_before.get("revision") is not None else None

                    cmd = f"rg --no-heading -n -H -F -- {shlex.quote(literal)} {sub_name}"
                    agent_call = session.invoke_tool("command", {"command": cmd})
                    if is_async_detached_handle(agent_call):
                        # Record cold async handle detach and execute warm positive replay on same generation.
                        warm_call = session.invoke_tool("command", {"command": cmd})
                        detached_replays.append(
                            {
                                "instance": idx,
                                "literal": literal,
                                "cold_detached_call": agent_call.to_dict(),
                                "warm_replay_call": warm_call.to_dict(),
                            }
                        )
                        agent_call = warm_call

                    search_resp = sidecar.driver.search_raw(
                        literal,
                        origin_project_id=pid,
                        regex=False,
                        case_sensitive=True,
                        include=(f"{sub_name}/**",),
                        limit=500,
                    )
                    idx_after = sidecar.driver.get_source_index(pid)
                    rev_after = int(idx_after["revision"]) if idx_after.get("revision") is not None else None

                    ev = evaluate_g04_agreement(
                        agent_call,
                        search_resp,
                        ref_pairs,
                        project_id=pid,
                        require_nonempty_reference=True,
                        generation_before=rev_before,
                        generation_after=rev_after,
                    )
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
            finally:
                session.close()

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
                idx_before = sidecar.driver.get_source_index(pid)
                rev_before = int(idx_before["revision"]) if idx_before.get("revision") is not None else None
                cmd = f"rg --no-heading -n -H -F -- {shlex.quote(literal)} ."
                agent_call = session.invoke_tool("command", {"command": cmd})
                search_resp = sidecar.driver.search_raw(
                    literal,
                    origin_project_id=pid,
                    regex=False,
                    case_sensitive=True,
                    limit=500,
                )
                idx_after = sidecar.driver.get_source_index(pid)
                rev_after = int(idx_after["revision"]) if idx_after.get("revision") is not None else None
            finally:
                session.close()
            ref_res = reference_search(
                edir,
                CodeQuerySpec(pattern=literal, form="literal", case_sensitive=True),
                include_hidden=False,
                rg_bin=rg_bin,
            )
            ref_pairs = {(h.path.removeprefix("./"), h.line_number) for h in ref_res.hits}
            ev = evaluate_g04_agreement(
                agent_call,
                search_resp,
                ref_pairs,
                project_id=pid,
                require_nonempty_reference=True,
                generation_before=rev_before,
                generation_after=rev_after,
            )
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
        "detached_cold_replays": detached_replays,
        "defects": defects,
        "edges": edge_results,
    }
