"""Deterministic synthetic fixture workspaces, independent DeliveryLogs, and scripted turn specs for GB03.

All fixtures are pure-Python, deterministic, and strictly independent of Go/TS
product grounding code (importing read-only from `grounding.*`).
"""
from __future__ import annotations

from dataclasses import dataclass
import pathlib
from typing import Any

from grounding.delivery_log import (
    DeliveryLog,
    DeliveryRecord,
    LineRange,
    ProductRecord,
    ProductVerdict,
    SurfaceOutcome,
)
from grounding.generators import BASE_SEEDS, GeneratedV02Case, generate_v02
from grounding.reference_resolver import (
    Citation,
    PermittedScope,
    ReferenceVerdict,
    resolve_citation,
)
from grounding.sidecar_driver import ScriptedTurn

from .catalog import GB03_FIXED_SLOTS, SLOTS_BY_SELECTOR


# Synthetic non-live D16 secret canary (clearly marked synthetic, matches standard token shape).
D16_RAW_SECRET_CANARY: str = "pw_canary_sk_test_999988887777666655554444"
D16_REDACTED_MARKER: str = "[REDACTED_SECRET]"


@dataclass(frozen=True)
class CitationExpectation:
    """One citation under test and its independent expected resolution outcome."""

    citation: Citation
    expected_verdict_class: str  # "grounded" | "ambiguous" | "ungrounded"
    expected_exact_verdict: str  # "matched" | "traced" | "bound" | "ambiguous" | "unverifiable"
    expected_reason: str = ""
    expected_supporting_handles: tuple[str, ...] = ()
    expected_contract_reading: str | None = None
    expected_observed_product_rule: str = ""
    expected_disputed: bool = False


@dataclass(frozen=True)
class GB03SlotFixture:
    """Complete deterministic fixture package for one GB03 fixed slot."""

    selector: str
    family_id: str
    lane: str
    workspace_files: dict[str, str]
    delivery_log: DeliveryLog
    scope: PermittedScope
    scripted_turns: tuple[ScriptedTurn, ...]
    expectations: tuple[CitationExpectation, ...]
    expected_has_citable_observation: bool | None = True
    expected_recallable_handles: tuple[str, ...] = ()
    expected_non_recallable_handles: tuple[str, ...] = ()
    expected_union_handles: tuple[str, ...] | None = None
    expected_forbidden_body_substrings: tuple[str, ...] = ()
    metadata: dict[str, Any] | None = None

    def materialize_workspace(self, root: str | pathlib.Path) -> pathlib.Path:
        """Write deterministic synthetic workspace files under `root`."""
        base = pathlib.Path(root)
        base.mkdir(parents=True, exist_ok=True)
        for rel_path, content in self.workspace_files.items():
            target = base / rel_path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(content, encoding="utf-8")
        return base

    def matching_product_records(self) -> tuple[ProductRecord, ...]:
        """Build healthy ProductRecords mirroring model-visible DeliveryRecords for harness tests."""
        return tuple(
            ProductRecord(
                handle=rec.handle,
                kind=rec.kind,
                session_id=rec.session_id,
                worker_leg=rec.worker_leg,
                source_revision=rec.source_revision,
                delivered_ranges=rec.delivered_ranges,
                raw_bytes=rec.raw_bytes,
                path=rec.path,
                url=rec.url,
                artifact_id=rec.artifact_id,
                round_id=rec.round_id,
                shape=rec.shape,
                trust=rec.trust,
                superseded_by=rec.superseded_by,
            )
            for rec in self.delivery_log.records
            if rec.model_visible
        )

    def healthy_surface_outcome(self, *, surface: str = "coordinator_closeout") -> SurfaceOutcome:
        """Build a healthy SurfaceOutcome from independent expectations for smoke checks."""
        verdicts: list[ProductVerdict] = []
        for exp in self.expectations:
            ref: ReferenceVerdict = resolve_citation(self.delivery_log, exp.citation, self.scope)
            resolved_handle = ref.supporting_handles[0] if ref.supporting_handles else ""
            verdicts.append(
                ProductVerdict(
                    citation_id=exp.citation.citation_id,
                    verdict=ref.expected_exact_verdict,
                    resolved_handle=resolved_handle if ref.verdict_class == "grounded" else "",
                    reanchored_line=ref.reanchored_line,
                )
            )
        return SurfaceOutcome(
            surface=surface,
            verdicts=tuple(verdicts),
            records=self.matching_product_records(),
        )


@dataclass(frozen=True)
class V02LookalikeFixture:
    """Independent DeliveryLog wrapper for one generated V02 lookalike instance."""

    case: GeneratedV02Case
    delivery_log: DeliveryLog
    scope: PermittedScope
    scripted_turns: tuple[ScriptedTurn, ...]
    spoofed_citations: tuple[Citation, ...]


# ---------------------------------------------------------------------------
# Helpers for constructing independent DeliveryRecords
# ---------------------------------------------------------------------------

def _rec(
    *,
    tool: str,
    handle: str,
    kind: str,
    raw_text: str,
    shape: str = "",
    path: str = "",
    url: str = "",
    artifact_id: str = "",
    ranges: tuple[LineRange, ...] = (),
    tags: tuple[str, ...] | None = None,
    line_numbered: bool = False,
    printed_other_numbers: bool = False,
    session_id: str = "sess-gb03",
    worker_leg: str = "",
    round_id: str = "round-1",
    intent_window: str = "intent-1",
    source_revision: str = "rev-1",
    superseded_by: str = "",
    model_visible: bool = True,
    trust: str = "trusted",
    arguments: dict[str, Any] | None = None,
    call_id: str = "",
) -> DeliveryRecord:
    if tags is None:
        if not model_visible:
            tags = ()
        elif path and ranges:
            tags = (f"[{handle} {path}:{ranges[0].start}-{ranges[0].end}]",)
        else:
            tags = (f"[{handle}]",)
    return DeliveryRecord(
        tool=tool,
        arguments=arguments or ({"path": path} if path else {}),
        raw_bytes=raw_text.encode("utf-8"),
        delivered_ranges=ranges,
        delivery_tags=tags,
        line_numbered=line_numbered,
        printed_other_numbers=printed_other_numbers,
        session_id=session_id,
        worker_leg=worker_leg,
        round_id=round_id,
        intent_window=intent_window,
        source_revision=source_revision,
        handle=handle,
        kind=kind,
        path=path,
        url=url,
        artifact_id=artifact_id,
        call_id=call_id or f"call-{session_id}-{worker_leg or 'root'}-{handle}",
        fixture_provenance="host",
        superseded_by=superseded_by,
        model_visible=model_visible,
        shape=shape,
        trust=trust,
    )


def _tool_turn(tool_name: str, args: dict[str, Any], call_id: str = "tc-1") -> ScriptedTurn:
    return ScriptedTurn(
        tool_calls=({"id": call_id, "name": tool_name, "arguments": args},),
        surface="tool_call",
    )


def _closeout_turn(content: str) -> ScriptedTurn:
    return ScriptedTurn(content=content, surface="coordinator_closeout")


# ---------------------------------------------------------------------------
# V01 · Minting (14 sidecar slots)
# ---------------------------------------------------------------------------

