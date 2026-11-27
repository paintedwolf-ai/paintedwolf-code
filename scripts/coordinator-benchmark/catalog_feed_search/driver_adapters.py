"""Transport-agnostic mappers from D1 /v1 JSON & SSE payloads to checker domain types."""

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
    """Map SourceIndexRootCoverage entries from /source/index or /source/search to DeclaredGaps."""
    gaps: list[DeclaredGap] = []
    for item in coverage_list:
        root_id = str(item["root_id"]) if item.get("root_id") else None
        state = str(item.get("state") or item.get("status") or "").lower()
        reason_map = {
            "warming": "catalog_warming",
            "preparing": "catalog_warming",
            "incomplete": "catalog_incomplete",
            "partial": "catalog_incomplete",
            "bounded": "catalog_bounded",
            "refreshing": "catalog_refreshing",
            "failed": "catalog_failed",
            "refresh_failed": "catalog_refresh_failed",
            "unavailable": "catalog_failed",
        }
        mapped = reason_map.get(state)
        if mapped:
            gaps.append(DeclaredGap(reason=mapped, root_id=root_id))
        if item.get("refreshing") is True and mapped != "catalog_refreshing":
            gaps.append(DeclaredGap(reason="catalog_refreshing", root_id=root_id))
        if int(item.get("unreadable_dirs") or 0) > 0:
            gaps.append(DeclaredGap(reason="catalog_failed", root_id=root_id))
    return tuple(gaps)


def map_search_issues(
    issues: list[dict[str, object]],
    *,
    truncated: bool = False,
    next_cursor: str | None = None,
    continuation_required: bool = False,
) -> tuple[DeclaredGap, ...]:
    """Map OpenAPI SearchIssue entries (router.go) to scoped DeclaredGap records."""
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


def map_catalog_browse_to_answer(
    entries_by_root: dict[str, list[dict[str, object]]],
    coverage_list: list[dict[str, object]] | None = None,
    *,
    instrumentation_complete: bool = True,
) -> ProductCatalogAnswer:
    """Map browsed catalog entries + /source/index coverage to ProductCatalogAnswer."""
    items: list[CatalogItem] = []
    for root_id, entries in sorted(entries_by_root.items()):
        for entry in entries:
            path = str(entry.get("path") or entry.get("name") or "").removeprefix("./")
            raw_kind = str(entry.get("kind") or entry.get("type") or "file").lower()
            kind = "dir" if raw_kind in ("dir", "directory", "folder") else (
                "symlink" if raw_kind in ("symlink", "link") else "file"
            )
            sha = str(entry["sha256"]) if entry.get("sha256") else None
            items.append(CatalogItem(path=path, kind=kind, root_id=root_id, sha256=sha))
    gaps = map_source_index_coverage(coverage_list or [])
    return ProductCatalogAnswer(
        items=tuple(items),
        declared_gaps=gaps,
        instrumentation_complete=instrumentation_complete,
    )


def map_sse_source_changed_to_answer(
    frames: list[dict[str, object]],
    *,
    project_id: str | None = None,
    default_root_id: str = "root-0",
    reconciled_walk: tuple[CatalogItem, ...] | None = None,
    reference_post_walk: tuple[CatalogItem, ...] | None = None,
    claims_operation: bool = True,
    claims_ordering: bool = False,
    claims_batch_effect: bool = False,
    instrumentation_complete: bool = True,
) -> ProductFeedAnswer:
    """Map /v1/events EventEnvelopeSourceChanged frames to ProductFeedAnswer."""
    events: list[FeedEvent] = []
    gaps: list[DeclaredGap] = []

    for frame_idx, frame in enumerate(frames):
        if frame.get("topic") != "source_changed":
            continue
        data = frame.get("data")
        if not isinstance(data, dict):
            continue
        if project_id and data.get("project_id") and str(data["project_id"]) != project_id:
            continue
        batch_id = str(frame.get("id") or data.get("batch_id") or f"sse-{frame_idx}")
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
                op = str(ch.get("kind") or ch.get("op") or ch.get("action") or "change").lower()
                root_id = str(ch.get("root_id") or default_root_id)
                effect_id = str(ch["effect_id"]) if ch.get("effect_id") else None
                seq_val = int(ch["seq"]) if ch.get("seq") is not None else None
                events.append(
                    FeedEvent(
                        path=path,
                        batch_id=batch_id,
                        op=op,
                        effect_id=effect_id,
                        root_id=root_id,
                        seq=seq_val,
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
    """Map POST /v1/search SearchResponse JSON to ProductSearchAnswer."""
    hits: list[ScopedSearchHit] = []
    raw_hits = response.get("hits") or []
    if isinstance(raw_hits, list):
        for h in raw_hits:
            if not isinstance(h, dict):
                continue
            path = str(h.get("path") or "").removeprefix("./")
            line = int(h.get("line") or 1)
            col = int(h.get("column") or 1)
            byte_offset = int(h.get("byte_offset") or 0)
            byte_end = int(h.get("byte_end") or byte_offset)
            match_text = str(h.get("match_text") or h.get("snippet") or h.get("title") or "")
            line_text = str(h.get("line_text") or h.get("snippet") or "")
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
        refused_drift = (not applied) and skipped and ("drift" in reason or "sha" in reason or "changed" in reason or "conflict" in reason)
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
