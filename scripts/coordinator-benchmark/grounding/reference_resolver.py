"""Pure-Python independent reference resolver for Painted Wolf grounding.

Reads ONLY the campaign's independent DeliveryLog and PermittedScope.
Zero imports or calls to any Go/product grounding packages
(lycaon/internal/evidence, lycaon/internal/citationmd, lycaon/internal/grounding).
"""
from __future__ import annotations

from dataclasses import dataclass
import posixpath
import re
from typing import Iterable

from .delivery_log import DeliveryLog, DeliveryRecord, LineRange


CITATION_TYPES: frozenset[str] = frozenset(
    {"handle", "path_coord", "handle_path", "url", "artifact"}
)

# Mediated contact records (GB01 Finding F2): never grant URL citation support on their own.
MEDIATED_CONTACT_KINDS: frozenset[str] = frozenset(
    {"dialed_host", "retrieved_host", "audited_host"}
)

# Admitted URL-bearing observation kinds.
ADMITTED_URL_KINDS: frozenset[str] = frozenset(
    {"web", "fetch", "browser", "http", "web_fetch", "web_page"}
)

# Brief-only kinds (Decision 002 S06): prove only the brief row, create no citation duty,
# supply no line/excerpt, and are unavailable to recall.
SOURCE_CHANGE_KIND: str = "source_change"

# Opaque command kind without numbered file delivery does not index printed paths.
OPAQUE_COMMAND_KINDS: frozenset[str] = frozenset({"command"})

_HANDLE_RE = re.compile(r"^(?:(?P<leg>[A-Za-z0-9._-]+):)?(?P<kind>[a-z][a-z0-9_]*)#(?P<ord>[1-9]\d*)$")
_NUMBERED_PREFIX_RE = re.compile(r"^\s*\d+(?:\s*[:|]\s?|\t)")


@dataclass(frozen=True)
class Citation:
    """Structured representation of a citation across all 5 contract types."""

    citation_type: str
    raw: str = ""
    handle: str = ""
    worker_leg: str = ""
    path: str = ""
    start_line: int | None = None
    end_line: int | None = None
    excerpt: str = ""
    url: str = ""
    artifact_id: str = ""
    citation_id: str = ""

    def __post_init__(self) -> None:
        if self.citation_type not in CITATION_TYPES:
            raise ValueError(
                f"Unknown citation_type {self.citation_type!r}; expected one of {sorted(CITATION_TYPES)}"
            )
        if self.start_line is not None and self.end_line is None:
            object.__setattr__(self, "end_line", self.start_line)
        if self.handle and ":" in self.handle and not self.worker_leg:
            leg, bare = self.handle.split(":", 1)
            object.__setattr__(self, "worker_leg", leg)
            object.__setattr__(self, "handle", bare)

    @property
    def names_handle(self) -> bool:
        return bool(self.handle)

    @property
    def has_coordinate(self) -> bool:
        return self.start_line is not None


@dataclass(frozen=True)
class PermittedScope:
    """Execution and provenance boundary for resolving a citation."""

    session_id: str
    worker_leg: str = ""
    permitted_legs: frozenset[str] = frozenset()
    round_id: str = ""
    intent_window: str = ""
    permitted_artifacts: frozenset[str] = frozenset()
    platform: str = "linux"

    def __post_init__(self) -> None:
        if not isinstance(self.permitted_legs, frozenset):
            object.__setattr__(self, "permitted_legs", frozenset(self.permitted_legs))
        if not isinstance(self.permitted_artifacts, frozenset):
            object.__setattr__(self, "permitted_artifacts", frozenset(self.permitted_artifacts))
        if self.platform not in {"linux", "macos"}:
            raise ValueError(f"Unsupported platform {self.platform!r}")

    @property
    def case_insensitive_paths(self) -> bool:
        return self.platform == "macos"