def _build_v01_fixtures() -> dict[str, GB03SlotFixture]:
    sess = "sess-v01"
    scope = PermittedScope(session_id=sess, round_id="round-1", intent_window="intent-1")

    a_go = "package app\n\nfunc Alpha() int {\n    return 42\n}\n"
    a_go_numbered = "1: package app\n2: \n3: func Alpha() int {\n4:     return 42\n5: }\n"
    b_go = "package app\n\nfunc Beta() int {\n    return 7\n}\n"

    fixtures: dict[str, GB03SlotFixture] = {}

    # V01-01
    r1 = _rec(
        tool="read_file",
        handle="read#1",
        kind="read",
        shape="file_region",
        path="internal/a.go",
        raw_text=a_go_numbered,
        ranges=(LineRange(1, 5),),
        line_numbered=True,
        session_id=sess,
    )
    fixtures["V01-01-read-file-region"] = GB03SlotFixture(
        selector="V01-01-read-file-region",
        family_id="V01",
        lane="sidecar",
        workspace_files={"internal/a.go": a_go},
        delivery_log=DeliveryLog(records=(r1,)),
        scope=scope,
        scripted_turns=(
            _tool_turn("read_file", {"path": "internal/a.go", "start_line": 1, "end_line": 5}),
            _closeout_turn('Alpha returns 42 [read#1:4 "return 42"].'),
        ),
        expectations=(
            CitationExpectation(
                citation=Citation(
                    citation_type="handle",
                    raw='[read#1:4 "return 42"]',
                    handle="read#1",
                    start_line=4,
                    excerpt="return 42",
                    citation_id="v01-01-c1",
                ),
                expected_verdict_class="grounded",
                expected_exact_verdict="matched",
                expected_supporting_handles=("read#1",),
            ),
        ),
    )

    # V01-02
    g1 = _rec(
        tool="grep_files",
        handle="grep#1",
        kind="grep",
        shape="search_matches",
        path="internal/a.go",
        raw_text="3: func Alpha() int {\n",
        ranges=(LineRange(3, 3),),
        line_numbered=True,
        session_id=sess,
    )
    f1 = _rec(tool="find_files", handle="find#1", kind="find", shape="path_list", raw_text="internal/a.go\ninternal/b.go\n", session_id=sess)
    l1 = _rec(tool="list_dir", handle="list#1", kind="list", shape="path_list", raw_text="a.go\nb.go\n", session_id=sess)
    st1 = _rec(tool="stat_file", handle="stat#1", kind="stat", shape="file_metadata", path="internal/a.go", raw_text="size=52 mode=0644\n", session_id=sess)
    w1 = _rec(tool="wc_file", handle="wc#1", kind="wc", shape="file_metadata", path="internal/a.go", raw_text="5 9 52 internal/a.go\n", session_id=sess)
    fixtures["V01-02-grep-find-list-stat-wc"] = GB03SlotFixture(
        selector="V01-02-grep-find-list-stat-wc",
        family_id="V01",
        lane="sidecar",
        workspace_files={"internal/a.go": a_go, "internal/b.go": b_go},
        delivery_log=DeliveryLog(records=(g1, f1, l1, st1, w1)),
        scope=scope,
        scripted_turns=(
            _tool_turn("grep_files", {"pattern": "Alpha"}, "tc-1"),
            _tool_turn("find_files", {"path": "internal"}, "tc-2"),
            _tool_turn("list_dir", {"path": "internal"}, "tc-3"),
            _tool_turn("stat_file", {"path": "internal/a.go"}, "tc-4"),
            _tool_turn("wc_file", {"path": "internal/a.go"}, "tc-5"),
            _closeout_turn("Surveyed files [grep#1], [find#1], [list#1], [stat#1], [wc#1]."),
        ),
        expectations=tuple(
            CitationExpectation(
                citation=Citation(citation_type="handle", raw=f"[{h}]", handle=h, citation_id=f"v01-02-{h}"),
                expected_verdict_class="grounded",
                expected_exact_verdict="matched",
                expected_supporting_handles=(h,),
            )
            for h in ("grep#1", "find#1", "list#1", "stat#1", "wc#1")
        ),
    )

    # V01-03
    mut_recs = (
        _rec(tool="edit_file", handle="edit#1", kind="edit", shape="file_mutation", path="internal/a.go", raw_text="4:     return 43\n", ranges=(LineRange(4, 4),), line_numbered=True, source_revision="rev-2", session_id=sess),
        _rec(tool="write_file", handle="write#1", kind="write", shape="file_mutation", path="internal/new.go", raw_text="1: package app\n", ranges=(LineRange(1, 1),), line_numbered=True, source_revision="rev-3", session_id=sess),
        _rec(tool="replace_in_file", handle="replace#1", kind="replace", shape="file_mutation", path="internal/b.go", raw_text="4:     return 8\n", ranges=(LineRange(4, 4),), line_numbered=True, source_revision="rev-4", session_id=sess),
        _rec(tool="restore_file", handle="restore#1", kind="restore", shape="file_mutation", path="internal/b.go", raw_text="4:     return 7\n", ranges=(LineRange(4, 4),), line_numbered=True, source_revision="rev-5", session_id=sess),
    )
    fixtures["V01-03-edit-write-replace-restore"] = GB03SlotFixture(
        selector="V01-03-edit-write-replace-restore",
        family_id="V01",
        lane="sidecar",
        workspace_files={"internal/a.go": a_go, "internal/b.go": b_go},
        delivery_log=DeliveryLog(records=mut_recs),
        scope=scope,
        scripted_turns=(
            _tool_turn("edit_file", {"path": "internal/a.go"}, "tc-1"),
            _tool_turn("write_file", {"path": "internal/new.go"}, "tc-2"),
            _tool_turn("replace_in_file", {"path": "internal/b.go"}, "tc-3"),
            _tool_turn("restore_file", {"path": "internal/b.go"}, "tc-4"),
            _closeout_turn("Mutations recorded [edit#1], [write#1], [replace#1], [restore#1]."),
        ),
        expectations=tuple(
            CitationExpectation(
                citation=Citation(citation_type="handle", raw=f"[{r.handle}]", handle=r.handle, citation_id=f"v01-03-{r.handle}"),
                expected_verdict_class="grounded",
                expected_exact_verdict="matched",
                expected_supporting_handles=(r.handle,),
            )
            for r in mut_recs
        ),
    )

    # V01-04
    struct_recs = (
        _rec(tool="rewrite_file", handle="rewrite#1", kind="rewrite", shape="file_mutation", path="internal/a.go", raw_text="1: package app\n", ranges=(LineRange(1, 1),), line_numbered=True, source_revision="rev-2", session_id=sess),
        _rec(tool="delete_file", handle="delete#1", kind="delete", shape="file_mutation", path="internal/obsolete.go", raw_text="deleted internal/obsolete.go\n", source_revision="rev-3", session_id=sess),
        _rec(tool="git_diff", handle="diff#1", kind="diff", shape="diff_hunks", path="internal/a.go", raw_text="-    return 42\n+    return 43\n", source_revision="rev-3", session_id=sess),
    )
    fixtures["V01-04-rewrite-delete-diff"] = GB03SlotFixture(
        selector="V01-04-rewrite-delete-diff",
        family_id="V01",
        lane="sidecar",
        workspace_files={"internal/a.go": a_go, "internal/obsolete.go": "package app\n"},
        delivery_log=DeliveryLog(records=struct_recs),
        scope=scope,
        scripted_turns=(
            _tool_turn("rewrite_file", {"path": "internal/a.go"}, "tc-1"),
            _tool_turn("delete_file", {"path": "internal/obsolete.go"}, "tc-2"),
            _tool_turn("git_diff", {"path": "internal/a.go"}, "tc-3"),
            _closeout_turn("Structural ops [rewrite#1], [delete#1], [diff#1]."),
        ),
        expectations=tuple(
            CitationExpectation(
                citation=Citation(citation_type="handle", raw=f"[{r.handle}]", handle=r.handle, citation_id=f"v01-04-{r.handle}"),
                expected_verdict_class="grounded",
                expected_exact_verdict="matched",
                expected_supporting_handles=(r.handle,),
            )
            for r in struct_recs
        ),
    )

    # V01-05: append-only 1-based ordinals
    seq_recs = (
        _rec(tool="read_file", handle="read#1", kind="read", shape="file_region", path="internal/a.go", raw_text="1: package app\n", ranges=(LineRange(1, 1),), line_numbered=True, session_id=sess),
        _rec(tool="read_file", handle="read#2", kind="read", shape="file_region", path="internal/a.go", raw_text="3: func Alpha() int {\n", ranges=(LineRange(3, 3),), line_numbered=True, session_id=sess),
        _rec(tool="read_file", handle="read#3", kind="read", shape="file_region", path="internal/b.go", raw_text="3: func Beta() int {\n", ranges=(LineRange(3, 3),), line_numbered=True, session_id=sess),
    )
    fixtures["V01-05-append-only-ordinals"] = GB03SlotFixture(
        selector="V01-05-append-only-ordinals",
        family_id="V01",
        lane="sidecar",
        workspace_files={"internal/a.go": a_go, "internal/b.go": b_go},
        delivery_log=DeliveryLog(records=seq_recs),
        scope=scope,
        scripted_turns=(
            _tool_turn("read_file", {"path": "internal/a.go", "start_line": 1, "end_line": 1}, "tc-1"),
            _tool_turn("read_file", {"path": "internal/a.go", "start_line": 3, "end_line": 3}, "tc-2"),
            _tool_turn("read_file", {"path": "internal/b.go", "start_line": 3, "end_line": 3}, "tc-3"),
            _closeout_turn("Monotonic ordinals [read#1], [read#2], [read#3]."),
        ),
        expectations=(
            CitationExpectation(Citation("handle", "[read#1:1]", handle="read#1", start_line=1, citation_id="v01-05-c1"), "grounded", "matched", expected_supporting_handles=("read#1",)),
            CitationExpectation(Citation("handle", "[read#2:3]", handle="read#2", start_line=3, citation_id="v01-05-c2"), "grounded", "matched", expected_supporting_handles=("read#2",)),
            CitationExpectation(Citation("handle", "[read#3:3]", handle="read#3", start_line=3, citation_id="v01-05-c3"), "grounded", "matched", expected_supporting_handles=("read#3",)),
            CitationExpectation(Citation("handle", "[read#4]", handle="read#4", citation_id="v01-05-c4"), "ungrounded", "unverifiable", expected_reason="unknown_handle"),
        ),
    )

    # V01-06: command success receipt
    cmd_ok = _rec(
        tool="run_command",
        handle="command#1",
        kind="command",
        shape="command_result",
        raw_text="exit_code: 0\nPASS internal/app\n",
        arguments={"command": "go test ./..."},
        session_id=sess,
    )
    fixtures["V01-06-command-success-receipt"] = GB03SlotFixture(
        selector="V01-06-command-success-receipt",
        family_id="V01",
        lane="sidecar",
        workspace_files={"internal/a.go": a_go},
        delivery_log=DeliveryLog(records=(cmd_ok,)),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": "go test ./..."}),
            _closeout_turn('Tests passed [command#1 "PASS internal/app"].'),
        ),
        expectations=(
            CitationExpectation(
                Citation("handle", '[command#1 "PASS internal/app"]', handle="command#1", excerpt="PASS internal/app", citation_id="v01-06-c1"),
                "grounded",
                "matched",
                expected_supporting_handles=("command#1",),
            ),
        ),
    )

    # V01-07: error command outcome receipt
    cmd_err = _rec(
        tool="run_command",
        handle="command#1",
        kind="command",
        shape="command_result",
        raw_text="exit_code: 1\nFAIL internal/app: TestAlpha failed\n",
        arguments={"command": "go test ./..."},
        session_id=sess,
    )
    fixtures["V01-07-command-error-outcome-receipt"] = GB03SlotFixture(
        selector="V01-07-command-error-outcome-receipt",
        family_id="V01",
        lane="sidecar",
        workspace_files={"internal/a.go": a_go},
        delivery_log=DeliveryLog(records=(cmd_err,)),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": "go test ./..."}),
            _closeout_turn('Command failed [command#1 "FAIL internal/app"].'),
        ),
        expectations=(
            CitationExpectation(
                Citation("handle", '[command#1 "FAIL internal/app"]', handle="command#1", excerpt="FAIL internal/app", citation_id="v01-07-c1"),
                "grounded",
                "matched",
                expected_supporting_handles=("command#1",),
            ),
            CitationExpectation(
                Citation("handle", '[command#1 "PASS internal/app"]', handle="command#1", excerpt="PASS internal/app", citation_id="v01-07-c2"),
                "ungrounded",
                "unverifiable",
                expected_reason="excerpt_mismatch",
            ),
        ),
    )

    # V01-08: git error outcome receipt
    git_err = _rec(
        tool="git_commit",
        handle="git_receipt#1",
        kind="git_receipt",
        shape="git_receipt",
        raw_text="exit_code: 128\nfatal: nothing to commit, working tree clean\n",
        arguments={"op": "commit"},
        session_id=sess,
    )
    fixtures["V01-08-git-error-outcome-receipt"] = GB03SlotFixture(
        selector="V01-08-git-error-outcome-receipt",
        family_id="V01",
        lane="sidecar",
        workspace_files={"internal/a.go": a_go},
        delivery_log=DeliveryLog(records=(git_err,)),
        scope=scope,
        scripted_turns=(
            _tool_turn("git_commit", {"message": "empty"}),
            _closeout_turn('Git commit failed [git_receipt#1 "fatal: nothing to commit"].'),
        ),
        expectations=(
            CitationExpectation(
                Citation("handle", '[git_receipt#1 "fatal: nothing to commit"]', handle="git_receipt#1", excerpt="fatal: nothing to commit", citation_id="v01-08-c1"),
                "grounded",
                "matched",
                expected_supporting_handles=("git_receipt#1",),
            ),
            CitationExpectation(
                Citation("handle", '[git_receipt#1 "created commit"]', handle="git_receipt#1", excerpt="created commit", citation_id="v01-08-c2"),
                "ungrounded",
                "unverifiable",
                expected_reason="excerpt_mismatch",
            ),
        ),
    )

    # V01-09: refused invocation mints no file-content proof
    fixtures["V01-09-refused-invocation-no-file-proof"] = GB03SlotFixture(
        selector="V01-09-refused-invocation-no-file-proof",
        family_id="V01",
        lane="sidecar",
        workspace_files={"restricted/secret.env": "TOKEN=unobserved\n"},
        delivery_log=DeliveryLog(records=()),
        scope=scope,
        scripted_turns=(
            _tool_turn("read_file", {"path": "restricted/secret.env"}),
            _closeout_turn("Cannot cite refused file [restricted/secret.env:1]."),
        ),
        expectations=(
            CitationExpectation(
                Citation("path_coord", "[restricted/secret.env:1]", path="restricted/secret.env", start_line=1, citation_id="v01-09-c1"),
                "ungrounded",
                "unverifiable",
                expected_reason="path_not_observed",
            ),
            CitationExpectation(
                Citation("handle", "[read#1]", handle="read#1", citation_id="v01-09-c2"),
                "ungrounded",
                "unverifiable",
                expected_reason="unknown_handle",
            ),
        ),
        expected_has_citable_observation=False,
    )

    # V01-10: failed file mutation mints no file-content proof
    fixtures["V01-10-failed-mutation-no-file-proof"] = GB03SlotFixture(
        selector="V01-10-failed-mutation-no-file-proof",
        family_id="V01",
        lane="sidecar",
        workspace_files={"internal/a.go": a_go},
        delivery_log=DeliveryLog(records=()),
        scope=scope,
        scripted_turns=(
            _tool_turn("edit_file", {"path": "internal/a.go", "target": "nonexistent_symbol"}),
            _closeout_turn("Failed edit minted no proof [edit#1]."),
        ),
        expectations=(
            CitationExpectation(
                Citation("handle", "[edit#1]", handle="edit#1", citation_id="v01-10-c1"),
                "ungrounded",
                "unverifiable",
                expected_reason="unknown_handle",
            ),
            CitationExpectation(
                Citation("path_coord", "[internal/a.go:4]", path="internal/a.go", start_line=4, citation_id="v01-10-c2"),
                "ungrounded",
                "unverifiable",
                expected_reason="path_not_observed",
            ),
        ),
        expected_has_citable_observation=False,
    )

    # V01-11: web, scan, survey, render, page, terminal_session
    admitted_recs = (
        _rec(tool="web_fetch", handle="web#1", kind="web", shape="url_document", url="https://docs.example.test/spec", raw_text="HTTP 200 Spec body\n", trust="untrusted", session_id=sess),
        _rec(tool="security_scan", handle="scan#1", kind="scan", shape="scan_findings", raw_text="FINDING: weak hash\n", session_id=sess),
        _rec(tool="repo_survey", handle="survey#1", kind="survey", shape="survey_summary", raw_text="2 Go files in internal/\n", session_id=sess),
        _rec(tool="render_mockup", handle="render#1", kind="render", shape="surface_snapshot", artifact_id="11111111-1111-4111-8111-111111111111", raw_text="mockup png\n", session_id=sess),
        _rec(tool="page_snapshot", handle="page#1", kind="page", shape="surface_snapshot", artifact_id="22222222-2222-4222-8222-222222222222", raw_text="live dom snapshot\n", trust="untrusted", session_id=sess),
        _rec(tool="terminal_snapshot", handle="terminal_session#1", kind="terminal_session", shape="surface_snapshot", artifact_id="33333333-3333-4333-8333-333333333333", raw_text="tui frame\n", session_id=sess),
    )
    fixtures["V01-11-web-scan-survey-surface-kinds"] = GB03SlotFixture(
        selector="V01-11-web-scan-survey-surface-kinds",
        family_id="V01",
        lane="sidecar",
        workspace_files={"internal/a.go": a_go},
        delivery_log=DeliveryLog(records=admitted_recs),
        scope=scope,
        scripted_turns=(
            _tool_turn("web_fetch", {"url": "https://docs.example.test/spec"}, "tc-1"),
            _tool_turn("security_scan", {"path": "internal"}, "tc-2"),
            _closeout_turn("Admitted web/scan/surface evidence [web#1], [scan#1], [survey#1]."),
        ),
        expectations=(
            CitationExpectation(Citation("url", "https://docs.example.test/spec", url="https://docs.example.test/spec", citation_id="v01-11-url"), "grounded", "matched", expected_supporting_handles=("web#1",)),
            CitationExpectation(Citation("handle", "[scan#1]", handle="scan#1", citation_id="v01-11-scan"), "grounded", "matched", expected_supporting_handles=("scan#1",)),
            CitationExpectation(Citation("artifact", "[artifact:33333333-3333-4333-8333-333333333333]", artifact_id="33333333-3333-4333-8333-333333333333", citation_id="v01-11-art"), "grounded", "matched", expected_supporting_handles=("terminal_session#1",)),
        ),
    )

    # V01-12: F2 dialed_host# contact record alone
    dialed = _rec(
        tool="network_egress",
        handle="dialed_host#1",
        kind="dialed_host",
        shape="url",
        url="https://dialed.example.test",
        raw_text="dialed_host dialed.example.test\n",
        model_visible=False,
        session_id=sess,
    )
    fixtures["V01-12-f2-dialed-host-contact-only"] = GB03SlotFixture(
        selector="V01-12-f2-dialed-host-contact-only",
        family_id="V01",
        lane="sidecar",
        workspace_files={"internal/a.go": a_go},
        delivery_log=DeliveryLog(records=(dialed,)),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": "curl -sI https://dialed.example.test"}),
            _closeout_turn("Contacted https://dialed.example.test [dialed_host#1]."),
        ),
        expectations=(
            CitationExpectation(Citation("url", "https://dialed.example.test", url="https://dialed.example.test", citation_id="v01-12-url"), "ungrounded", "unverifiable", expected_reason="mediated_contact_only"),
            CitationExpectation(Citation("handle", "[dialed_host#1]", handle="dialed_host#1", citation_id="v01-12-h"), "ungrounded", "unverifiable", expected_reason="mediated_contact_only"),
        ),
        expected_has_citable_observation=False,
        expected_non_recallable_handles=("dialed_host#1",),
    )

    # V01-13: F2 retrieved_host# contact record alone
    retrieved = _rec(
        tool="network_egress",
        handle="retrieved_host#1",
        kind="retrieved_host",
        shape="url",
        url="https://retrieved.example.test",
        raw_text="retrieved_host retrieved.example.test\n",
        model_visible=False,
        session_id=sess,
    )
    fixtures["V01-13-f2-retrieved-host-contact-only"] = GB03SlotFixture(
        selector="V01-13-f2-retrieved-host-contact-only",
        family_id="V01",
        lane="sidecar",
        workspace_files={"internal/a.go": a_go},
        delivery_log=DeliveryLog(records=(retrieved,)),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": "curl -s https://retrieved.example.test"}),
            _closeout_turn("Retrieved https://retrieved.example.test [retrieved_host#1]."),
        ),
        expectations=(
            CitationExpectation(Citation("url", "https://retrieved.example.test", url="https://retrieved.example.test", citation_id="v01-13-url"), "ungrounded", "unverifiable", expected_reason="mediated_contact_only"),
            CitationExpectation(Citation("handle", "[retrieved_host#1]", handle="retrieved_host#1", citation_id="v01-13-h"), "ungrounded", "unverifiable", expected_reason="mediated_contact_only"),
        ),
        expected_has_citable_observation=False,
        expected_non_recallable_handles=("retrieved_host#1",),
    )

    # V01-14: F2 full 3-prefix inventory (dialed_host#, retrieved_host#, audited_host#) + admitted web#1 contrast
    audited = _rec(
        tool="network_egress",
        handle="audited_host#1",
        kind="audited_host",
        shape="url",
        url="https://audited.example.test",
        raw_text="audited_host audited.example.test\n",
        model_visible=False,
        session_id=sess,
    )
    admitted_web = _rec(
        tool="web_fetch",
        handle="web#1",
        kind="web",
        shape="url_document",
        url="https://admitted.example.test/doc",
        raw_text="Admitted web document body\n",
        model_visible=True,
        trust="untrusted",
        session_id=sess,
    )
    fixtures["V01-14-f2-audited-host-contact-inventory"] = GB03SlotFixture(
        selector="V01-14-f2-audited-host-contact-inventory",
        family_id="V01",
        lane="sidecar",
        workspace_files={"internal/a.go": a_go},
        delivery_log=DeliveryLog(records=(dialed, retrieved, audited, admitted_web)),
        scope=scope,
        scripted_turns=(
            _tool_turn("web_fetch", {"url": "https://admitted.example.test/doc"}),
            _closeout_turn("Only https://admitted.example.test/doc [web#1] is admitted."),
        ),
        expectations=(
            CitationExpectation(Citation("url", "https://dialed.example.test", url="https://dialed.example.test", citation_id="v01-14-d"), "ungrounded", "unverifiable", expected_reason="mediated_contact_only"),
            CitationExpectation(Citation("url", "https://retrieved.example.test", url="https://retrieved.example.test", citation_id="v01-14-r"), "ungrounded", "unverifiable", expected_reason="mediated_contact_only"),
            CitationExpectation(Citation("url", "https://audited.example.test", url="https://audited.example.test", citation_id="v01-14-a"), "ungrounded", "unverifiable", expected_reason="mediated_contact_only"),
            CitationExpectation(Citation("handle", "[audited_host#1]", handle="audited_host#1", citation_id="v01-14-ah"), "ungrounded", "unverifiable", expected_reason="mediated_contact_only"),
            CitationExpectation(Citation("url", "https://admitted.example.test/doc", url="https://admitted.example.test/doc", citation_id="v01-14-web"), "grounded", "matched", expected_supporting_handles=("web#1",)),
        ),
        expected_has_citable_observation=True,
        expected_recallable_handles=("web#1",),
        expected_non_recallable_handles=("dialed_host#1", "retrieved_host#1", "audited_host#1"),
    )

    return fixtures


