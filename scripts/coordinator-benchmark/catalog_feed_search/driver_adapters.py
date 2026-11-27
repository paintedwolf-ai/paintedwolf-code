"""Transport-agnostic mappers from exact D1 /v1 OpenAPI JSON & SSE payloads to checker types."""

from __future__ import annotations

from dataclasses import dataclass

from .checker import (
    CatalogItem,
    ProductCatalogAnswer,
    ProductSearchAnswer,
    ScopedSearchHit,
)
from .checker_feed import FeedEvent, ProductFeedAnswer
from .checker_replace import ProductReplaceAnswer, ReplaceFileObservation
from .checker_types import ALL_D4_GAPS, DeclaredGap
from .references import SearchHit

_EXECUTOR_TO_LEG: dict[str, str | None] = {
    "code": "code",
    "code_search": "code",
    "symbol": "symbol",
    "symbols": "symbol",
    "files": "files",
    "file_lookup": "files",
    "store": "store",
    "messages": "store",
}

_HIT_KIND_TO_LEG: dict[str, str] = {
    "code": "code",
    "source_code": "code",
    "symbol": "symbol",
    "source_symbol": "symbol",
    "file": "files",
    "source_file": "files",
    "message": "store",
    "session": "store",
    "finding": "store",
    "evidence": "store",
}


@dataclass(frozen=True)
class AgentDiscoveryObservation:
    """Captured model-facing result from command/summarize/survey_repo agent routes."""

    tool_name: str
    ok: bool
    rejection_code: str | None
    generation_id: str | None
    fresh: bool | None
    declared_gaps: tuple[str, ...]
    serialized_result: dict[str, object]


def map_source_index_coverage(coverage_list: list[dict[str, object]]) -> tuple[DeclaredGap, ...]:
    """Map exact D1 SourceIndexRootCoverage entries to DeclaredGaps.

    Real D1 fields: root_id, state (warming|ready|failed), discovery_complete,
    refreshing, bounded_directories, failed_directories, error.
    A readable partial root (state="ready" with discovery_complete=False,
    bounded_directories > 0, failed_directories > 0, or non-empty error)
    NEVER appears complete.
    """
    gaps: list[DeclaredGap] = []
    for item in coverage_list:
        root_id = str(item["root_id"]) if item.get("root_id") else None
        state = str(item.get("state") or "").lower()
        if state == "warming":
            gaps.append(DeclaredGap(reason="catalog_warming", root_id=root_id))
        elif state == "failed":
            gaps.append(DeclaredGap(reason="catalog_failed", root_id=root_id))

        if item.get("discovery_complete") is False and state != "warming":
            gaps.append(DeclaredGap(reason="catalog_incomplete", root_id=root_id))
        if item.get("refreshing") is True:
            gaps.append(DeclaredGap(reason="catalog_refreshing", root_id=root_id))
        if int(item.get("bounded_directories") or 0) > 0:
            gaps.append(DeclaredGap(reason="catalog_bounded", root_id=root_id))
        if int(item.get("failed_directories") or 0) > 0:
            gaps.append(DeclaredGap(reason="catalog_failed", root_id=root_id))
        if str(item.get("error") or "").strip():
            err_reason = "catalog_refresh_failed" if state == "ready" else "catalog_failed"
            gaps.append(DeclaredGap(reason=err_reason, root_id=root_id))
    return tuple(gaps)


def map_search_issues(
    issues: list[dict[str, object]],
    *,
    truncated: bool = False,
    next_cursor: str | None = None,
    continuation_required: bool = False,
) -> tuple[DeclaredGap, ...]:
    """Map OpenAPI SearchIssue entries to scoped DeclaredGap records."""
    gaps: list[DeclaredGap] = []
    for issue in issues:
        reason = str(issue.get("reason") or "")
        if reason not in ALL_D4_GAPS:
            continue
        executor = str(issue.get("executor") or "").lower()
        leg = _EXECUTOR_TO_LEG.get(executor)
        root_id = str(issue["root_id"]) if issue.get("root_id") else None
        raw_paths = issue.get("paths")
        paths = tuple(str(p) for p in raw_paths) if isinstance(raw_paths, list) else ()
        count_val = issue.get("count")
        skipped_count = int(count_val) if count_val is not None else None
        gaps.append(
            DeclaredGap(
                reason=reason,
                root_id=root_id,
                leg=leg,
                paths=paths,
                truncated=truncated if reason == "result_limit" else None,
                continuation=next_cursor if reason == "result_limit" else None,
                continuation_required=continuation_required if reason == "result_limit" else False,
                skipped_count=skipped_count if reason == "files_skipped" else None,
            )
        )
    return tuple(gaps)


