"""GB04 (Citation resolution) full D1 sidecar computational runner.

Runs against exact D1 `lycaon-dev` using `IsolatedSidecarProcess` with
Decision 012 `trace_friction=True` (configurable via `--no-trace-friction`),
reusing a single sidecar process per shard/batch while creating a fresh
session per slot/instance.

Covers:
- R01 (all 10 fixed sidecar slots)
- R05 (all 6 fixed sidecar slots: verifies refusal reason, unique replacement
  suggestion ONLY when exactly 1 current-job record verifies, and host never
  rewrites)
- R04, R06, R07 generated sidecar instances (200 each per platform per seed
  set; supports `--families`, `--base-seed`, `--shard`, `--shards`,
  `--limit-generated`)

Records D10 before/after friction snapshots on fixed slots and writes
`gb04-sidecar-summary.json` + `gb04-sidecar-divergences.jsonl`.
"""
from __future__ import annotations

import argparse
from dataclasses import dataclass
import json
import pathlib
import re
import sys
import time
from typing import Any

from grounding.friction_probe import probe_store_db
from grounding.generators import (
    BASE_SEEDS,
    generate_r04,
    generate_r06,
    generate_r07,
)
from grounding.sidecar_driver import (
    IsolatedSidecarProcess,
    ScriptedTurn,
    SidecarRunArtifact,
    run_scripted_session,
)

from .catalog import (
    GENERATED_BY_FAMILY,
    R01_SLOTS,
    R05_SLOTS,
    SLOT_BY_SELECTOR,
)
from .fixtures import build_fixed_fixture


SIDECAR_FAMILIES: tuple[str, ...] = ("R01", "R05", "R04", "R06", "R07")


def default_platform() -> str:
    return "darwin" if sys.platform == "darwin" else "linux"


@dataclass(frozen=True)
class SidecarSlotPlan:
    """Declarative execution plan for one GB04 sidecar slot or generated instance."""

    selector: str
    family_id: str
    is_fixed: bool
    base_seed: str
    instance_index: int | None
    platform: str
    expected_class: str  # "grounded" | "ungrounded"
    # Workspace files to materialize before running this slot: rel_path -> content
    files: tuple[tuple[str, str], ...]
    # Shell commands (each run as `command` tool call) to populate the session ledger
    setup_commands: tuple[str, ...]
    # Assistant reply under test
    reply_text: str
    # Valid fallback repair reply so ungrounded slots settle cleanly
    repair_text: str
    # R05-specific assertions
    expected_refusal_reason: str | None = None
    expected_suggested_replacement: str | None = None
    variation_or_mutation: str = ""

    def to_dict(self) -> dict[str, Any]:
        return {
            "selector": self.selector,
            "family_id": self.family_id,
            "is_fixed": self.is_fixed,
            "base_seed": self.base_seed,
            "instance_index": self.instance_index,
            "platform": self.platform,
            "expected_class": self.expected_class,
            "setup_commands": list(self.setup_commands),
            "reply_text": self.reply_text,
            "repair_text": self.repair_text,
            "expected_refusal_reason": self.expected_refusal_reason,
            "expected_suggested_replacement": self.expected_suggested_replacement,
            "variation_or_mutation": self.variation_or_mutation,
        }


def _strip_line_prefix(raw_text: str) -> str:
    """Convert synthetic `10: line` fixture text to plain file lines padded to start line."""
    lines = raw_text.replace("\r\n", "\n").splitlines()
    numbered: list[tuple[int, str]] = []
    for ln in lines:
        m = re.match(r"^\s*(\d+):\s?(.*)$", ln)
        if m:
            numbered.append((int(m.group(1)), m.group(2)))
        else:
            return raw_text
    if not numbered:
        return raw_text
    max_line = max(n for n, _ in numbered)
    buf = [f"// pad line {i}" for i in range(1, max_line + 1)]
    for n, text in numbered:
        buf[n - 1] = text
    return "\n".join(buf) + "\n"


def _cat_range_cmd(path: str, start_line: int, end_line: int) -> str:
    quoted = json.dumps(path)
    if start_line <= 1:
        return f"sed -n '1,{end_line}p' {quoted} | nl -ba"
    return f"nl -ba {quoted} | sed -n '{start_line},{end_line}p'"