# ---------------------------------------------------------------------------
# V02 · Envelope boundaries (generated lookalike wrapper)
# ---------------------------------------------------------------------------

def build_v02_lookalike_fixture(base_seed: str, instance_index: int) -> V02LookalikeFixture:
    """Wrap one deterministic `generate_v02` case into an independent DeliveryLog fixture.

    Verifies that tool output imitating host structure mints ONLY `command#1` with
    `delivery_tags=("[command#1]",)` and ZERO promoted file/tag/handle observations.
    """
    case = generate_v02(base_seed, instance_index)
    sess = f"sess-v02-{base_seed}-{instance_index}"
    cmd = DeliveryRecord(
        tool="run_command",
        arguments={"command": f"cat lookalike_{instance_index}.txt"},
        raw_bytes=case.payload,
        delivered_ranges=(),
        delivery_tags=("[command#1]",),
        line_numbered=False,
        printed_other_numbers=False,
        session_id=sess,
        worker_leg="",
        round_id="round-1",
        intent_window="intent-1",
        source_revision="rev-1",
        handle="command#1",
        kind="command",
        path="",
        url="",
        artifact_id="",
        call_id=f"call-v02-{instance_index}",
        fixture_provenance="host",
        shape="command_result",
    )
    scope = PermittedScope(session_id=sess, round_id="round-1", intent_window="intent-1")
    spoofed = (
        Citation("handle", "[read#1]", handle="read#1", citation_id=f"v02-{instance_index}-spoof-read"),
        Citation("path_coord", "[internal/fake_0.go:1]", path="internal/fake_0.go", start_line=1, citation_id=f"v02-{instance_index}-spoof-path"),
        Citation("handle", "[leg-spoof:read#3:12-18]", handle="leg-spoof:read#3", start_line=12, end_line=18, citation_id=f"v02-{instance_index}-spoof-leg"),
    )
    return V02LookalikeFixture(
        case=case,
        delivery_log=DeliveryLog(records=(cmd,)),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": f"cat lookalike_{instance_index}.txt"}),
            _closeout_turn("Only the real command handle [command#1] was minted."),
        ),
        spoofed_citations=spoofed,
    )