@dataclass(frozen=True)
class ReferenceVerdict:
    """Independent reference resolver decision for one citation."""

    verdict_class: str  # "grounded" | "ambiguous" | "ungrounded"
    expected_exact_verdict: str  # "matched" | "traced" | "bound" | "ambiguous" | "unverifiable"
    supporting_handles: tuple[str, ...] = ()
    reason: str = ""
    reanchored_line: int | None = None
    # Decision 004 F3 & Decision 005: keeps product behavior, observed product rule,
    # and contract reading distinct for mismatched printed numbering and copied tags.
    contract_reading_verdict: str | None = None
    observed_product_rule: str = ""
    disputed_expectation: bool = False


def normalize_path(path: str, *, case_insensitive: bool = False) -> str:
    """Normalize a workspace-relative path for cross-platform comparison."""
    cleaned = path.strip().strip('"').strip("'")
    cleaned = cleaned.replace("\\", "/")
    if not cleaned:
        return ""
    norm = posixpath.normpath(cleaned)
    if norm == ".":
        return ""
    while norm.startswith("./"):
        norm = norm[2:]
    if norm.startswith("/"):
        norm = norm.lstrip("/")
    if case_insensitive:
        norm = norm.lower()
    return norm


def parse_handle(handle: str) -> tuple[str, str, int] | None:
    """Parse `[leg:]kind#ordinal` into (leg, kind, ordinal) or None."""
    match = _HANDLE_RE.match(handle.strip())
    if not match:
        return None
    return (match.group("leg") or "", match.group("kind"), int(match.group("ord")))


def _normalize_newlines(text: str) -> str:
    return text.replace("\r\n", "\n").replace("\r", "\n")


def _strip_number_prefix(line: str) -> str:
    return _NUMBERED_PREFIX_RE.sub("", line, count=1)


def _delivered_line_map(record: DeliveryRecord) -> list[tuple[int, str, str]]:
    """Return ordered list of (true_line_number, raw_line, content_line) for a record."""
    text = _normalize_newlines(record.raw_text)
    raw_lines = text.split("\n")
    # Drop single trailing empty split element produced by a final newline
    if raw_lines and raw_lines[-1] == "" and text.endswith("\n"):
        raw_lines = raw_lines[:-1]

    expected_numbers: list[int] = []
    for rng in record.delivered_ranges:
        expected_numbers.extend(range(rng.start, rng.end + 1))

    if not expected_numbers:
        expected_numbers = list(range(1, len(raw_lines) + 1))

    result: list[tuple[int, str, str]] = []
    for line_no, raw_line in zip(expected_numbers, raw_lines):
        if record.line_numbered or record.printed_other_numbers:
            content = _strip_number_prefix(raw_line)
        else:
            content = raw_line
        result.append((line_no, raw_line, content))
    return result


def _range_covered(record: DeliveryRecord, start: int, end: int) -> bool:
    """True if every line in [start, end] is covered by one contiguous delivered range."""
    return any(rng.contains_range(start, end) for rng in record.delivered_ranges)


def _find_contiguous_excerpt_matches(
    record: DeliveryRecord, excerpt: str
) -> list[int]:
    """Find all starting true line numbers where `excerpt` matches a contiguous span.

    Multiline excerpts must match one contiguous span within this single observation,
    preserving blank lines and clipping (R03). Mixed CRLF/LF line endings are
    normalized (R06).
    """
    norm_excerpt = _normalize_newlines(excerpt)
    if not norm_excerpt:
        return []

    exc_lines = norm_excerpt.split("\n")
    if len(exc_lines) > 1 and exc_lines[-1] == "" and norm_excerpt.endswith("\n"):
        exc_lines = exc_lines[:-1]

    line_entries = _delivered_line_map(record)
    if not line_entries or len(exc_lines) > len(line_entries):
        return []

    matches: list[int] = []
    span_len = len(exc_lines)

    for idx in range(len(line_entries) - span_len + 1):
        window = line_entries[idx : idx + span_len]
        # Verify true line numbers are contiguous inside the observation
        contiguous = all(
            window[i + 1][0] == window[i][0] + 1 for i in range(span_len - 1)
        )
        if not contiguous:
            continue

        if span_len == 1:
            needle = exc_lines[0]
            needle_stripped = needle.strip()
            raw_l, content_l = window[0][1], window[0][2]
            if (
                needle in content_l
                or needle in raw_l
                or (needle_stripped and needle_stripped in content_l)
            ):
                matches.append(window[0][0])
        else:
            ok = True
            for offset, exc_line in enumerate(exc_lines):
                raw_l, content_l = window[offset][1], window[offset][2]
                if exc_line == "":
                    # Blank lines must be preserved strictly (R03)
                    if content_l.strip() != "":
                        ok = False
                        break
                elif offset == 0:
                    # First line may be a suffix/substring (clipping preserved)
                    if exc_line not in content_l and exc_line.strip() not in content_l and exc_line not in raw_l:
                        ok = False
                        break
                elif offset == span_len - 1:
                    # Last line may be a prefix/substring (clipping preserved)
                    if exc_line not in content_l and exc_line.strip() not in content_l and exc_line not in raw_l:
                        ok = False
                        break
                else:
                    # Interior lines must match the full line content (allowing outer whitespace tolerance)
                    if exc_line != content_l and exc_line.strip() != content_l.strip() and exc_line != raw_l:
                        ok = False
                        break
            if ok:
                matches.append(window[0][0])

    return matches