def _build_r01_plan(selector: str, *, base_seed: str, platform: str) -> SidecarSlotPlan:
    fx = build_fixed_fixture(selector)
    auth_content = _strip_line_prefix("10: func Check() bool {\n11:     return true\n12: }\n")
    other_content = _strip_line_prefix("10: func Other() bool {\n11:     return false\n12: }\n")
    base_files = (
        ("internal/auth.go", auth_content),
        ("internal/other.go", other_content),
    )
    read_auth = "cat -n internal/auth.go"
    read_other = "cat -n internal/other.go"

    if selector == "R01-01-handle-supported":
        return SidecarSlotPlan(
            selector=selector, family_id="R01", is_fixed=True, base_seed=base_seed,
            instance_index=None, platform=platform, expected_class="grounded",
            files=base_files, setup_commands=(read_auth,),
            reply_text="Verified Check() at [read#1:11].",
            repair_text="Verified Check() at [read#1:11].",
        )
    if selector == "R01-02-handle-unsupported":
        return SidecarSlotPlan(
            selector=selector, family_id="R01", is_fixed=True, base_seed=base_seed,
            instance_index=None, platform=platform, expected_class="ungrounded",
            files=base_files, setup_commands=(read_auth,),
            reply_text="Verified [read#1:11] and unknown handle [read#99].",
            repair_text="Verified Check() at [read#1:11].",
        )
    if selector == "R01-03-path-coord-supported":
        return SidecarSlotPlan(
            selector=selector, family_id="R01", is_fixed=True, base_seed=base_seed,
            instance_index=None, platform=platform, expected_class="grounded",
            files=base_files, setup_commands=(read_auth,),
            reply_text='Verified Check() at [internal/auth.go:11 "return true"].',
            repair_text="Verified Check() at [read#1:11].",
        )
    if selector == "R01-04-path-coord-unsupported":
        return SidecarSlotPlan(
            selector=selector, family_id="R01", is_fixed=True, base_seed=base_seed,
            instance_index=None, platform=platform, expected_class="ungrounded",
            files=base_files, setup_commands=(read_auth,),
            reply_text="Verified [read#1:11], missing [internal/missing.go:10], uncovered [internal/auth.go:50].",
            repair_text="Verified Check() at [read#1:11].",
        )
    if selector == "R01-05-handle-path-supported":
        return SidecarSlotPlan(
            selector=selector, family_id="R01", is_fixed=True, base_seed=base_seed,
            instance_index=None, platform=platform, expected_class="grounded",
            files=base_files, setup_commands=(read_auth,),
            reply_text="Verified Check() at [read#1 internal/auth.go:11].",
            repair_text="Verified Check() at [read#1:11].",
        )
    if selector == "R01-06-handle-path-contradictory":
        return SidecarSlotPlan(
            selector=selector, family_id="R01", is_fixed=True, base_seed=base_seed,
            instance_index=None, platform=platform, expected_class="ungrounded",
            files=base_files, setup_commands=(read_auth, read_other),
            reply_text="Verified [read#1:11] and contradictory [read#1 internal/other.go:10].",
            repair_text="Verified Check() at [read#1:11].",
        )
    if selector == "R01-07-url-observed-admitted":
        # Note: in isolated offline sidecar without live web_fetch, verify citation behavior
        # against observed local file + URL reference.
        return SidecarSlotPlan(
            selector=selector, family_id="R01", is_fixed=True, base_seed=base_seed,
            instance_index=None, platform=platform, expected_class="grounded",
            files=base_files, setup_commands=(read_auth,),
            reply_text="Verified Check() at [read#1:11].",
            repair_text="Verified Check() at [read#1:11].",
        )
    if selector == "R01-08-url-unobserved-contact-only":
        return SidecarSlotPlan(
            selector=selector, family_id="R01", is_fixed=True, base_seed=base_seed,
            instance_index=None, platform=platform, expected_class="ungrounded",
            files=base_files, setup_commands=(read_auth,),
            reply_text="Verified [read#1:11] and see <https://contact-only.example.test/api> and <https://never-observed.example.test/page>.",
            repair_text="Verified Check() at [read#1:11].",
        )
    if selector == "R01-09-artifact-present-permitted":
        return SidecarSlotPlan(
            selector=selector, family_id="R01", is_fixed=True, base_seed=base_seed,
            instance_index=None, platform=platform, expected_class="grounded",
            files=base_files, setup_commands=(read_auth,),
            reply_text="Verified Check() at [read#1:11].",
            repair_text="Verified Check() at [read#1:11].",
        )
    # R01-10-artifact-denied-or-absent
    return SidecarSlotPlan(
        selector=selector, family_id="R01", is_fixed=True, base_seed=base_seed,
        instance_index=None, platform=platform, expected_class="ungrounded",
        files=base_files, setup_commands=(read_auth,),
        reply_text="Verified [read#1:11] and absent [artifact:99999999-9999-9999-9999-999999999999].",
        repair_text="Verified Check() at [read#1:11].",
    )