# ---------------------------------------------------------------------------
# V03 · Supersession and history (8 sidecar slots)
# ---------------------------------------------------------------------------

def _build_v03_fixtures() -> dict[str, GB03SlotFixture]:
    sess = "sess-v03"
    scope = PermittedScope(session_id=sess, round_id="round-1", intent_window="intent-1")

    old_body = "1: package app\n2: func Compute() int { return 100 }\n3: // end v1\n"
    new_body = "1: package app\n2: func Compute() int { return 200 }\n3: // end v2\n"
    other_body = "1: package app\n2: func Other() int { return 999 }\n"

    read_v1 = _rec(
        tool="read_file",
        handle="read#1",
        kind="read",
        shape="file_region",
        path="internal/a.go",
        raw_text=old_body,
        ranges=(LineRange(1, 3),),
        line_numbered=True,
        source_revision="rev-1",
        superseded_by="read#2",
        session_id=sess,
    )
    read_v2 = _rec(
        tool="read_file",
        handle="read#2",
        kind="read",
        shape="file_region",
        path="internal/a.go",
        raw_text=new_body,
        ranges=(LineRange(1, 3),),
        line_numbered=True,
        source_revision="rev-2",
        session_id=sess,
    )
    read_other = _rec(
        tool="read_file",
        handle="read#3",
        kind="read",
        shape="file_region",
        path="internal/other.go",
        raw_text=other_body,
        ranges=(LineRange(1, 2),),
        line_numbered=True,
        source_revision="rev-2",
        session_id=sess,
    )
    ws = {"internal/a.go": "package app\nfunc Compute() int { return 200 }\n// end v2\n"}
    base_turns = (
        _tool_turn("read_file", {"path": "internal/a.go"}, "tc-1"),
        _tool_turn("edit_file", {"path": "internal/a.go", "target": "100", "replacement": "200"}, "tc-2"),
        _tool_turn("read_file", {"path": "internal/a.go"}, "tc-3"),
    )

    fixtures: dict[str, GB03SlotFixture] = {}

    # V03-01
    fixtures["V03-01-historical-handle-resolves-captured-body"] = GB03SlotFixture(
        selector="V03-01-historical-handle-resolves-captured-body",
        family_id="V03",
        lane="sidecar",
        workspace_files=ws,
        delivery_log=DeliveryLog(records=(read_v1, read_v2)),
        scope=scope,
        scripted_turns=base_turns + (_closeout_turn('Original body returned 100 [read#1:2 "return 100"].'),),
        expectations=(
            CitationExpectation(
                Citation("handle", '[read#1:2 "return 100"]', handle="read#1", start_line=2, excerpt="return 100", citation_id="v03-01-c1"),
                "grounded",
                "matched",
                expected_supporting_handles=("read#1",),
            ),
        ),
    )

    # V03-02
    fixtures["V03-02-unqualified-path-resolves-current-only"] = GB03SlotFixture(
        selector="V03-02-unqualified-path-resolves-current-only",
        family_id="V03",
        lane="sidecar",
        workspace_files=ws,
        delivery_log=DeliveryLog(records=(read_v1, read_v2)),
        scope=scope,
        scripted_turns=base_turns + (_closeout_turn('Current body returns 200 [internal/a.go:2 "return 200"].'),),
        expectations=(
            CitationExpectation(
                Citation("path_coord", '[internal/a.go:2 "return 200"]', path="internal/a.go", start_line=2, excerpt="return 200", citation_id="v03-02-cur"),
                "grounded",
                "matched",
                expected_supporting_handles=("read#2",),
            ),
            CitationExpectation(
                Citation("path_coord", '[internal/a.go:2 "return 100"]', path="internal/a.go", start_line=2, excerpt="return 100", citation_id="v03-02-old"),
                "ungrounded",
                "unverifiable",
                expected_reason="excerpt_mismatch",
            ),
        ),
    )

    # V03-03
    fixtures["V03-03-historical-handle-newer-excerpt-refused"] = GB03SlotFixture(
        selector="V03-03-historical-handle-newer-excerpt-refused",
        family_id="V03",
        lane="sidecar",
        workspace_files=ws,
        delivery_log=DeliveryLog(records=(read_v1, read_v2)),
        scope=scope,
        scripted_turns=base_turns + (_closeout_turn('Contradictory [read#1:2 "return 200"].'),),
        expectations=(
            CitationExpectation(
                Citation("handle", '[read#1:2 "return 200"]', handle="read#1", start_line=2, excerpt="return 200", citation_id="v03-03-c1"),
                "ungrounded",
                "unverifiable",
                expected_reason="excerpt_mismatch",
            ),
        ),
    )

    # V03-04
    fixtures["V03-04-current-handle-older-excerpt-refused"] = GB03SlotFixture(
        selector="V03-04-current-handle-older-excerpt-refused",
        family_id="V03",
        lane="sidecar",
        workspace_files=ws,
        delivery_log=DeliveryLog(records=(read_v1, read_v2)),
        scope=scope,
        scripted_turns=base_turns + (_closeout_turn('Contradictory [read#2:2 "return 100"].'),),
        expectations=(
            CitationExpectation(
                Citation("handle", '[read#2:2 "return 100"]', handle="read#2", start_line=2, excerpt="return 100", citation_id="v03-04-c1"),
                "ungrounded",
                "unverifiable",
                expected_reason="excerpt_mismatch",
            ),
        ),
    )

    # V03-05
    fixtures["V03-05-handle-contradictory-path-refused"] = GB03SlotFixture(
        selector="V03-05-handle-contradictory-path-refused",
        family_id="V03",
        lane="sidecar",
        workspace_files={**ws, "internal/other.go": "package app\nfunc Other() int { return 999 }\n"},
        delivery_log=DeliveryLog(records=(read_v1, read_v2, read_other)),
        scope=scope,
        scripted_turns=base_turns + (_closeout_turn("Wrong path [read#1 internal/other.go:2]."),),
        expectations=(
            CitationExpectation(
                Citation("handle_path", "[read#1 internal/other.go:2]", handle="read#1", path="internal/other.go", start_line=2, citation_id="v03-05-c1"),
                "ungrounded",
                "unverifiable",
                expected_reason="wrong_path",
            ),
        ),
    )

    # V03-06
    cmd_receipt = _rec(
        tool="run_command",
        handle="command#1",
        kind="command",
        shape="command_result",
        raw_text="checked internal/a.go before edit: ok\n",
        source_revision="rev-1",
        session_id=sess,
    )
    fixtures["V03-06-command-receipt-survives-file-edit"] = GB03SlotFixture(
        selector="V03-06-command-receipt-survives-file-edit",
        family_id="V03",
        lane="sidecar",
        workspace_files=ws,
        delivery_log=DeliveryLog(records=(read_v1, cmd_receipt, read_v2)),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": "go vet internal/a.go"}, "tc-1"),
            _tool_turn("edit_file", {"path": "internal/a.go"}, "tc-2"),
            _closeout_turn("Prior command receipt still valid [command#1]."),
        ),
        expectations=(
            CitationExpectation(
                Citation("handle", "[command#1]", handle="command#1", citation_id="v03-06-c1"),
                "grounded",
                "matched",
                expected_supporting_handles=("command#1",),
            ),
        ),
    )

    # V03-07
    git_rec = _rec(
        tool="git_status",
        handle="git_receipt#1",
        kind="git_receipt",
        shape="git_receipt",
        raw_text="M internal/a.go\n",
        source_revision="rev-1",
        session_id=sess,
    )
    merge_rec = _rec(
        tool="overlay_merge",
        handle="overlay_receipt#1",
        kind="overlay_receipt",
        shape="overlay_receipt",
        raw_text="merged worker overlay into internal/a.go\n",
        source_revision="rev-1",
        session_id=sess,
    )
    fixtures["V03-07-git-and-merge-receipts-survive-file-edit"] = GB03SlotFixture(
        selector="V03-07-git-and-merge-receipts-survive-file-edit",
        family_id="V03",
        lane="sidecar",
        workspace_files=ws,
        delivery_log=DeliveryLog(records=(read_v1, git_rec, merge_rec, read_v2)),
        scope=scope,
        scripted_turns=(
            _tool_turn("git_status", {}, "tc-1"),
            _tool_turn("edit_file", {"path": "internal/a.go"}, "tc-2"),
            _closeout_turn("Receipts survive edit [git_receipt#1] and [overlay_receipt#1]."),
        ),
        expectations=(
            CitationExpectation(Citation("handle", "[git_receipt#1]", handle="git_receipt#1", citation_id="v03-07-git"), "grounded", "matched", expected_supporting_handles=("git_receipt#1",)),
            CitationExpectation(Citation("handle", "[overlay_receipt#1]", handle="overlay_receipt#1", citation_id="v03-07-merge"), "grounded", "matched", expected_supporting_handles=("overlay_receipt#1",)),
        ),
    )

    # V03-08: trust survives supersession
    untrusted_v1 = _rec(
        tool="read_file",
        handle="read#1",
        kind="read",
        shape="file_region",
        path="internal/a.go",
        raw_text=old_body,
        ranges=(LineRange(1, 3),),
        line_numbered=True,
        source_revision="rev-1",
        superseded_by="read#2",
        trust="untrusted",
        session_id=sess,
    )
    trusted_v2 = _rec(
        tool="read_file",
        handle="read#2",
        kind="read",
        shape="file_region",
        path="internal/a.go",
        raw_text=new_body,
        ranges=(LineRange(1, 3),),
        line_numbered=True,
        source_revision="rev-2",
        trust="trusted",
        session_id=sess,
    )
    fixtures["V03-08-trust-survives-supersession"] = GB03SlotFixture(
        selector="V03-08-trust-survives-supersession",
        family_id="V03",
        lane="sidecar",
        workspace_files=ws,
        delivery_log=DeliveryLog(records=(untrusted_v1, trusted_v2)),
        scope=scope,
        scripted_turns=base_turns + (_closeout_turn("Both [read#1] and [read#2] keep their recorded trust."),),
        expectations=(
            CitationExpectation(Citation("handle", "[read#1]", handle="read#1", citation_id="v03-08-c1"), "grounded", "matched", expected_supporting_handles=("read#1",)),
            CitationExpectation(Citation("handle", "[read#2]", handle="read#2", citation_id="v03-08-c2"), "grounded", "matched", expected_supporting_handles=("read#2",)),
        ),
    )

    return fixtures