def _evaluate_coordinate_and_excerpt(
    record: DeliveryRecord,
    start_line: int | None,
    end_line: int | None,
    excerpt: str,
    *,
    raw_citation: str = "",
) -> tuple[bool, str, str, int | None, str | None, str, bool]:
    """Evaluate line/range and excerpt against one observation.

    Returns:
        (supported, exact_verdict, reason, reanchored_line,
         contract_reading, observed_product_rule, disputed)
    """
    if record.kind == SOURCE_CHANGE_KIND:
        if start_line is not None or excerpt:
            return (False, "unverifiable", "source_change_brief_only", None, None, "", False)
        return (True, "matched", "source_change_brief_row", None, None, "", False)

    if start_line is None and not excerpt:
        return (True, "matched", "exact_match", None, None, "", False)

    effective_end = end_line if end_line is not None else start_line

    # Case 1: Excerpt supplied (with or without line coordinate)
    if excerpt:
        matches = _find_contiguous_excerpt_matches(record, excerpt)
        if not matches:
            return (False, "unverifiable", "excerpt_mismatch", None, None, "", False)
        if start_line is None:
            return (True, "matched", "verified_excerpt", None, None, "", False)
        # Line coordinate AND excerpt both supplied
        assert effective_end is not None
        if _range_covered(record, start_line, effective_end) and start_line in matches:
            return (True, "matched", "exact_match", None, None, "", False)
        # Re-anchor rule (Decision 002 R03/R06/R07): wrong/uncovered line re-anchors
        # ONLY when the excerpt matches uniquely (1 match) within the permitted observation.
        if len(matches) == 1:
            return (True, "matched", "reanchored_excerpt", matches[0], None, "", False)
        return (False, "unverifiable", "excerpt_ambiguous", None, None, "", False)

    # Case 2: Coordinate only, no excerpt
    assert start_line is not None and effective_end is not None
    if not _range_covered(record, start_line, effective_end):
        return (False, "unverifiable", "uncovered_range", None, None, "", False)

    # Decision 004 F3 & Decision 005: printed_other_numbers without an excerpt.
    if record.printed_other_numbers:
        is_copied_host_tag = bool(raw_citation.strip()) and (
            raw_citation.strip() in record.delivery_tags
        )
        if is_copied_host_tag:
            # Decision 005: copied host-issued delivery tag formats the true coverage
            # and is a contract-valid citation; keep observed_product_rule identifiable
            # so sidecar execution decides whether runtime agrees or flags a candidate false reject.
            return (
                True,
                "matched",
                "copied_host_delivery_tag",
                None,
                "matched",
                "printed_other_numbers_rejects_unexcerpted_coord",
                True,
            )
        # Un-excerpted counted coordinate: D1 product rule rejects (unverifiable),
        # while contract phrasing for traced coordinates is kept distinct.
        return (
            False,
            "unverifiable",
            "mismatched_printed_numbers_no_excerpt",
            None,
            "traced",
            "printed_other_numbers_rejects_unexcerpted_coord",
            True,
        )

    if record.line_numbered:
        return (True, "matched", "numbered_coordinate", None, None, "", False)
    return (True, "traced", "traced_range", None, None, "", False)