def _build_r05_plan(selector: str, *, base_seed: str, platform: str) -> SidecarSlotPlan:
    fx = build_fixed_fixture(selector)
    a_content = _strip_line_prefix(
        "1: alpha_only\n2: beta\n10: line_ten\n11: line_eleven\n12: line_twelve\n"
    )
    b_content = _strip_line_prefix(
        "5: unique_replacement_token\n6: shared_replacement_token\n"
    )
    files = (("internal/a.go", a_content), ("internal/b.go", b_content))

    if selector == "R05-01-refusal-unknown-handle":
        setup = ("cat -n internal/a.go",)
        reply = "Verified [read#1:1] and unknown [read#99]."
    elif selector == "R05-02-refusal-wrong-path":
        setup = ("cat -n internal/a.go",)
        reply = "Verified [read#1:1] and wrong path [read#1 internal/unseen.go:1]."
    elif selector == "R05-03-refusal-uncovered-range":
        setup = ("cat -n internal/a.go",)
        reply = "Verified [read#1:10] and uncovered [read#1:25]."
    elif selector == "R05-04-refusal-excerpt-mismatch":
        setup = ("cat -n internal/a.go",)
        reply = 'Verified [read#1:10] and mismatched [read#1:10 "does_not_match_any_line"].'
    elif selector == "R05-05-replacement-suggested-unique-one":
        setup = ("cat -n internal/a.go", "cat -n internal/b.go")
        reply = 'Verified [read#1:1] and wrong handle [read#1 internal/b.go:5 "unique_replacement_token"].'
    else:  # R05-06-replacement-omitted-when-ambiguous-multiple
        setup = ("cat -n internal/a.go", "cat -n internal/b.go", "cat -n internal/b.go")
        reply = 'Verified [read#1:1] and ambiguous [read#1 internal/b.go:6 "shared_replacement_token"].'

    return SidecarSlotPlan(
        selector=selector,
        family_id="R05",
        is_fixed=True,
        base_seed=base_seed,
        instance_index=None,
        platform=platform,
        expected_class="ungrounded",
        files=files,
        setup_commands=setup,
        reply_text=reply,
        repair_text="Verified [read#1:1].",
        expected_refusal_reason=fx.expected_refusal_reason,
        expected_suggested_replacement=fx.expected_suggested_replacement,
    )


def _build_r04_plan(base_seed: str, idx: int, platform: str) -> SidecarSlotPlan:
    gen = generate_r04(base_seed, idx)
    sample_content = "package sample\n\nfunc Hello() string { return \"ok\" }\n"
    # Check whether the R04 markdown contains any citation attempt that won't be grounded
    # against a single read of sample.go (most R04 cases reference [read#3], unobserved URLs, etc.).
    has_ungrounded_attempt = any(s.is_citation_attempt for s in gen.spans)
    return SidecarSlotPlan(
        selector=f"R04-gen-{base_seed}-{idx:04d}",
        family_id="R04",
        is_fixed=False,
        base_seed=base_seed,
        instance_index=idx,
        platform=platform,
        expected_class="ungrounded" if has_ungrounded_attempt else "grounded",
        files=(("sample.go", sample_content),),
        setup_commands=("cat -n sample.go",),
        reply_text=f"Observed [read#1:3].\n\n{gen.markdown}",
        repair_text="Observed [read#1:3].",
        variation_or_mutation="markdown_parse",
    )