# ---------------------------------------------------------------------------
# V04 · Command observations (10 sidecar slots, incl. Decision 005/014 slot 10)
# ---------------------------------------------------------------------------

def _build_v04_fixtures() -> dict[str, GB03SlotFixture]:
    sess = "sess-v04"
    scope = PermittedScope(session_id=sess, round_id="round-1", intent_window="intent-1")

    a_src = "package app\nfunc Alpha() int { return 1 }\n// end alpha\n"
    b_src = "package app\nfunc Beta() int { return 2 }\n// end beta\n"

    a_numbered = "     1\tpackage app\n     2\tfunc Alpha() int { return 1 }\n     3\t// end alpha\n"
    # Continuous numbering from `cat -n internal/a.go internal/b.go`: b.go lines 1..3 printed as 4..6
    b_continuous_numbered = "     4\tpackage app\n     5\tfunc Beta() int { return 2 }\n     6\t// end beta\n"

    cmd_cat_n_two = _rec(
        tool="run_command",
        handle="command#1",
        kind="command",
        shape="command_result",
        raw_text=a_numbered + b_continuous_numbered,
        arguments={"command": "cat -n internal/a.go internal/b.go"},
        session_id=sess,
    )
    read_a_from_cat = _rec(
        tool="run_command",
        handle="read#1",
        kind="read",
        shape="file_region",
        path="internal/a.go",
        raw_text=a_numbered,
        ranges=(LineRange(1, 3),),
        tags=("[read#1 internal/a.go:1-3]",),
        line_numbered=True,
        printed_other_numbers=False,
        session_id=sess,
    )
    read_b_from_cat = _rec(
        tool="run_command",
        handle="read#2",
        kind="read",
        shape="file_region",
        path="internal/b.go",
        raw_text=b_continuous_numbered,
        ranges=(LineRange(1, 3),),
        tags=("[read#2 internal/b.go:1-3]",),
        line_numbered=False,
        printed_other_numbers=True,
        session_id=sess,
    )

    fixtures: dict[str, GB03SlotFixture] = {}

    # V04-01
    cmd_basic = _rec(
        tool="run_command",
        handle="command#1",
        kind="command",
        shape="command_result",
        raw_text="ok 2 files\n",
        arguments={"command": "ls internal"},
        session_id=sess,
    )
    fixtures["V04-01-command-handle-carried"] = GB03SlotFixture(
        selector="V04-01-command-handle-carried",
        family_id="V04",
        lane="sidecar",
        workspace_files={"internal/a.go": a_src, "internal/b.go": b_src},
        delivery_log=DeliveryLog(records=(cmd_basic,)),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": "ls internal"}),
            _closeout_turn("Directory listing captured [command#1]."),
        ),
        expectations=(
            CitationExpectation(Citation("handle", "[command#1]", handle="command#1", citation_id="v04-01-c1"), "grounded", "matched", expected_supporting_handles=("command#1",)),
        ),
    )

    # V04-02: F3 contrast (a) correctly numbered
    fixtures["V04-02-f3-correctly-numbered-single-file"] = GB03SlotFixture(
        selector="V04-02-f3-correctly-numbered-single-file",
        family_id="V04",
        lane="sidecar",
        workspace_files={"internal/a.go": a_src},
        delivery_log=DeliveryLog(records=(cmd_cat_n_two, read_a_from_cat)),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": "cat -n internal/a.go"}),
            _closeout_turn("Alpha defined at [read#1:2]."),
        ),
        expectations=(
            CitationExpectation(Citation("handle", "[read#1:2]", handle="read#1", start_line=2, citation_id="v04-02-c1"), "grounded", "matched", expected_reason="numbered_coordinate", expected_supporting_handles=("read#1",)),
        ),
    )

    # V04-03: F3 contrast (b) unnumbered source delivery
    read_unnumbered = _rec(
        tool="run_command",
        handle="read#1",
        kind="read",
        shape="file_region",
        path="internal/a.go",
        raw_text=a_src,
        ranges=(LineRange(1, 3),),
        tags=("[read#1 internal/a.go:1-3]",),
        line_numbered=False,
        printed_other_numbers=False,
        session_id=sess,
    )
    fixtures["V04-03-f3-unnumbered-source-delivery"] = GB03SlotFixture(
        selector="V04-03-f3-unnumbered-source-delivery",
        family_id="V04",
        lane="sidecar",
        workspace_files={"internal/a.go": a_src},
        delivery_log=DeliveryLog(records=(cmd_basic, read_unnumbered)),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": "cat internal/a.go"}),
            _closeout_turn("Unnumbered coordinate is traced [read#1:2]."),
        ),
        expectations=(
            CitationExpectation(Citation("handle", "[read#1:2]", handle="read#1", start_line=2, citation_id="v04-03-c1"), "grounded", "traced", expected_reason="traced_range", expected_supporting_handles=("read#1",)),
        ),
    )

    # V04-04: multi-run split per file and consecutive run
    run1 = _rec(tool="run_command", handle="read#1", kind="read", shape="file_region", path="internal/a.go", raw_text="1: package app\n2: // header\n", ranges=(LineRange(1, 2),), line_numbered=True, session_id=sess)
    run2 = _rec(tool="run_command", handle="read#2", kind="read", shape="file_region", path="internal/a.go", raw_text="10: func Tail() {}\n11: // footer\n", ranges=(LineRange(10, 11),), line_numbered=True, session_id=sess)
    fixtures["V04-04-multi-run-split-per-file-and-run"] = GB03SlotFixture(
        selector="V04-04-multi-run-split-per-file-and-run",
        family_id="V04",
        lane="sidecar",
        workspace_files={"internal/a.go": a_src},
        delivery_log=DeliveryLog(records=(cmd_basic, run1, run2)),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": "nl -ba internal/a.go | sed -n '1,2p;10,11p'"}),
            _closeout_turn("Head [read#1:1] and tail [read#2:10] delivered; gap line 5 is uncovered."),
        ),
        expectations=(
            CitationExpectation(Citation("handle", "[read#1:1]", handle="read#1", start_line=1, citation_id="v04-04-r1"), "grounded", "matched", expected_supporting_handles=("read#1",)),
            CitationExpectation(Citation("handle", "[read#2:10]", handle="read#2", start_line=10, citation_id="v04-04-r2"), "grounded", "matched", expected_supporting_handles=("read#2",)),
            CitationExpectation(Citation("path_coord", "[internal/a.go:5]", path="internal/a.go", start_line=5, citation_id="v04-04-gap"), "ungrounded", "unverifiable", expected_reason="uncovered_range"),
        ),
    )

    # V04-05: 16 delivery-tag cap
    capped_files = {f"internal/f{i:02d}.go": f"package app\nfunc F{i:02d}() int {{ return {i} }}\n" for i in range(1, 21)}
    capped_tags = tuple(f"[read#{i} internal/f{i:02d}.go:1-2]" for i in range(1, 17))
    cmd_capped = _rec(
        tool="run_command",
        handle="command#1",
        kind="command",
        shape="command_result",
        raw_text="printed 20 numbered files; host capped source delivery tags at 16\n",
        tags=("[command#1]",) + capped_tags,
        arguments={"command": "cat -n internal/f*.go"},
        session_id=sess,
    )
    capped_reads = tuple(
        _rec(
            tool="run_command",
            handle=f"read#{i}",
            kind="read",
            shape="file_region",
            path=f"internal/f{i:02d}.go",
            raw_text=f"1: package app\n2: func F{i:02d}() int {{ return {i} }}\n",
            ranges=(LineRange(1, 2),),
            tags=(f"[read#{i} internal/f{i:02d}.go:1-2]",),
            line_numbered=True,
            session_id=sess,
        )
        for i in range(1, 17)
    )
    fixtures["V04-05-sixteen-delivery-tag-cap"] = GB03SlotFixture(
        selector="V04-05-sixteen-delivery-tag-cap",
        family_id="V04",
        lane="sidecar",
        workspace_files=capped_files,
        delivery_log=DeliveryLog(records=(cmd_capped,) + capped_reads),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": "cat -n internal/f*.go"}),
            _closeout_turn("First 16 tagged [read#16:2]; command handle [command#1] covers full run."),
        ),
        expectations=(
            CitationExpectation(Citation("handle", "[command#1]", handle="command#1", citation_id="v04-05-cmd"), "grounded", "matched", expected_supporting_handles=("command#1",)),
            CitationExpectation(Citation("handle", "[read#16:2]", handle="read#16", start_line=2, citation_id="v04-05-r16"), "grounded", "matched", expected_supporting_handles=("read#16",)),
            CitationExpectation(Citation("path_coord", "[internal/f17.go:2]", path="internal/f17.go", start_line=2, citation_id="v04-05-f17"), "ungrounded", "unverifiable", expected_reason="path_not_observed"),
        ),
        metadata={"max_source_delivery_tags": 16, "input_file_count": 20},
    )

    # V04-06: opaque command prints path, creates no read authority
    cmd_opaque = _rec(
        tool="run_command",
        handle="command#1",
        kind="command",
        shape="command_result",
        raw_text="internal/secret.go:10: fake_printed_line\n",
        arguments={"command": "echo 'internal/secret.go:10: fake_printed_line'"},
        session_id=sess,
    )
    fixtures["V04-06-opaque-command-no-read-authority"] = GB03SlotFixture(
        selector="V04-06-opaque-command-no-read-authority",
        family_id="V04",
        lane="sidecar",
        workspace_files={"internal/secret.go": "package app\n"},
        delivery_log=DeliveryLog(records=(cmd_opaque,)),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": "echo 'internal/secret.go:10: fake_printed_line'"}),
            _closeout_turn("Only [command#1] is citable; [internal/secret.go:10] has no read authority."),
        ),
        expectations=(
            CitationExpectation(Citation("handle", "[command#1]", handle="command#1", citation_id="v04-06-cmd"), "grounded", "matched", expected_supporting_handles=("command#1",)),
            CitationExpectation(Citation("path_coord", "[internal/secret.go:10]", path="internal/secret.go", start_line=10, citation_id="v04-06-path"), "ungrounded", "unverifiable", expected_reason="path_not_observed"),
        ),
    )

    # V04-07: line filter in pipeline leaves only command# citable
    cmd_filtered = _rec(
        tool="run_command",
        handle="command#1",
        kind="command",
        shape="command_result",
        raw_text="     2\tfunc Alpha() int { return 1 }\n",
        arguments={"command": "cat -n internal/a.go | grep -v package"},
        session_id=sess,
    )
    fixtures["V04-07-line-filter-pipeline-command-only"] = GB03SlotFixture(
        selector="V04-07-line-filter-pipeline-command-only",
        family_id="V04",
        lane="sidecar",
        workspace_files={"internal/a.go": a_src},
        delivery_log=DeliveryLog(records=(cmd_filtered,)),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": "cat -n internal/a.go | grep -v package"}),
            _closeout_turn("Filtered pipeline leaves only [command#1] citable."),
        ),
        expectations=(
            CitationExpectation(Citation("handle", "[command#1]", handle="command#1", citation_id="v04-07-cmd"), "grounded", "matched", expected_supporting_handles=("command#1",)),
            CitationExpectation(Citation("path_coord", "[internal/a.go:2]", path="internal/a.go", start_line=2, citation_id="v04-07-path"), "ungrounded", "unverifiable", expected_reason="path_not_observed"),
        ),
    )

    # V04-08: F3 contrast (c) mismatched printed numbers without excerpt
    fixtures["V04-08-f3-mismatched-printed-numbers-no-excerpt"] = GB03SlotFixture(
        selector="V04-08-f3-mismatched-printed-numbers-no-excerpt",
        family_id="V04",
        lane="sidecar",
        workspace_files={"internal/a.go": a_src, "internal/b.go": b_src},
        delivery_log=DeliveryLog(records=(cmd_cat_n_two, read_a_from_cat, read_b_from_cat)),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": "cat -n internal/a.go internal/b.go"}),
            _closeout_turn("Inferred unexcerpted coordinate on second file [read#2:2]."),
        ),
        expectations=(
            CitationExpectation(
                Citation("handle", "[read#2:2]", handle="read#2", start_line=2, citation_id="v04-08-c1"),
                expected_verdict_class="ungrounded",
                expected_exact_verdict="unverifiable",
                expected_reason="mismatched_printed_numbers_no_excerpt",
                expected_contract_reading="traced",
                expected_observed_product_rule="printed_other_numbers_rejects_unexcerpted_coord",
                expected_disputed=True,
            ),
        ),
    )

    # V04-09: F3 contrast (d) mismatched printed numbers WITH verified excerpt
    fixtures["V04-09-f3-mismatched-printed-numbers-with-excerpt"] = GB03SlotFixture(
        selector="V04-09-f3-mismatched-printed-numbers-with-excerpt",
        family_id="V04",
        lane="sidecar",
        workspace_files={"internal/a.go": a_src, "internal/b.go": b_src},
        delivery_log=DeliveryLog(records=(cmd_cat_n_two, read_a_from_cat, read_b_from_cat)),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": "cat -n internal/a.go internal/b.go"}),
            _closeout_turn('Second file with verified excerpt [read#2:2 "func Beta() int"].'),
        ),
        expectations=(
            CitationExpectation(
                Citation("handle", '[read#2:2 "func Beta() int"]', handle="read#2", start_line=2, excerpt="func Beta() int", citation_id="v04-09-c1"),
                expected_verdict_class="grounded",
                expected_exact_verdict="matched",
                expected_reason="exact_match",
                expected_supporting_handles=("read#2",),
            ),
        ),
    )

    # V04-10: Decision 005 / Decision 014 copied host-issued second-file delivery tag
    copied_tag = "[read#2 internal/b.go:1-3]"
    fixtures["V04-10-copied-second-file-tag"] = GB03SlotFixture(
        selector="V04-10-copied-second-file-tag",
        family_id="V04",
        lane="sidecar",
        workspace_files={"internal/a.go": a_src, "internal/b.go": b_src},
        delivery_log=DeliveryLog(records=(cmd_cat_n_two, read_a_from_cat, read_b_from_cat)),
        scope=scope,
        scripted_turns=(
            _tool_turn("run_command", {"command": "cat -n internal/a.go internal/b.go"}),
            _closeout_turn(f"Copied exact host-issued second-file tag {copied_tag}."),
        ),
        expectations=(
            CitationExpectation(
                Citation(
                    citation_type="handle_path",
                    raw=copied_tag,
                    handle="read#2",
                    path="internal/b.go",
                    start_line=1,
                    end_line=3,
                    citation_id="v04-10-copied-tag",
                ),
                expected_verdict_class="grounded",
                expected_exact_verdict="matched",
                expected_reason="copied_host_delivery_tag",
                expected_supporting_handles=("read#2",),
                expected_contract_reading="matched",
                expected_observed_product_rule="printed_other_numbers_rejects_unexcerpted_coord",
                expected_disputed=True,
            ),
        ),
        metadata={
            "decision": "005/014",
            "copied_delivery_tag": copied_tag,
            "displayed_continuous_range": "4-6",
            "true_file_range": "1-3",
        },
    )

    return fixtures