def _is_in_leg_scope(record: DeliveryRecord, target_leg: str, scope: PermittedScope) -> bool:
    if record.session_id != scope.session_id:
        return False
    if not record.model_visible:
        return False
    if target_leg:
        if target_leg != scope.worker_leg and target_leg not in scope.permitted_legs:
            return False
        return record.worker_leg == target_leg
    return record.worker_leg == scope.worker_leg


def _is_file_region_record(record: DeliveryRecord) -> bool:
    if not record.path:
        return False
    if record.kind in MEDIATED_CONTACT_KINDS or record.kind == SOURCE_CHANGE_KIND:
        return False
    if record.kind in OPAQUE_COMMAND_KINDS and not record.delivered_ranges:
        return False
    return bool(record.delivered_ranges)


def _scoped_current_file_records(
    log: DeliveryLog,
    target_leg: str,
    scope: PermittedScope,
    *,
    kind: str | None = None,
) -> list[DeliveryRecord]:
    out: list[DeliveryRecord] = []
    for rec in log.records:
        if not _is_in_leg_scope(rec, target_leg, scope):
            continue
        if rec.is_superseded:
            continue
        if scope.round_id and rec.round_id and rec.round_id != scope.round_id:
            continue
        if kind is not None and rec.kind != kind:
            continue
        if not _is_file_region_record(rec):
            continue
        out.append(rec)
    return out


def has_citable_observation(log: DeliveryLog, scope: PermittedScope) -> bool | None:
    """Return whether the current intent window has any citable observation.

    Returns None when the ledger is unreadable (V07 / S01: never treated as empty!).
    `source_change#` brief rows create NO citation duty (Decision 002 S06).
    Mediated contact records (`dialed_host#`, `retrieved_host#`, `audited_host#`)
    create NO citable URL observation on their own (Decision 004 F2).
    """
    if not log.readable:
        return None
    for rec in log.records:
        if rec.session_id != scope.session_id:
            continue
        if not rec.model_visible:
            continue
        if scope.intent_window and rec.intent_window != scope.intent_window:
            continue
        if rec.worker_leg and rec.worker_leg != scope.worker_leg and rec.worker_leg not in scope.permitted_legs:
            continue
        if rec.kind == SOURCE_CHANGE_KIND or rec.kind in MEDIATED_CONTACT_KINDS:
            continue
        return True
    return False


def is_recallable(record: DeliveryRecord) -> bool:
    """`source_change#` brief handles are not served by `recall` (Decision 002 S06)."""
    return record.kind != SOURCE_CHANGE_KIND and record.kind not in MEDIATED_CONTACT_KINDS


def _handle_exists_outside_scope(log: DeliveryLog, handle: str, target_leg: str, scope: PermittedScope) -> str | None:
    """Detect whether a handle exists only in a forbidden leg or session."""
    for rec in log.records:
        if rec.handle != handle:
            continue
        if rec.session_id != scope.session_id:
            return "wrong_session"
        if target_leg:
            if target_leg != scope.worker_leg and target_leg not in scope.permitted_legs:
                return "wrong_leg"
            if rec.worker_leg != target_leg:
                return "wrong_leg"
        else:
            if rec.worker_leg != scope.worker_leg:
                return "wrong_leg"
    return None