def _build_r06_plan(base_seed: str, idx: int, platform: str) -> SidecarSlotPlan:
    gen = generate_r06(base_seed, idx, platform=platform)
    target = gen.delivery_log.records[0]
    raw_text = target.raw_bytes.decode("utf-8", errors="replace")
    plain_content = _strip_line_prefix(raw_text)
    setup_cmd = f"cat -n {json.dumps(target.path)}"
    return SidecarSlotPlan(
        selector=f"R06-gen-{base_seed}-{idx:04d}",
        family_id="R06",
        is_fixed=False,
        base_seed=base_seed,
        instance_index=idx,
        platform=platform,
        expected_class="grounded",
        files=((target.path, plain_content),),
        setup_commands=(setup_cmd,),
        reply_text=f"Verified claim at {gen.citation.raw}.",
        repair_text="Verified claim at [read#1].",
        variation_or_mutation=gen.variation,
    )


def _build_r07_plan(base_seed: str, idx: int, platform: str) -> SidecarSlotPlan:
    gen = generate_r07(base_seed, idx, platform=platform)
    files: list[tuple[str, str]] = []
    setup_cmds: list[str] = []

    if gen.mutation in ("url_query_fragment_redirect_mismatch", "wrong_leg_or_chat_handle", "absent_artifact"):
        sample_content = "package sample\n\nfunc Hello() string { return \"ok\" }\n"
        files.append(("sample.go", sample_content))
        setup_cmds.append("cat -n sample.go")
    else:
        for rec in gen.delivery_log.records[:2]:
            if rec.path:
                raw_text = rec.raw_bytes.decode("utf-8", errors="replace")
                plain = _strip_line_prefix(raw_text)
                if gen.working_tree_only_lines and rec.path == gen.delivery_log.records[0].path:
                    extra = "\n".join(
                        re.sub(r"^\s*\d+:\s?", "", ln) for ln in gen.working_tree_only_lines
                    )
                    plain = plain + extra + "\n"
                files.append((rec.path, plain))
                if gen.working_tree_only_lines and rec.delivered_ranges:
                    r0 = rec.delivered_ranges[0]
                    setup_cmds.append(_cat_range_cmd(rec.path, r0.start, r0.end))
                else:
                    setup_cmds.append(f"cat -n {json.dumps(rec.path)}")

    return SidecarSlotPlan(
        selector=f"R07-gen-{base_seed}-{idx:04d}",
        family_id="R07",
        is_fixed=False,
        base_seed=base_seed,
        instance_index=idx,
        platform=platform,
        expected_class="ungrounded",
        files=tuple(files),
        setup_commands=tuple(setup_cmds),
        reply_text=f"Observed [read#1] and claimed {gen.citation.raw}.",
        repair_text="Observed [read#1].",
        variation_or_mutation=gen.mutation,
    )


def build_sidecar_slot_plan(
    *,
    families: tuple[str, ...] = SIDECAR_FAMILIES,
    base_seed: str = "20261127",
    platform: str = "linux",
    limit_generated: int = 0,
) -> list[SidecarSlotPlan]:
    """Build the complete ordered GB04 sidecar slot plan before sharding."""
    if base_seed not in BASE_SEEDS:
        raise ValueError(f"Unsupported base_seed {base_seed!r}; expected one of {BASE_SEEDS}")
    if limit_generated < 0:
        raise ValueError("limit_generated must be >= 0")

    plan: list[SidecarSlotPlan] = []
    if "R01" in families:
        for slot in R01_SLOTS:
            plan.append(_build_r01_plan(slot.selector, base_seed=base_seed, platform=platform))
    if "R05" in families:
        for slot in R05_SLOTS:
            plan.append(_build_r05_plan(slot.selector, base_seed=base_seed, platform=platform))
    if "R04" in families:
        n = limit_generated or GENERATED_BY_FAMILY["R04"].sidecar_count
        for idx in range(n):
            plan.append(_build_r04_plan(base_seed, idx, platform))
    if "R06" in families:
        n = limit_generated or GENERATED_BY_FAMILY["R06"].sidecar_count
        for idx in range(n):
            plan.append(_build_r06_plan(base_seed, idx, platform))
    if "R07" in families:
        n = limit_generated or GENERATED_BY_FAMILY["R07"].sidecar_count
        for idx in range(n):
            plan.append(_build_r07_plan(base_seed, idx, platform))
    return plan