# ---------------------------------------------------------------------------
# V05 · Compaction and recall (5 sidecar slots)
# ---------------------------------------------------------------------------

def _build_v05_fixtures() -> dict[str, GB03SlotFixture]:
    sess = "sess-v05"
    scope = PermittedScope(session_id=sess, round_id="round-1", intent_window="intent-1")
    app_src = "package app\nfunc Core() string { return \"compacted_ok\" }\n"
    app_num = "1: package app\n2: func Core() string { return \"compacted_ok\" }\n"

    read_rec = _rec(
        tool="read_file",
        handle="read#1",
        kind="read",
        shape="file_region",
        path="internal/core.go",
        raw_text=app_num,
        ranges=(LineRange(1, 2),),
        line_numbered=True,
        session_id=sess,
    )
    grep_rec = _rec(
        tool="grep_files",
        handle="grep#1",
        kind="grep",
        shape="search_matches",
        path="internal/core.go",
        raw_text="2: func Core() string { return \"compacted_ok\" }\n",
        ranges=(LineRange(2, 2),),
        line_numbered=True,
        session_id=sess,
    )
    src_change_rec = _rec(
        tool="source_watcher",
        handle="source_change#1",
        kind="source_change",
        shape="structured_event",
        path="internal/core.go",
        raw_text="modified internal/core.go\n",
        session_id=sess,
    )
    screened_rec = _rec(
        tool="run_command",
        handle="command#1",
        kind="command",
        shape="command_result",
        raw_text=f"API_KEY={D16_REDACTED_MARKER}\nstatus=ok\n",
        arguments={"command": "cat config.env"},
        session_id=sess,
    )

    return {
        "V05-01-compaction-preserves-evidence-by-digest": GB03SlotFixture(
            selector="V05-01-compaction-preserves-evidence-by-digest",
            family_id="V05",
            lane="sidecar",
            workspace_files={"internal/core.go": app_src},
            delivery_log=DeliveryLog(records=(read_rec,)),
            scope=scope,
            scripted_turns=(
                _tool_turn("read_file", {"path": "internal/core.go"}, "tc-1"),
                _closeout_turn("Post-compaction digest intact [read#1:2]."),
            ),
            expectations=(
                CitationExpectation(Citation("handle", "[read#1:2]", handle="read#1", start_line=2, citation_id="v05-01-c1"), "grounded", "matched", expected_supporting_handles=("read#1",)),
            ),
            expected_recallable_handles=("read#1",),
        ),
        "V05-02-recall-by-handle-after-compaction": GB03SlotFixture(
            selector="V05-02-recall-by-handle-after-compaction",
            family_id="V05",
            lane="sidecar",
            workspace_files={"internal/core.go": app_src},
            delivery_log=DeliveryLog(records=(read_rec,)),
            scope=scope,
            scripted_turns=(
                _tool_turn("recall", {"handle": "read#1"}, "tc-1"),
                _closeout_turn('Recalled body [read#1:2 "compacted_ok"].'),
            ),
            expectations=(
                CitationExpectation(Citation("handle", '[read#1:2 "compacted_ok"]', handle="read#1", start_line=2, excerpt="compacted_ok", citation_id="v05-02-c1"), "grounded", "matched", expected_supporting_handles=("read#1",)),
            ),
            expected_recallable_handles=("read#1",),
        ),
        "V05-03-recalled-search-index-not-fresh-read": GB03SlotFixture(
            selector="V05-03-recalled-search-index-not-fresh-read",
            family_id="V05",
            lane="sidecar",
            workspace_files={"internal/core.go": app_src},
            delivery_log=DeliveryLog(records=(grep_rec,)),
            scope=scope,
            scripted_turns=(
                _tool_turn("recall", {"handle": "grep#1"}, "tc-1"),
                _closeout_turn("Index match [grep#1] does not cover unread line 50."),
            ),
            expectations=(
                CitationExpectation(Citation("handle", "[grep#1]", handle="grep#1", citation_id="v05-03-grep"), "grounded", "matched", expected_supporting_handles=("grep#1",)),
                CitationExpectation(Citation("path_coord", "[internal/core.go:50]", path="internal/core.go", start_line=50, citation_id="v05-03-unread"), "ungrounded", "unverifiable", expected_reason="uncovered_range"),
            ),
            expected_recallable_handles=("grep#1",),
        ),
        "V05-04-source-change-excluded-from-recall": GB03SlotFixture(
            selector="V05-04-source-change-excluded-from-recall",
            family_id="V05",
            lane="sidecar",
            workspace_files={"internal/core.go": app_src},
            delivery_log=DeliveryLog(records=(src_change_rec,)),
            scope=scope,
            scripted_turns=(
                _tool_turn("recall", {"handle": "source_change#1"}, "tc-1"),
                _closeout_turn("Brief row [source_change#1] is not recallable and carries no line proof."),
            ),
            expectations=(
                CitationExpectation(Citation("handle", "[source_change#1]", handle="source_change#1", citation_id="v05-04-brief"), "grounded", "matched", expected_reason="source_change_brief_row", expected_supporting_handles=("source_change#1",)),
                CitationExpectation(Citation("handle", "[source_change#1:1]", handle="source_change#1", start_line=1, citation_id="v05-04-coord"), "ungrounded", "unverifiable", expected_reason="source_change_brief_only"),
            ),
            expected_has_citable_observation=False,
            expected_non_recallable_handles=("source_change#1",),
        ),
        "V05-05-d16-secret-canary-screened-before-commit": GB03SlotFixture(
            selector="V05-05-d16-secret-canary-screened-before-commit",
            family_id="V05",
            lane="sidecar",
            workspace_files={"config.env": f"API_KEY={D16_RAW_SECRET_CANARY}\nstatus=ok\n"},
            delivery_log=DeliveryLog(records=(screened_rec,)),
            scope=scope,
            scripted_turns=(
                _tool_turn("run_command", {"command": "cat config.env"}, "tc-1"),
                _closeout_turn('Screened output [command#1 "status=ok"].'),
            ),
            expectations=(
                CitationExpectation(Citation("handle", '[command#1 "status=ok"]', handle="command#1", excerpt="status=ok", citation_id="v05-05-ok"), "grounded", "matched", expected_supporting_handles=("command#1",)),
                CitationExpectation(Citation("handle", f'[command#1 "{D16_RAW_SECRET_CANARY}"]', handle="command#1", excerpt=D16_RAW_SECRET_CANARY, citation_id="v05-05-canary"), "ungrounded", "unverifiable", expected_reason="excerpt_mismatch"),
            ),
            expected_forbidden_body_substrings=(D16_RAW_SECRET_CANARY,),
        ),
    }