def _attempt_stale_ordinal_binding(
    log: DeliveryLog,
    citation: Citation,
    target_leg: str,
    scope: PermittedScope,
    parsed_kind: str,
) -> ReferenceVerdict | None:
    """Bind a stale ordinal ONLY when structured fields uniquely identify a current record.

    Distinguishes permitted binding of a stale ordinal from a wrong-leg or
    wrong-observation reference (Decision 002 R03/R06/R07).
    """
    # Without a path or coordinate/excerpt, a stale bare handle cannot be bound.
    if not citation.path and not citation.has_coordinate and not citation.excerpt:
        return None

    norm_cited_path = normalize_path(citation.path, case_insensitive=scope.case_insensitive_paths)
    candidates: list[DeliveryRecord] = []
    for rec in _scoped_current_file_records(log, target_leg, scope, kind=parsed_kind):
        if norm_cited_path:
            rec_path = normalize_path(rec.path, case_insensitive=scope.case_insensitive_paths)
            if rec_path != norm_cited_path:
                continue
        supported, _exact, _reason, _reanchor, _contract, _rule, _disputed = _evaluate_coordinate_and_excerpt(
            rec, citation.start_line, citation.end_line, citation.excerpt, raw_citation=citation.raw
        )
        if supported:
            candidates.append(rec)

    if len(candidates) == 1:
        return ReferenceVerdict(
            verdict_class="grounded",
            expected_exact_verdict="bound",
            supporting_handles=(candidates[0].handle,),
            reason="bound_stale_ordinal",
        )
    if len(candidates) > 1:
        return ReferenceVerdict(
            verdict_class="ambiguous",
            expected_exact_verdict="ambiguous",
            supporting_handles=tuple(r.handle for r in candidates),
            reason="ambiguous_stale_ordinal",
        )
    return None


def resolve_citation(
    log: DeliveryLog,
    citation: Citation,
    scope: PermittedScope,
) -> ReferenceVerdict:
    """Resolve one citation strictly from the independent DeliveryLog and PermittedScope."""
    if not log.readable:
        return ReferenceVerdict(
            verdict_class="ungrounded",
            expected_exact_verdict="unverifiable",
            reason="unreadable_ledger",
        )

    ctype = citation.citation_type
    if ctype == "url":
        return _resolve_url(log, citation, scope)
    if ctype == "artifact":
        return _resolve_artifact(log, citation, scope)
    if ctype == "path_coord":
        return _resolve_path_coord(log, citation, scope)
    if ctype in {"handle", "handle_path"}:
        return _resolve_handle(log, citation, scope)
    raise ValueError(f"Unhandled citation_type: {ctype!r}")


def _resolve_handle(
    log: DeliveryLog, citation: Citation, scope: PermittedScope
) -> ReferenceVerdict:
    parsed = parse_handle(
        f"{citation.worker_leg}:{citation.handle}" if citation.worker_leg else citation.handle
    )
    if parsed is None:
        return ReferenceVerdict(
            verdict_class="ungrounded",
            expected_exact_verdict="unverifiable",
            reason="malformed_handle",
        )
    target_leg, parsed_kind, _ord = parsed

    if parsed_kind in MEDIATED_CONTACT_KINDS:
        return ReferenceVerdict(
            verdict_class="ungrounded",
            expected_exact_verdict="unverifiable",
            reason="mediated_contact_only",
        )

    # Check if target leg is even permitted
    if target_leg and target_leg != scope.worker_leg and target_leg not in scope.permitted_legs:
        return ReferenceVerdict(
            verdict_class="ungrounded",
            expected_exact_verdict="unverifiable",
            reason="wrong_leg",
        )

    exact_matches = [
        rec
        for rec in log.records
        if rec.handle == citation.handle and _is_in_leg_scope(rec, target_leg, scope)
    ]

    if not exact_matches:
        outside_reason = _handle_exists_outside_scope(log, citation.handle, target_leg, scope)
        if outside_reason is not None:
            return ReferenceVerdict(
                verdict_class="ungrounded",
                expected_exact_verdict="unverifiable",
                reason=outside_reason,
            )
        bound = _attempt_stale_ordinal_binding(log, citation, target_leg, scope, parsed_kind)
        if bound is not None:
            return bound
        return ReferenceVerdict(
            verdict_class="ungrounded",
            expected_exact_verdict="unverifiable",
            reason="unknown_handle",
        )

    # Exact handle found in scope; never substitute another observation if path/body contradicts (V03).
    rec = exact_matches[0]
    if rec.kind == SOURCE_CHANGE_KIND:
        if citation.path or citation.has_coordinate or citation.excerpt:
            return ReferenceVerdict(
                verdict_class="ungrounded",
                expected_exact_verdict="unverifiable",
                reason="source_change_brief_only",
            )
        return ReferenceVerdict(
            verdict_class="grounded",
            expected_exact_verdict="matched",
            supporting_handles=(rec.handle,),
            reason="source_change_brief_row",
        )

    if citation.path:
        cited_norm = normalize_path(citation.path, case_insensitive=scope.case_insensitive_paths)
        rec_norm = normalize_path(rec.path, case_insensitive=scope.case_insensitive_paths)
        if not rec_norm or cited_norm != rec_norm:
            return ReferenceVerdict(
                verdict_class="ungrounded",
                expected_exact_verdict="unverifiable",
                reason="wrong_path",
            )

    supported, exact_v, reason, reanchor, contract_v, obs_rule, disputed = _evaluate_coordinate_and_excerpt(
        rec, citation.start_line, citation.end_line, citation.excerpt, raw_citation=citation.raw
    )
    if supported:
        return ReferenceVerdict(
            verdict_class="grounded",
            expected_exact_verdict=exact_v,
            supporting_handles=(rec.handle,),
            reason=reason,
            reanchored_line=reanchor,
            contract_reading_verdict=contract_v,
            observed_product_rule=obs_rule,
            disputed_expectation=disputed,
        )
    return ReferenceVerdict(
        verdict_class="ungrounded",
        expected_exact_verdict="unverifiable",
        reason=reason,
        contract_reading_verdict=contract_v,
        observed_product_rule=obs_rule,
        disputed_expectation=disputed,
    )