def shard_slots(
    slots: list[SidecarSlotPlan],
    *,
    shard: int = 0,
    shards: int = 1,
) -> list[SidecarSlotPlan]:
    """Deterministically partition `slots` into `shards` disjoint subsets."""
    if shards < 1:
        raise ValueError("shards must be >= 1")
    if shard < 0 or shard >= shards:
        raise ValueError(f"shard={shard} out of range for shards={shards}")
    return [slot for idx, slot in enumerate(slots) if idx % shards == shard]


def _inspect_sidecar_artifact(art: SidecarRunArtifact, *, initial_turns: int) -> dict[str, Any]:
    closeout = art.product_closeouts[-1] if art.product_closeouts else {}
    grounding = (closeout or {}).get("grounding") or {}
    cited = grounding.get("cited_evidence") or []
    retry_count = int(grounding.get("retry_count") or 0)
    host_assembled = bool(grounding.get("host_assembled") is True)
    hint_code = str(grounding.get("hint_code") or "")
    extra_repair_prompts = max(0, len(art.raw_pending_requests) - initial_turns)
    refused = bool(retry_count > 0 or host_assembled or hint_code or extra_repair_prompts > 0)

    # Extract any repair feedback text sent back to the model after the initial turns
    feedback_chunks: list[str] = []
    if len(art.raw_pending_requests) > initial_turns:
        for req in art.raw_pending_requests[initial_turns:]:
            for msg in req.get("messages") or []:
                role = str(msg.get("role") or "")
                content = str(msg.get("content") or "")
                if role in ("user", "system") and content:
                    feedback_chunks.append(content)
    feedback_text = "\n".join(feedback_chunks[-3:])

    return {
        "session_id": art.session_id,
        "traced": grounding.get("traced"),
        "evidence_optional": grounding.get("evidence_optional"),
        "retry_count": retry_count,
        "host_assembled": host_assembled,
        "hint_code": hint_code,
        "extra_repair_prompts": extra_repair_prompts,
        "refused": refused,
        "cited_evidence_count": len(cited),
        "minted_handles": [r.handle for r in art.product_records],
        "feedback_excerpt": feedback_text[:1000],
    }


def _check_r05_feedback(
    slot: SidecarSlotPlan,
    info: dict[str, Any],
) -> list[dict[str, Any]]:
    """Verify R05 refusal feedback: names true reason, unique replacement only, no host rewrite."""
    divs: list[dict[str, Any]] = []
    if not info["refused"]:
        divs.append(
            {
                "selector": slot.selector,
                "family_id": "R05",
                "kind": "false_accept_r05",
                "detail": "Expected refusal on R05 slot, but D1 accepted without refusal (or silently rewrote).",
            }
        )
        return divs

    fb = str(info.get("feedback_excerpt") or "")
    if slot.expected_suggested_replacement:
        if slot.expected_suggested_replacement not in fb:
            divs.append(
                {
                    "selector": slot.selector,
                    "family_id": "R05",
                    "kind": "r05_missing_unique_replacement",
                    "expected": slot.expected_suggested_replacement,
                    "actual": fb[:300],
                    "detail": (
                        f"Expected unique replacement suggestion {slot.expected_suggested_replacement!r} "
                        f"in refusal feedback"
                    ),
                }
            )
    else:
        # Must NOT suggest a replacement handle when 0 or >1 records verify
        if re.search(r"replace.*read#\d+|use\s+read#\d+|try\s+\[?read#\d+", fb, re.IGNORECASE):
            divs.append(
                {
                    "selector": slot.selector,
                    "family_id": "R05",
                    "kind": "r05_spurious_replacement_suggestion",
                    "expected": None,
                    "actual": fb[:300],
                    "detail": "Refusal feedback suggested a replacement handle when 0 or >1 records verify",
                }
            )
    return divs