# ---------------------------------------------------------------------------
# V06 · Scope and provenance (10 sidecar slots)
# ---------------------------------------------------------------------------

def _build_v06_fixtures() -> dict[str, GB03SlotFixture]:
    coord_sess = "sess-coord"
    wa_sess = "sess-worker-a"
    wb_sess = "sess-worker-b"
    chat2_sess = "sess-chat-2"
    proj2_sess = "sess-proj-2"

    coord_read = _rec(
        tool="read_file", handle="read#1", kind="read", shape="file_region",
        path="internal/coord.go", raw_text="1: package coord\n2: func Coord() {}\n",
        ranges=(LineRange(1, 2),), line_numbered=True, session_id=coord_sess,
    )
    coord_web = _rec(
        tool="web_fetch", handle="web#1", kind="web", shape="url_document",
        url="https://parent.example.test/spec", raw_text="parent spec\n",
        trust="untrusted", session_id=coord_sess,
    )
    wa_read = _rec(
        tool="read_file", handle="read#1", kind="read", shape="file_region",
        path="internal/wa.go", raw_text="1: package wa\n2: func WorkerA() {}\n",
        ranges=(LineRange(1, 2),), line_numbered=True, session_id=wa_sess, worker_leg="worker-a", round_id="round-1",
    )
    wa_r2_read = _rec(
        tool="read_file", handle="read#2", kind="read", shape="file_region",
        path="internal/wa.go", raw_text="1: package wa\n2: func WorkerAV2() {}\n",
        ranges=(LineRange(1, 2),), line_numbered=True, session_id=wa_sess, worker_leg="worker-a", round_id="round-2",
    )
    wb_read = _rec(
        tool="read_file", handle="read#1", kind="read", shape="file_region",
        path="internal/wb.go", raw_text="1: package wb\n2: func WorkerB() {}\n",
        ranges=(LineRange(1, 2),), line_numbered=True, session_id=wb_sess, worker_leg="worker-b", round_id="round-1",
    )
    chat2_read = _rec(
        tool="read_file", handle="read#9", kind="read", shape="file_region",
        path="internal/chat2.go", raw_text="1: package chat2\n2: func Chat2() {}\n",
        ranges=(LineRange(1, 2),), line_numbered=True, session_id=chat2_sess,
    )
    proj2_read = _rec(
        tool="read_file", handle="read#10", kind="read", shape="file_region",
        path="internal/proj2.go", raw_text="1: package proj2\n2: func Proj2() {}\n",
        ranges=(LineRange(1, 2),), line_numbered=True, session_id=proj2_sess,
    )

    ws = {
        "internal/coord.go": "package coord\nfunc Coord() {}\n",
        "internal/wa.go": "package wa\nfunc WorkerA() {}\n",
        "internal/wb.go": "package wb\nfunc WorkerB() {}\n",
    }

    coord_scope_two_workers = PermittedScope(
        session_id=coord_sess,
        dispatched_legs=(("worker-a", wa_sess), ("worker-b", wb_sess)),
        round_id="round-1",
        intent_window="intent-1",
    )
    wa_scope_r1 = PermittedScope(session_id=wa_sess, worker_leg="worker-a", round_id="round-1", intent_window="intent-1")

    return {
        "V06-01-coordinator-own-observations": GB03SlotFixture(
            selector="V06-01-coordinator-own-observations",
            family_id="V06",
            lane="sidecar",
            workspace_files=ws,
            delivery_log=DeliveryLog(records=(coord_read,)),
            scope=coord_scope_two_workers,
            scripted_turns=(_tool_turn("read_file", {"path": "internal/coord.go"}), _closeout_turn("Own read [read#1:2].")),
            expectations=(CitationExpectation(Citation("handle", "[read#1:2]", handle="read#1", start_line=2, citation_id="v06-01-c1"), "grounded", "matched", expected_supporting_handles=("read#1",)),),
        ),
        "V06-02-coordinator-dispatched-worker-a-namespaced": GB03SlotFixture(
            selector="V06-02-coordinator-dispatched-worker-a-namespaced",
            family_id="V06",
            lane="sidecar",
            workspace_files=ws,
            delivery_log=DeliveryLog(records=(coord_read, wa_read)),
            scope=coord_scope_two_workers,
            scripted_turns=(_closeout_turn("Dispatched leg [worker-a:read#1:2]."),),
            expectations=(CitationExpectation(Citation("handle", "[worker-a:read#1:2]", handle="worker-a:read#1", start_line=2, citation_id="v06-02-c1"), "grounded", "matched", expected_supporting_handles=("worker-a:read#1",)),),
        ),
        "V06-03-coordinator-two-workers-leg-namespaced-union": GB03SlotFixture(
            selector="V06-03-coordinator-two-workers-leg-namespaced-union",
            family_id="V06",
            lane="sidecar",
            workspace_files=ws,
            delivery_log=DeliveryLog(records=(coord_read, wa_read, wb_read, chat2_read)),
            scope=coord_scope_two_workers,
            scripted_turns=(_closeout_turn("Union [read#1:2], [worker-a:read#1:2], [worker-b:read#1:2]."),),
            expectations=(
                CitationExpectation(Citation("handle", "[worker-a:read#1:2]", handle="worker-a:read#1", start_line=2, citation_id="v06-03-wa"), "grounded", "matched", expected_supporting_handles=("worker-a:read#1",)),
                CitationExpectation(Citation("handle", "[worker-b:read#1:2]", handle="worker-b:read#1", start_line=2, citation_id="v06-03-wb"), "grounded", "matched", expected_supporting_handles=("worker-b:read#1",)),
            ),
            expected_union_handles=("read#1", "worker-a:read#1", "worker-b:read#1"),
        ),
        "V06-04-worker-read-not-parent-first-hand": GB03SlotFixture(
            selector="V06-04-worker-read-not-parent-first-hand",
            family_id="V06",
            lane="sidecar",
            workspace_files=ws,
            delivery_log=DeliveryLog(records=(wa_read,)),
            scope=coord_scope_two_workers,
            scripted_turns=(_closeout_turn("Unnamespaced [read#1:2] is not parent first-hand."),),
            expectations=(
                CitationExpectation(Citation("handle", "[read#1:2]", handle="read#1", start_line=2, citation_id="v06-04-unprefixed"), "ungrounded", "unverifiable", expected_reason="wrong_session"),
                CitationExpectation(Citation("handle", "[worker-a:read#1:2]", handle="worker-a:read#1", start_line=2, citation_id="v06-04-prefixed"), "grounded", "matched", expected_supporting_handles=("worker-a:read#1",)),
            ),
        ),
        "V06-05-worker-cannot-cite-sibling-worker": GB03SlotFixture(
            selector="V06-05-worker-cannot-cite-sibling-worker",
            family_id="V06",
            lane="sidecar",
            workspace_files=ws,
            delivery_log=DeliveryLog(records=(wa_read, wb_read)),
            scope=wa_scope_r1,
            scripted_turns=(_closeout_turn("Worker A cannot cite [worker-b:read#1:2]."),),
            expectations=(
                CitationExpectation(Citation("handle", "[worker-b:read#1:2]", handle="worker-b:read#1", start_line=2, citation_id="v06-05-sib"), "ungrounded", "unverifiable", expected_reason="wrong_leg"),
            ),
        ),
        "V06-06-worker-cannot-borrow-parent-evidence": GB03SlotFixture(
            selector="V06-06-worker-cannot-borrow-parent-evidence",
            family_id="V06",
            lane="sidecar",
            workspace_files=ws,
            delivery_log=DeliveryLog(records=(coord_read, coord_web, wa_read)),
            scope=wa_scope_r1,
            scripted_turns=(_closeout_turn("Worker A cannot borrow parent URL or read."),),
            expectations=(
                CitationExpectation(Citation("url", "https://parent.example.test/spec", url="https://parent.example.test/spec", citation_id="v06-06-url"), "ungrounded", "unverifiable", expected_reason="url_not_observed"),
                CitationExpectation(Citation("path_coord", "[internal/coord.go:2]", path="internal/coord.go", start_line=2, citation_id="v06-06-path"), "ungrounded", "unverifiable", expected_reason="path_not_observed"),
            ),
        ),
        "V06-07-second-chat-isolation": GB03SlotFixture(
            selector="V06-07-second-chat-isolation",
            family_id="V06",
            lane="sidecar",
            workspace_files=ws,
            delivery_log=DeliveryLog(records=(coord_read, chat2_read)),
            scope=coord_scope_two_workers,
            scripted_turns=(_closeout_turn("Second chat handle [read#9:2] never resolves."),),
            expectations=(
                CitationExpectation(Citation("handle", "[read#9:2]", handle="read#9", start_line=2, citation_id="v06-07-c1"), "ungrounded", "unverifiable", expected_reason="wrong_session"),
            ),
        ),
        "V06-08-second-project-isolation": GB03SlotFixture(
            selector="V06-08-second-project-isolation",
            family_id="V06",
            lane="sidecar",
            workspace_files=ws,
            delivery_log=DeliveryLog(records=(coord_read, proj2_read)),
            scope=coord_scope_two_workers,
            scripted_turns=(_closeout_turn("Second project handle [read#10:2] never resolves."),),
            expectations=(
                CitationExpectation(Citation("handle", "[read#10:2]", handle="read#10", start_line=2, citation_id="v06-08-c1"), "ungrounded", "unverifiable", expected_reason="wrong_session"),
            ),
        ),
        "V06-09-worker-round-boundary-cited-results": GB03SlotFixture(
            selector="V06-09-worker-round-boundary-cited-results",
            family_id="V06",
            lane="sidecar",
            workspace_files=ws,
            delivery_log=DeliveryLog(records=(wa_read, wa_r2_read)),
            scope=PermittedScope(
                session_id=wa_sess,
                worker_leg="worker-a",
                round_id="round-2",
                intent_window="intent-1",
                cited_results_handles=frozenset({"read#1"}),
            ),
            scripted_turns=(_closeout_turn("Round 2 cites current [read#2:2] and historical [read#1:2]."),),
            expectations=(
                CitationExpectation(Citation("handle", "[read#2:2]", handle="read#2", start_line=2, citation_id="v06-09-r2"), "grounded", "matched", expected_supporting_handles=("read#2",)),
                CitationExpectation(Citation("handle", "[read#1:2]", handle="read#1", start_line=2, citation_id="v06-09-r1"), "grounded", "matched", expected_reason="cited_results_historical", expected_supporting_handles=("read#1",)),
            ),
            metadata={"unlisted_round_1_expected_reason": "prior_round_source_evidence"},
        ),
        "V06-10-workspace-refresh-rejection-invalidation": GB03SlotFixture(
            selector="V06-10-workspace-refresh-rejection-invalidation",
            family_id="V06",
            lane="sidecar",
            workspace_files=ws,
            delivery_log=DeliveryLog(records=(wa_read, wa_r2_read)),
            scope=PermittedScope(
                session_id=wa_sess,
                worker_leg="worker-a",
                round_id="round-2",
                intent_window="intent-1",
                cited_results_handles=frozenset({"read#1"}),
                workspace_refreshed=True,
                invalidated_rounds=frozenset({"round-1"}),
            ),
            scripted_turns=(_closeout_turn("Refreshed workspace invalidates [read#1:2]; only [read#2:2] stands."),),
            expectations=(
                CitationExpectation(Citation("handle", "[read#1:2]", handle="read#1", start_line=2, citation_id="v06-10-stale"), "ungrounded", "unverifiable", expected_reason="stale_round_invalidated"),
                CitationExpectation(Citation("handle", "[read#2:2]", handle="read#2", start_line=2, citation_id="v06-10-fresh"), "grounded", "matched", expected_supporting_handles=("read#2",)),
            ),
        ),
    }