def _resolve_path_coord(
    log: DeliveryLog, citation: Citation, scope: PermittedScope
) -> ReferenceVerdict:
    if not citation.path:
        return ReferenceVerdict(
            verdict_class="ungrounded",
            expected_exact_verdict="unverifiable",
            reason="missing_path",
        )
    if not citation.has_coordinate and not citation.excerpt:
        return ReferenceVerdict(
            verdict_class="ungrounded",
            expected_exact_verdict="unverifiable",
            reason="bare_path_not_line_evidence",
        )

    cited_norm = normalize_path(citation.path, case_insensitive=scope.case_insensitive_paths)
    candidates = _scoped_current_file_records(log, citation.worker_leg, scope)
    path_matches = [
        rec
        for rec in candidates
        if normalize_path(rec.path, case_insensitive=scope.case_insensitive_paths) == cited_norm
    ]
    if not path_matches:
        return ReferenceVerdict(
            verdict_class="ungrounded",
            expected_exact_verdict="unverifiable",
            reason="path_not_observed",
        )

    supported_results: list[tuple[DeliveryRecord, str, str, int | None, str | None, str, bool]] = []
    first_failure: tuple[str, str | None, str, bool] | None = None

    for rec in path_matches:
        supported, exact_v, reason, reanchor, contract_v, obs_rule, disputed = _evaluate_coordinate_and_excerpt(
            rec, citation.start_line, citation.end_line, citation.excerpt, raw_citation=citation.raw
        )
        if supported:
            supported_results.append((rec, exact_v, reason, reanchor, contract_v, obs_rule, disputed))
        elif first_failure is None:
            first_failure = (reason, contract_v, obs_rule, disputed)

    if len(supported_results) == 1:
        rec, exact_v, reason, reanchor, contract_v, obs_rule, disputed = supported_results[0]
        if exact_v == "matched" and citation.path != rec.path:
            exact_v = "bound"
            if reason == "exact_match":
                reason = "bound_normalized_path"
        return ReferenceVerdict(
            verdict_class="grounded",
            expected_exact_verdict=exact_v,
            supporting_handles=(rec.handle,),
            reason=reason,
            reanchored_line=reanchor,
            contract_reading_verdict=contract_v,
            observed_product_rule=obs_rule,
            disputed_expectation=disputed,
        )

    if len(supported_results) > 1:
        return ReferenceVerdict(
            verdict_class="ambiguous",
            expected_exact_verdict="ambiguous",
            supporting_handles=tuple(r.handle for r, *_ in supported_results),
            reason="ambiguous_multiple_records",
        )

    reason, contract_v, obs_rule, disputed = first_failure or ("uncovered_range", None, "", False)
    return ReferenceVerdict(
        verdict_class="ungrounded",
        expected_exact_verdict="unverifiable",
        reason=reason,
        contract_reading_verdict=contract_v,
        observed_product_rule=obs_rule,
        disputed_expectation=disputed,
    )