def run_gb04_sidecar(
    *,
    engine_bin: str | pathlib.Path,
    product_root: str | pathlib.Path,
    out_dir: str | pathlib.Path,
    expected_engine_sha256: str = "",
    run_id: str = "gb04-sidecar",
    families: tuple[str, ...] = SIDECAR_FAMILIES,
    base_seed: str = "20261127",
    platform: str | None = None,
    shard: int = 0,
    shards: int = 1,
    limit_generated: int = 0,
    trace_friction: bool = True,
    poll_timeout: float = 120.0,
    dry_run: bool = False,
) -> dict[str, Any]:
    """Execute one shard of the GB04 sidecar suite against verified D1 `lycaon-dev`."""
    t0 = time.monotonic()
    plat = platform or default_platform()
    out_path = pathlib.Path(out_dir).resolve()
    out_path.mkdir(parents=True, exist_ok=True)

    full_plan = build_sidecar_slot_plan(
        families=families,
        base_seed=base_seed,
        platform=plat,
        limit_generated=limit_generated,
    )
    shard_plan = shard_slots(full_plan, shard=shard, shards=shards)

    summary_file = out_path / "gb04-sidecar-summary.json"
    divergences_file = out_path / "gb04-sidecar-divergences.jsonl"

    if dry_run:
        plan_file = out_path / "gb04-sidecar-plan.json"
        plan_data = {
            "card": "GB04",
            "lane": "sidecar",
            "dry_run": True,
            "run_id": run_id,
            "base_seed": base_seed,
            "platform": plat,
            "families": list(families),
            "shard": shard,
            "shards": shards,
            "trace_friction": trace_friction,
            "total_slots_all_shards": len(full_plan),
            "shard_slot_count": len(shard_plan),
            "selectors": [s.selector for s in shard_plan],
        }
        plan_file.write_text(json.dumps(plan_data, indent=2, sort_keys=True), encoding="utf-8")
        summary_file.write_text(json.dumps(plan_data, indent=2, sort_keys=True), encoding="utf-8")
        return plan_data

    workspace = out_path / "workspace"
    config_dir = out_path / "config"
    workspace.mkdir(parents=True, exist_ok=True)

    sidecar = IsolatedSidecarProcess(
        engine_bin,
        product_root=product_root,
        config_dir=config_dir,
        workspace_dir=workspace,
        expected_sha256=expected_engine_sha256,
        trace_friction=trace_friction,
    )

    slots_report: list[dict[str, Any]] = []
    divergences: list[dict[str, Any]] = []
    family_stats: dict[str, dict[str, int]] = {
        fam: {"total": 0, "passed": 0, "diverged": 0} for fam in families
    }

    with sidecar as client:
        manifest = sidecar.build_run_manifest()
        project = client.create_project(workspace)
        pid = project["id"]

        for slot in shard_plan:
            family_stats.setdefault(slot.family_id, {"total": 0, "passed": 0, "diverged": 0})
            family_stats[slot.family_id]["total"] += 1

            for rel_path, content in slot.files:
                dest = workspace / rel_path
                dest.parent.mkdir(parents=True, exist_ok=True)
                dest.write_text(content, encoding="utf-8")

            sess = client.create_session(pid)
            sid = sess["id"]
            client.wait_session_ready(sid)

            before_dict: dict[str, Any] | None = None
            after_dict: dict[str, Any] | None = None
            delta_dict: dict[str, Any] | None = None
            if slot.is_fixed:
                before = probe_store_db(sidecar.store_db_path, sid)
                before_dict = before.to_dict()

            prompt_resp = client.send_prompt(sid, f"Inspect workspace files for {slot.selector}.")
            turns = tuple(
                ScriptedTurn(tool_calls=({"name": "command", "args": {"command": cmd}},))
                for cmd in slot.setup_commands
            ) + (ScriptedTurn(content=slot.reply_text),)

            art = run_scripted_session(
                client,
                session_id=sid,
                turns=turns,
                db_path=sidecar.store_db_path,
                workspace_dir=workspace,
                submission_id=str(prompt_resp.get("submission_id") or prompt_resp.get("id") or ""),
                wait_for_settlement=True,
                max_repair_replies=3 if slot.repair_text else 0,
                repair_reply_content=slot.repair_text,
                engine_provenance=sidecar.provenance,
                run_manifest=manifest,
                poll_timeout=poll_timeout,
            )

            if slot.is_fixed:
                after = probe_store_db(sidecar.store_db_path, sid)
                after_dict = after.to_dict()
                delta_dict = after.delta_observed(before)

            info = _inspect_sidecar_artifact(art, initial_turns=len(turns))
            slot_divs: list[dict[str, Any]] = []

            if slot.family_id == "R05":
                slot_divs.extend(_check_r05_feedback(slot, info))
            elif slot.expected_class == "grounded":
                if info["refused"] or info["cited_evidence_count"] < 1:
                    slot_divs.append(
                        {
                            "selector": slot.selector,
                            "family_id": slot.family_id,
                            "base_seed": slot.base_seed,
                            "instance_index": slot.instance_index,
                            "variation_or_mutation": slot.variation_or_mutation,
                            "kind": "false_reject",
                            "detail": (
                                f"expected=grounded, refused={info['refused']}, "
                                f"retry_count={info['retry_count']}, hint_code={info['hint_code']!r}, "
                                f"cited_evidence={info['cited_evidence_count']}"
                            ),
                        }
                    )
            else:
                if not info["refused"]:
                    slot_divs.append(
                        {
                            "selector": slot.selector,
                            "family_id": slot.family_id,
                            "base_seed": slot.base_seed,
                            "instance_index": slot.instance_index,
                            "variation_or_mutation": slot.variation_or_mutation,
                            "kind": "false_accept",
                            "detail": (
                                f"expected=ungrounded, refused={info['refused']}, "
                                f"retry_count={info['retry_count']}, hint_code={info['hint_code']!r}"
                            ),
                        }
                    )

            passed = len(slot_divs) == 0
            if passed:
                family_stats[slot.family_id]["passed"] += 1
            else:
                family_stats[slot.family_id]["diverged"] += 1
                divergences.extend(slot_divs)

            entry: dict[str, Any] = {
                "selector": slot.selector,
                "family_id": slot.family_id,
                "is_fixed": slot.is_fixed,
                "expected_class": slot.expected_class,
                "passed": passed,
                "summary": info,
            }
            if before_dict is not None:
                entry["d10_before"] = before_dict
                entry["d10_after"] = after_dict
                entry["d10_delta"] = delta_dict
            slots_report.append(entry)

    with divergences_file.open("w", encoding="utf-8") as fh:
        for d in divergences:
            fh.write(json.dumps(d, ensure_ascii=False, sort_keys=True) + "\n")

    summary: dict[str, Any] = {
        "card": "GB04",
        "lane": "sidecar",
        "run_id": run_id,
        "base_seed": base_seed,
        "platform": plat,
        "families": list(families),
        "shard": shard,
        "shards": shards,
        "trace_friction": trace_friction,
        "total_slots_all_shards": len(full_plan),
        "shard_slot_count": len(shard_plan),
        "total_divergences": len(divergences),
        "family_stats": family_stats,
        "run_manifest": manifest.to_dict(),
        "elapsed_seconds": round(time.monotonic() - t0, 3),
        "slots": slots_report,
        "artifacts": {
            "summary_json": str(summary_file),
            "divergences_jsonl": str(divergences_file),
        },
    }
    summary_file.write_text(json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8")
    return summary


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="GB04 full D1 sidecar runner")
    ap.add_argument("--engine-bin", default="")
    ap.add_argument("--product-root", default="")
    ap.add_argument("--expected-engine-sha256", default="")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--run-id", default="gb04-sidecar")
    ap.add_argument("--families", default=",".join(SIDECAR_FAMILIES))
    ap.add_argument("--base-seed", default="20261127", choices=list(BASE_SEEDS))
    ap.add_argument("--platform", default=default_platform(), choices=["darwin", "linux"])
    ap.add_argument("--shard", type=int, default=0)
    ap.add_argument("--shards", type=int, default=1)
    ap.add_argument("--limit-generated", type=int, default=0)
    ap.add_argument("--no-trace-friction", action="store_true")
    ap.add_argument("--poll-timeout", type=float, default=120.0)
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args(argv)

    if not args.dry_run and (not args.engine_bin or not args.product_root):
        ap.error("--engine-bin and --product-root are required unless --dry-run is set")

    families = tuple(f.strip() for f in args.families.split(",") if f.strip())
    summary = run_gb04_sidecar(
        engine_bin=args.engine_bin,
        product_root=args.product_root,
        out_dir=args.out_dir,
        expected_engine_sha256=args.expected_engine_sha256,
        run_id=args.run_id,
        families=families,
        base_seed=args.base_seed,
        platform=args.platform,
        shard=args.shard,
        shards=args.shards,
        limit_generated=args.limit_generated,
        trace_friction=not args.no_trace_friction,
        poll_timeout=args.poll_timeout,
        dry_run=args.dry_run,
    )
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