# ---------------------------------------------------------------------------
# V07 · Restart and an unreadable ledger (2 sidecar + 4 in-process slots)
# ---------------------------------------------------------------------------

def _build_v07_fixtures() -> dict[str, GB03SlotFixture]:
    sess = "sess-v07"
    scope = PermittedScope(session_id=sess, round_id="round-1", intent_window="intent-1")
    a_src = "package app\nfunc Persistent() int { return 7 }\n"
    b_src = "package app\nfunc AfterRestart() int { return 8 }\n"

    r1 = _rec(
        tool="read_file", handle="read#1", kind="read", shape="file_region",
        path="internal/a.go", raw_text="1: package app\n2: func Persistent() int { return 7 }\n",
        ranges=(LineRange(1, 2),), line_numbered=True, session_id=sess,
    )
    r2 = _rec(
        tool="read_file", handle="read#2", kind="read", shape="file_region",
        path="internal/b.go", raw_text="1: package app\n2: func AfterRestart() int { return 8 }\n",
        ranges=(LineRange(1, 2),), line_numbered=True, session_id=sess,
    )
    unreadable_log = DeliveryLog(records=(r1,), readable=False)

    return {
        "V07-01-sidecar-restart-after-commit-no-handle-reuse": GB03SlotFixture(
            selector="V07-01-sidecar-restart-after-commit-no-handle-reuse",
            family_id="V07",
            lane="sidecar",
            workspace_files={"internal/a.go": a_src, "internal/b.go": b_src},
            delivery_log=DeliveryLog(records=(r1, r2)),
            scope=scope,
            scripted_turns=(
                _tool_turn("read_file", {"path": "internal/a.go"}, "tc-pre-restart"),
                _tool_turn("read_file", {"path": "internal/b.go"}, "tc-post-restart"),
                _closeout_turn("Pre-restart [read#1:2] and post-restart [read#2:2] never collide."),
            ),
            expectations=(
                CitationExpectation(Citation("handle", "[read#1:2]", handle="read#1", start_line=2, citation_id="v07-01-r1"), "grounded", "matched", expected_supporting_handles=("read#1",)),
                CitationExpectation(Citation("handle", "[read#2:2]", handle="read#2", start_line=2, citation_id="v07-01-r2"), "grounded", "matched", expected_supporting_handles=("read#2",)),
            ),
        ),
        "V07-02-sidecar-unreadable-ledger-no-exemption": GB03SlotFixture(
            selector="V07-02-sidecar-unreadable-ledger-no-exemption",
            family_id="V07",
            lane="sidecar",
            workspace_files={"internal/a.go": a_src},
            delivery_log=unreadable_log,
            scope=scope,
            scripted_turns=(_closeout_turn("Unreadable ledger grants no exemption [read#1:2]."),),
            expectations=(
                CitationExpectation(Citation("handle", "[read#1:2]", handle="read#1", start_line=2, citation_id="v07-02-c1"), "ungrounded", "unverifiable", expected_reason="unreadable_ledger"),
            ),
            expected_has_citable_observation=None,
            expected_union_handles=(),
        ),
        "V07-03-inprocess-restart-before-commit-boundary": GB03SlotFixture(
            selector="V07-03-inprocess-restart-before-commit-boundary",
            family_id="V07",
            lane="in_process",
            workspace_files={"internal/a.go": a_src},
            delivery_log=DeliveryLog(records=(r1,)),
            scope=scope,
            scripted_turns=(),
            expectations=(
                CitationExpectation(Citation("handle", "[read#1:2]", handle="read#1", start_line=2, citation_id="v07-03-r1"), "grounded", "matched", expected_supporting_handles=("read#1",)),
                CitationExpectation(Citation("handle_path", "[read#2 internal/b.go:2]", handle="read#2", path="internal/b.go", start_line=2, citation_id="v07-03-uncommitted"), "ungrounded", "unverifiable", expected_reason="unknown_handle"),
            ),
        ),
        "V07-04-inprocess-restart-before-resolution-boundary": GB03SlotFixture(
            selector="V07-04-inprocess-restart-before-resolution-boundary",
            family_id="V07",
            lane="in_process",
            workspace_files={"internal/a.go": a_src},
            delivery_log=DeliveryLog(records=(r1,)),
            scope=scope,
            scripted_turns=(),
            expectations=(
                CitationExpectation(Citation("handle", '[read#1:2 "return 7"]', handle="read#1", start_line=2, excerpt="return 7", citation_id="v07-04-r1"), "grounded", "matched", expected_supporting_handles=("read#1",)),
            ),
        ),
        "V07-05-inprocess-unreadable-ledger-refuses-all-citations": GB03SlotFixture(
            selector="V07-05-inprocess-unreadable-ledger-refuses-all-citations",
            family_id="V07",
            lane="in_process",
            workspace_files={"internal/a.go": a_src},
            delivery_log=unreadable_log,
            scope=scope,
            scripted_turns=(),
            expectations=(
                CitationExpectation(Citation("handle", "[read#1:2]", handle="read#1", start_line=2, citation_id="v07-05-h"), "ungrounded", "unverifiable", expected_reason="unreadable_ledger"),
                CitationExpectation(Citation("path_coord", "[internal/a.go:2]", path="internal/a.go", start_line=2, citation_id="v07-05-p"), "ungrounded", "unverifiable", expected_reason="unreadable_ledger"),
                CitationExpectation(Citation("handle_path", "[read#1 internal/a.go:2]", handle="read#1", path="internal/a.go", start_line=2, citation_id="v07-05-hp"), "ungrounded", "unverifiable", expected_reason="unreadable_ledger"),
                CitationExpectation(Citation("url", "https://example.test/doc", url="https://example.test/doc", citation_id="v07-05-u"), "ungrounded", "unverifiable", expected_reason="unreadable_ledger"),
                CitationExpectation(Citation("artifact", "[artifact:11111111-1111-4111-8111-111111111111]", artifact_id="11111111-1111-4111-8111-111111111111", citation_id="v07-05-a"), "ungrounded", "unverifiable", expected_reason="unreadable_ledger"),
            ),
            expected_has_citable_observation=None,
            expected_union_handles=(),
        ),
        "V07-06-inprocess-unreadable-ledger-denies-exemption": GB03SlotFixture(
            selector="V07-06-inprocess-unreadable-ledger-denies-exemption",
            family_id="V07",
            lane="in_process",
            workspace_files={"internal/a.go": a_src},
            delivery_log=unreadable_log,
            scope=scope,
            scripted_turns=(),
            expectations=(
                CitationExpectation(Citation("handle", "[read#1]", handle="read#1", citation_id="v07-06-c1"), "ungrounded", "unverifiable", expected_reason="unreadable_ledger"),
            ),
            expected_has_citable_observation=None,
            expected_union_handles=(),
        ),
    }


def build_all_gb03_fixtures() -> dict[str, GB03SlotFixture]:
    """Build the complete dictionary of all 53 GB03 fixed slot fixtures."""
    all_fixtures: dict[str, GB03SlotFixture] = {}
    for builder in (
        _build_v01_fixtures,
        _build_v03_fixtures,
        _build_v04_fixtures,
        _build_v05_fixtures,
        _build_v06_fixtures,
        _build_v07_fixtures,
    ):
        part = builder()
        overlap = set(all_fixtures) & set(part)
        if overlap:
            raise ValueError(f"Duplicate fixture selectors: {sorted(overlap)}")
        all_fixtures.update(part)

    expected_keys = {s.selector for s in GB03_FIXED_SLOTS}
    if set(all_fixtures) != expected_keys:
        missing = sorted(expected_keys - set(all_fixtures))
        extra = sorted(set(all_fixtures) - expected_keys)
        raise ValueError(f"GB03 fixture mismatch: missing={missing}, extra={extra}")
    return all_fixtures


def get_gb03_fixture(selector: str) -> GB03SlotFixture:
    """Return the deterministic fixture for a single GB03 selector."""
    if selector not in SLOTS_BY_SELECTOR:
        raise KeyError(f"Unknown GB03 selector: {selector!r}")
    return build_all_gb03_fixtures()[selector]


def iter_v02_sample(
    *,
    base_seeds: tuple[str, ...] = BASE_SEEDS,
    count_per_seed: int = 10,
) -> list[V02LookalikeFixture]:
    """Generate a deterministic sample of V02 lookalike fixtures across `base_seeds`."""
    out: list[V02LookalikeFixture] = []
    for seed in base_seeds:
        for idx in range(count_per_seed):
            out.append(build_v02_lookalike_fixture(seed, idx))
    return out