def map_browse_entries_to_answer(
    entries_by_root: dict[str, list[dict[str, object]]],
    coverage_list: list[dict[str, object]] | None = None,
    *,
    generation_revision: int | None = None,
    instrumentation_complete: bool = True,
) -> ProductCatalogAnswer:
    """Map D1 /source/browse entries (`name`, `is_dir`) + coverage to ProductCatalogAnswer(projection='browse')."""
    items: list[CatalogItem] = []
    for root_id, entries in sorted(entries_by_root.items()):
        for entry in entries:
            path = str(entry.get("path") or entry.get("name") or "").removeprefix("./")
            kind = "dir" if bool(entry.get("is_dir")) else "file"
            items.append(CatalogItem(path=path, kind=kind, root_id=root_id))
    return ProductCatalogAnswer(
        items=tuple(items),
        declared_gaps=map_source_index_coverage(coverage_list or []),
        instrumentation_complete=instrumentation_complete,
        projection="browse",
        generation_revision=generation_revision,
    )


def map_catalog_index_matches_to_answer(
    matches: list[dict[str, object]],
    coverage_list: list[dict[str, object]] | None = None,
    *,
    generation_revision: int | None = None,
    instrumentation_complete: bool = True,
) -> ProductCatalogAnswer:
    """Map D1 catalog-backed `/source/search` file matches to ProductCatalogAnswer(projection='catalog')."""
    items: list[CatalogItem] = []
    for m in matches:
        root_id = str(m.get("root_id") or "root-0")
        path = str(m.get("path") or "").removeprefix("./")
        if path:
            items.append(CatalogItem(path=path, kind="file", root_id=root_id))
    return ProductCatalogAnswer(
        items=tuple(items),
        declared_gaps=map_source_index_coverage(coverage_list or []),
        instrumentation_complete=instrumentation_complete,
        projection="catalog",
        generation_revision=generation_revision,
    )


def map_sse_source_changed_to_answer(
    frames: list[dict[str, object]],
    *,
    project_id: str | None = None,
    workspace_id: str | None = None,
    default_root_id: str = "root-0",
    reconciled_walk: tuple[CatalogItem, ...] | None = None,
    reference_post_walk: tuple[CatalogItem, ...] | None = None,
    claims_operation: bool = True,
    claims_ordering: bool = False,
    claims_batch_effect: bool = False,
    instrumentation_complete: bool = True,
) -> ProductFeedAnswer:
    """Map D1 /v1/events EventEnvelopeSourceChanged frames (`SourceChangesEvent` / `SourceChange`)."""
    events: list[FeedEvent] = []
    gaps: list[DeclaredGap] = []

    for frame_idx, frame in enumerate(frames):
        if frame.get("topic") != "source_changed":
            continue
        data = frame.get("data")
        if not isinstance(data, dict):
            continue
        if project_id and str(data.get("project_id") or "") != project_id:
            continue
        ev_ws = str(data.get("workspace_id") or "")
        if workspace_id and ev_ws and ev_ws != workspace_id:
            continue
        env_id = str(frame.get("id") or f"sse-{frame_idx}")
        batch_id = f"{ev_ws}:{env_id}" if ev_ws else env_id
        if data.get("resync") is True:
            gaps.append(DeclaredGap(reason="resync", root_id=default_root_id))
        raw_changes = data.get("changes") or []
        if isinstance(raw_changes, list):
            for ch in raw_changes:
                if not isinstance(ch, dict):
                    continue
                path = str(ch.get("path") or "").removeprefix("./")
                if not path:
                    continue
                op = str(ch.get("op") or "write").lower()
                root_id = str(ch.get("root_id") or default_root_id)
                events.append(
                    FeedEvent(
                        path=path,
                        batch_id=batch_id,
                        op=op,
                        root_id=root_id,
                    )
                )
                from_path = str(ch.get("from_path") or "").removeprefix("./")
                if from_path:
                    events.append(
                        FeedEvent(
                            path=from_path,
                            batch_id=batch_id,
                            op=op,
                            root_id=root_id,
                        )
                    )

    return ProductFeedAnswer(
        events=tuple(events),
        declared_gaps=tuple(gaps),
        reconciled_walk=reconciled_walk,
        reference_post_walk=reference_post_walk,
        instrumentation_complete=instrumentation_complete,
        claims_operation=claims_operation,
        claims_ordering=claims_ordering,
        claims_batch_effect=claims_batch_effect,
    )