def _resolve_url(
    log: DeliveryLog, citation: Citation, scope: PermittedScope
) -> ReferenceVerdict:
    target_url = citation.url.strip()
    if not target_url:
        return ReferenceVerdict(
            verdict_class="ungrounded",
            expected_exact_verdict="unverifiable",
            reason="empty_url",
        )

    admitted_matches: list[DeliveryRecord] = []
    contact_only_hit = False

    for rec in log.records:
        if rec.session_id != scope.session_id:
            continue
        if rec.worker_leg and rec.worker_leg != scope.worker_leg and rec.worker_leg not in scope.permitted_legs:
            continue
        if rec.url != target_url:
            continue
        if rec.kind in MEDIATED_CONTACT_KINDS:
            # GB01 Finding F2: dialed_host#, retrieved_host#, audited_host# NEVER grant URL citation support.
            contact_only_hit = True
            continue
        if not rec.model_visible:
            continue
        if rec.kind in ADMITTED_URL_KINDS:
            admitted_matches.append(rec)

    if len(admitted_matches) == 1:
        return ReferenceVerdict(
            verdict_class="grounded",
            expected_exact_verdict="matched",
            supporting_handles=(admitted_matches[0].handle,),
            reason="url_observed",
        )
    if len(admitted_matches) > 1:
        return ReferenceVerdict(
            verdict_class="grounded",
            expected_exact_verdict="matched",
            supporting_handles=tuple(r.handle for r in admitted_matches),
            reason="url_observed_multiple",
        )
    if contact_only_hit:
        return ReferenceVerdict(
            verdict_class="ungrounded",
            expected_exact_verdict="unverifiable",
            reason="mediated_contact_only",
        )
    return ReferenceVerdict(
        verdict_class="ungrounded",
        expected_exact_verdict="unverifiable",
        reason="url_not_observed",
    )


def _resolve_artifact(
    log: DeliveryLog, citation: Citation, scope: PermittedScope
) -> ReferenceVerdict:
    art_id = citation.artifact_id.strip()
    if not art_id:
        return ReferenceVerdict(
            verdict_class="ungrounded",
            expected_exact_verdict="unverifiable",
            reason="empty_artifact_id",
        )
    if scope.permitted_artifacts and art_id not in scope.permitted_artifacts:
        return ReferenceVerdict(
            verdict_class="ungrounded",
            expected_exact_verdict="unverifiable",
            reason="artifact_not_permitted",
        )

    matches = [
        rec
        for rec in log.records
        if rec.session_id == scope.session_id
        and rec.model_visible
        and rec.artifact_id == art_id
        and (
            rec.worker_leg == scope.worker_leg
            or rec.worker_leg in scope.permitted_legs
        )
    ]
    if matches:
        return ReferenceVerdict(
            verdict_class="grounded",
            expected_exact_verdict="matched",
            supporting_handles=tuple(r.handle for r in matches),
            reason="artifact_present",
        )
    return ReferenceVerdict(
        verdict_class="ungrounded",
        expected_exact_verdict="unverifiable",
        reason="artifact_absent",
    )


def resolve_all(
    log: DeliveryLog,
    citations: Iterable[Citation],
    scope: PermittedScope,
) -> list[ReferenceVerdict]:
    """Resolve a sequence of citations against the same DeliveryLog and scope."""
    return [resolve_citation(log, c, scope) for c in citations]