def map_search_response_to_answer(
    response: dict[str, object],
    *,
    default_root_id: str = "root-0",
    default_leg: str = "code",
    continuation_required: bool = False,
    instrumentation_complete: bool = True,
) -> ProductSearchAnswer:
    """Map D1 POST /v1/search SearchResponse JSON to ProductSearchAnswer.

    D1 SearchHit carries `path`, `line`, `hit_id`, `hit_kind`, `root_id`, `title`,
    and `snippet`, NOT `byte_offset`/`byte_end`/`match_text`. We never invent 0/snippet
    offsets: hits without explicit spans are marked `span_available=False` and the
    answer uses `projection="path_line"`.
    """
    hits: list[ScopedSearchHit] = []
    any_missing_span = False
    raw_hits = response.get("hits") or []
    if isinstance(raw_hits, list):
        for h in raw_hits:
            if not isinstance(h, dict):
                continue
            path = str(h.get("path") or "").removeprefix("./")
            line = int(h.get("line") or 1)
            has_span = ("byte_offset" in h and "byte_end" in h and "match_text" in h)
            if not has_span:
                any_missing_span = True
            col = int(h.get("column") or 1)
            byte_offset = int(h["byte_offset"]) if has_span else -1
            byte_end = int(h["byte_end"]) if has_span else -1
            match_text = str(h["match_text"]) if has_span else ""
            line_text = str(h.get("snippet") or "")
            root_id = str(h.get("root_id") or default_root_id)
            raw_kind = str(h.get("hit_kind") or "").lower()
            leg = _HIT_KIND_TO_LEG.get(raw_kind, default_leg)
            hits.append(
                ScopedSearchHit(
                    hit=SearchHit(
                        path=path,
                        line_number=line,
                        column=col,
                        byte_offset=byte_offset,
                        byte_end=byte_end,
                        match_text=match_text,
                        line_text=line_text,
                    ),
                    root_id=root_id,
                    leg=leg,
                    span_available=has_span,
                )
            )

    status = str(response.get("status") or "complete").lower()
    next_cursor = str(response["next_cursor"]) if response.get("next_cursor") else None
    raw_issues = response.get("issues") or []
    gaps = map_search_issues(
        raw_issues if isinstance(raw_issues, list) else [],
        truncated=(status == "limited"),
        next_cursor=next_cursor,
        continuation_required=continuation_required,
    )
    return ProductSearchAnswer(
        hits=tuple(hits),
        declared_gaps=gaps,
        instrumentation_complete=instrumentation_complete,
        projection="path_line" if (any_missing_span or not hits) else "exact_span",
    )


def map_replace_apply_to_answer(
    apply_response: dict[str, object],
    preview_response: dict[str, object],
    *,
    disk_hashes_by_key: dict[tuple[str, str], str],
    drifted_keys: set[tuple[str, str]] | None = None,
    drifted_hashes: dict[tuple[str, str], str] | None = None,
    instrumentation_complete: bool = True,
) -> ProductReplaceAnswer:
    """Map /v1/search/replace/apply + /preview + independent post-op disk hashes to ProductReplaceAnswer."""
    drifted = drifted_keys or set()
    drifted_sha = drifted_hashes or {}
    preview_sha: dict[tuple[str, str], str] = {}
    for pf in preview_response.get("files") or []:
        if isinstance(pf, dict):
            preview_sha[(str(pf.get("root_id") or "root-0"), str(pf.get("path") or ""))] = str(
                pf.get("sha256") or ""
            )

    observations: list[ReplaceFileObservation] = []
    for item in apply_response.get("files") or []:
        if not isinstance(item, dict):
            continue
        root_id = str(item.get("root_id") or "root-0")
        path = str(item.get("path") or "").removeprefix("./")
        key = (root_id, path)
        applied = bool(item.get("applied"))
        skipped = bool(item.get("skipped"))
        reason = str(item.get("reason") or "").lower()
        is_drifted = key in drifted
        refused_drift = (not applied) and skipped and (
            "drift" in reason or "sha" in reason or "changed" in reason or "conflict" in reason
        )
        observations.append(
            ReplaceFileObservation(
                path=path,
                root_id=root_id,
                applied=applied,
                refused_drift=refused_drift,
                observed_sha256=disk_hashes_by_key.get(key, ""),
                expected_before_sha256=preview_sha.get(key, ""),
                drifted_before_apply=is_drifted,
                drifted_sha256=drifted_sha.get(key),
            )
        )

    gaps: list[DeclaredGap] = list(
        map_search_issues(
            preview_response.get("issues") if isinstance(preview_response.get("issues"), list) else [],  # type: ignore[arg-type]
        )
    )
    if str(preview_response.get("state") or "").lower() == "limited":
        gaps.append(DeclaredGap(reason="limited_preview"))

    return ProductReplaceAnswer(
        files=tuple(observations),
        declared_gaps=tuple(gaps),
        instrumentation_complete=instrumentation_complete,
    )


def map_agent_tool_result(tool_name: str, raw_result: dict[str, object]) -> AgentDiscoveryObservation:
    """Capture model-facing tool result, generation/coverage facts, and rejection code."""
    ok = bool(raw_result.get("ok", True)) and not bool(raw_result.get("error"))
    rejection_code = None
    for field_name in ("rejection_code", "code", "error_code"):
        if raw_result.get(field_name):
            rejection_code = str(raw_result[field_name])
            break
    gen_id = str(raw_result["generation_id"]) if raw_result.get("generation_id") else None
    fresh_val = raw_result.get("fresh")
    fresh = bool(fresh_val) if fresh_val is not None else None
    gaps: list[str] = []
    if fresh is False:
        gaps.append("stale_inventory")
    for issue in raw_result.get("issues") or []:
        if isinstance(issue, dict) and issue.get("reason"):
            gaps.append(str(issue["reason"]))
    return AgentDiscoveryObservation(
        tool_name=tool_name,
        ok=ok,
        rejection_code=rejection_code,
        generation_id=gen_id,
        fresh=fresh,
        declared_gaps=tuple(dict.fromkeys(gaps)),
        serialized_result=dict(raw_result),
    )
