"""Pure-Python independent reference resolver for Painted Wolf grounding.

Reads ONLY the campaign's independent DeliveryLog and PermittedScope.
Zero imports or calls to any Go/product grounding packages
(lycaon/internal/evidence, lycaon/internal/citationmd, lycaon/internal/grounding).
"""
from __future__ import annotations

from dataclasses import dataclass
import posixpath
import re
from typing import Iterable, Mapping

from .delivery_log import DeliveryLog, DeliveryRecord


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

# Meaningful-span floor for excerpts (GB02-R6 / docs/grounding.md).
MIN_MEANINGFUL_EXCERPT_CHARS: int = 3

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
    def occurrence_id(self) -> str:
        return self.citation_id

    @property
    def names_handle(self) -> bool:
        return bool(self.handle)

    @property
    def has_coordinate(self) -> bool:
        return self.start_line is not None

    @property
    def is_multi_line_range(self) -> bool:
        return (
            self.start_line is not None
            and self.end_line is not None
            and self.end_line > self.start_line
        )


@dataclass(frozen=True)
class PermittedScope:
    """Execution, dispatch, and provenance boundary for resolving a citation.

    Models observation owner session and viewer scope separately (GB02-R3):
    - `session_id`: the viewer's own session ID.
    - `worker_leg`: empty for coordinator/root, or the worker's leg name.
    - `dispatched_legs`: mapping `leg_id -> child_session_id` for child legs
      dispatched by this viewer (stored as sorted tuple of pairs for immutability).
    - `permitted_legs`: legacy convenience set of same-session leg names when
      `dispatched_legs` is not explicitly supplied.
    - `round_id`: active round boundary for current-source evidence.
    - `cited_results_handles`: prior-round handles explicitly permitted as
      historical provenance (`cited_results`) unless invalidated by refresh.
    - `workspace_refreshed`: when True, invalidates stale prior-round source observations.
    - `invalidated_rounds`: explicit set of rounds invalidated by refresh/rejection.
    - `permitted_artifacts`: `None` means no restriction supplied; an empty set
      `frozenset()` explicitly denies ALL artifacts (GB02-R4).
    - `platform`: `"linux"` (case-sensitive ext4) or `"macos"`/`"darwin"` (case-insensitive APFS).
    - `filesystem_case_sensitive`: optional explicit override; defaults from `platform`.
    """

    session_id: str
    worker_leg: str = ""
    permitted_legs: frozenset[str] = frozenset()
    dispatched_legs: tuple[tuple[str, str], ...] = ()
    round_id: str = ""
    intent_window: str = ""
    cited_results_handles: frozenset[str] = frozenset()
    workspace_refreshed: bool = False
    invalidated_rounds: frozenset[str] = frozenset()
    permitted_artifacts: frozenset[str] | None = None
    platform: str = "linux"
    filesystem_case_sensitive: bool | None = None

    def __post_init__(self) -> None:
        if not isinstance(self.permitted_legs, frozenset):
            object.__setattr__(self, "permitted_legs", frozenset(self.permitted_legs))
        if isinstance(self.dispatched_legs, Mapping):
            object.__setattr__(
                self,
                "dispatched_legs",
                tuple(sorted((str(k), str(v)) for k, v in self.dispatched_legs.items())),
            )
        elif not isinstance(self.dispatched_legs, tuple):
            object.__setattr__(
                self,
                "dispatched_legs",
                tuple(sorted((str(k), str(v)) for k, v in self.dispatched_legs)),
            )
        if not isinstance(self.cited_results_handles, frozenset):
            object.__setattr__(
                self, "cited_results_handles", frozenset(self.cited_results_handles)
            )
        if not isinstance(self.invalidated_rounds, frozenset):
            object.__setattr__(
                self, "invalidated_rounds", frozenset(self.invalidated_rounds)
            )
        if self.permitted_artifacts is not None and not isinstance(
            self.permitted_artifacts, frozenset
        ):
            object.__setattr__(
                self, "permitted_artifacts", frozenset(self.permitted_artifacts)
            )
        if self.platform not in {"linux", "macos", "darwin"}:
            raise ValueError(f"Unsupported platform {self.platform!r}")

    @property
    def viewer_session_id(self) -> str:
        return self.session_id

    @property
    def active_round_id(self) -> str:
        return self.round_id

    @property
    def dispatched_leg_map(self) -> dict[str, str]:
        return dict(self.dispatched_legs)

    @property
    def all_permitted_legs(self) -> frozenset[str]:
        return self.permitted_legs | frozenset(self.dispatched_leg_map.keys())

    @property
    def case_insensitive_paths(self) -> bool:
        if self.filesystem_case_sensitive is not None:
            return not self.filesystem_case_sensitive
        return self.platform in {"macos", "darwin"}


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


def _excerpt_meets_floor(excerpt: str) -> bool:
    """Check the meaningful-span floor for a cited excerpt (GB02-R6)."""
    stripped = "".join(_normalize_newlines(excerpt).split())
    return len(stripped) >= MIN_MEANINGFUL_EXCERPT_CHARS


def _find_contiguous_excerpt_matches(
    record: DeliveryRecord, excerpt: str
) -> list[tuple[int, int]]:
    """Find all (start_line, end_line) spans where `excerpt` matches contiguously.

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

    matches: list[tuple[int, int]] = []
    span_len = len(exc_lines)

    for idx in range(len(line_entries) - span_len + 1):
        window = line_entries[idx : idx + span_len]
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
                matches.append((window[0][0], window[0][0]))
        else:
            ok = True
            for offset, exc_line in enumerate(exc_lines):
                raw_l, content_l = window[offset][1], window[offset][2]
                if exc_line == "":
                    if content_l.strip() != "":
                        ok = False
                        break
                elif offset == 0:
                    if exc_line not in content_l and exc_line.strip() not in content_l and exc_line not in raw_l:
                        ok = False
                        break
                elif offset == span_len - 1:
                    if exc_line not in content_l and exc_line.strip() not in content_l and exc_line not in raw_l:
                        ok = False
                        break
                else:
                    if exc_line != content_l and exc_line.strip() != content_l.strip() and exc_line != raw_l:
                        ok = False
                        break
            if ok:
                matches.append((window[0][0], window[-1][0]))

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
        if not _excerpt_meets_floor(excerpt):
            return (False, "unverifiable", "excerpt_below_floor", None, None, "", False)

        matches = _find_contiguous_excerpt_matches(record, excerpt)
        if not matches:
            return (False, "unverifiable", "excerpt_mismatch", None, None, "", False)
        if start_line is None:
            return (True, "matched", "verified_excerpt", None, None, "", False)

        assert effective_end is not None
        if effective_end < start_line:
            return (False, "unverifiable", "invalid_line_range", None, None, "", False)

        # GB02-R6: Keep inclusive multi-line range strictly separate from single-point re-anchoring.
        if effective_end > start_line:
            if not _range_covered(record, start_line, effective_end):
                return (False, "unverifiable", "uncovered_range", None, None, "", False)
            # Excerpt must fall within the exact cited inclusive range [start_line, effective_end].
            in_bounds = [
                (m_start, m_end)
                for m_start, m_end in matches
                if m_start >= start_line and m_end <= effective_end
            ]
            if in_bounds:
                return (True, "matched", "exact_match", None, None, "", False)
            return (False, "unverifiable", "excerpt_outside_cited_range", None, None, "", False)

        # Single-coordinate (start_line == effective_end) + excerpt
        match_starts = [m_start for m_start, _ in matches]
        if _range_covered(record, start_line, start_line) and start_line in match_starts:
            return (True, "matched", "exact_match", None, None, "", False)
        # Single-point re-anchoring ONLY when the excerpt matches uniquely (1 match)
        # and the matched span is fully within delivered_ranges.
        if len(matches) == 1 and _range_covered(record, matches[0][0], matches[0][1]):
            return (True, "matched", "reanchored_excerpt", matches[0][0], None, "", False)
        return (False, "unverifiable", "excerpt_ambiguous", None, None, "", False)

    # Case 2: Coordinate only, no excerpt
    assert start_line is not None and effective_end is not None
    if effective_end < start_line or not _range_covered(record, start_line, effective_end):
        return (False, "unverifiable", "uncovered_range", None, None, "", False)

    # Decision 004 F3 & Decision 005: printed_other_numbers without an excerpt.
    if record.printed_other_numbers:
        is_copied_host_tag = bool(raw_citation.strip()) and (
            raw_citation.strip() in record.delivery_tags
        )
        if is_copied_host_tag:
            return (
                True,
                "matched",
                "copied_host_delivery_tag",
                None,
                "matched",
                "printed_other_numbers_rejects_unexcerpted_coord",
                True,
            )
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


def _record_belongs_to_viewer_own_scope(record: DeliveryRecord, scope: PermittedScope) -> bool:
    return record.session_id == scope.session_id and record.worker_leg == scope.worker_leg


def _record_belongs_to_dispatched_leg(
    record: DeliveryRecord, target_leg: str, scope: PermittedScope
) -> bool:
    """Check if `record` belongs to `target_leg` under dispatch identity (GB02-R3)."""
    leg_map = scope.dispatched_leg_map
    if target_leg in leg_map:
        child_session = leg_map[target_leg]
        return record.session_id == child_session and record.worker_leg in {"", target_leg}
    if target_leg in scope.permitted_legs:
        return record.session_id == scope.session_id and record.worker_leg == target_leg
    return False


def _is_in_leg_scope(record: DeliveryRecord, target_leg: str, scope: PermittedScope) -> bool:
    if not record.model_visible:
        return False
    if target_leg:
        if target_leg == scope.worker_leg and scope.worker_leg:
            return _record_belongs_to_viewer_own_scope(record, scope)
        return _record_belongs_to_dispatched_leg(record, target_leg, scope)
    return _record_belongs_to_viewer_own_scope(record, scope)


def _record_in_viewer_union(record: DeliveryRecord, scope: PermittedScope) -> tuple[bool, str]:
    """Return (in_union, canonical_display_handle) for a record in viewer's permitted union."""
    if not record.model_visible:
        return (False, "")
    if _record_belongs_to_viewer_own_scope(record, scope):
        return (True, record.handle)
    for leg in sorted(scope.all_permitted_legs):
        if _record_belongs_to_dispatched_leg(record, leg, scope):
            return (True, f"{leg}:{record.handle}")
    return (False, "")


def permitted_union_handles(log: DeliveryLog, scope: PermittedScope) -> frozenset[str]:
    """Return the true scoped union of model-visible display handles (GB02-R3)."""
    if not log.readable:
        return frozenset()
    handles: set[str] = set()
    for rec in log.records:
        in_union, display = _record_in_viewer_union(rec, scope)
        if in_union:
            handles.add(display)
    return frozenset(handles)


def _is_file_region_record(record: DeliveryRecord) -> bool:
    if not record.path:
        return False
    if record.kind in MEDIATED_CONTACT_KINDS or record.kind == SOURCE_CHANGE_KIND:
        return False
    if record.kind in OPAQUE_COMMAND_KINDS and not record.delivered_ranges:
        return False
    return bool(record.delivered_ranges)


def _check_round_currency(record: DeliveryRecord, scope: PermittedScope) -> tuple[bool, str]:
    """Enforce worker round boundary, `cited_results`, and refresh invalidation (GB02-R3)."""
    if not scope.active_round_id or not record.round_id:
        return (True, "")
    if record.round_id == scope.active_round_id:
        return (True, "")
    # Prior-round file-region observation:
    if _is_file_region_record(record):
        if scope.workspace_refreshed or record.round_id in scope.invalidated_rounds:
            return (False, "stale_round_invalidated")
        if record.handle in scope.cited_results_handles:
            return (True, "cited_results_historical")
        return (False, "prior_round_source_evidence")
    return (True, "")


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
        if scope.active_round_id and rec.round_id and rec.round_id != scope.active_round_id:
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
        in_union, _ = _record_in_viewer_union(rec, scope)
        if not in_union:
            continue
        if scope.intent_window and rec.intent_window != scope.intent_window:
            continue
        if rec.kind == SOURCE_CHANGE_KIND or rec.kind in MEDIATED_CONTACT_KINDS:
            continue
        return True
    return False


def is_recallable(record: DeliveryRecord) -> bool:
    """`source_change#` brief handles are not served by `recall` (Decision 002 S06)."""
    return record.kind != SOURCE_CHANGE_KIND and record.kind not in MEDIATED_CONTACT_KINDS


def _handle_exists_outside_scope(
    log: DeliveryLog, handle: str, target_leg: str, scope: PermittedScope
) -> str | None:
    """Detect whether a handle exists only in a forbidden leg or session."""
    for rec in log.records:
        if rec.handle != handle:
            continue
        if _is_in_leg_scope(rec, target_leg, scope):
            continue
        if target_leg:
            if target_leg != scope.worker_leg and target_leg not in scope.all_permitted_legs:
                return "wrong_leg"
            return "wrong_leg"
        if rec.session_id != scope.session_id:
            return "wrong_session"
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
    """Bind a stale ordinal ONLY when structured fields uniquely identify a current record."""
    if not citation.path and not citation.has_coordinate and not citation.excerpt:
        return None

    norm_cited_path = normalize_path(citation.path, case_insensitive=scope.case_insensitive_paths)
    candidates: list[DeliveryRecord] = []
    for rec in _scoped_current_file_records(log, target_leg, scope, kind=parsed_kind):
        if norm_cited_path:
            rec_path = normalize_path(rec.path, case_insensitive=scope.case_insensitive_paths)
            if rec_path != norm_cited_path:
                continue
        supported, *_ = _evaluate_coordinate_and_excerpt(
            rec, citation.start_line, citation.end_line, citation.excerpt, raw_citation=citation.raw
        )
        if supported:
            candidates.append(rec)

    prefix = f"{target_leg}:" if target_leg else ""
    if len(candidates) == 1:
        return ReferenceVerdict(
            verdict_class="grounded",
            expected_exact_verdict="bound",
            supporting_handles=(f"{prefix}{candidates[0].handle}",),
            reason="bound_stale_ordinal",
        )
    if len(candidates) > 1:
        return ReferenceVerdict(
            verdict_class="ambiguous",
            expected_exact_verdict="ambiguous",
            supporting_handles=tuple(f"{prefix}{r.handle}" for r in candidates),
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

    if target_leg and target_leg != scope.worker_leg and target_leg not in scope.all_permitted_legs:
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

    rec = exact_matches[0]
    display_handle = f"{target_leg}:{rec.handle}" if target_leg else rec.handle

    # GB02-R3: Enforce round currency, cited_results, and refresh invalidation for own worker evidence
    if not target_leg or target_leg == scope.worker_leg:
        round_ok, round_reason = _check_round_currency(rec, scope)
        if not round_ok:
            return ReferenceVerdict(
                verdict_class="ungrounded",
                expected_exact_verdict="unverifiable",
                reason=round_reason,
            )
    else:
        round_reason = ""

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
            supporting_handles=(display_handle,),
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
        final_reason = round_reason or reason
        return ReferenceVerdict(
            verdict_class="grounded",
            expected_exact_verdict=exact_v,
            supporting_handles=(display_handle,),
            reason=final_reason,
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

    prefix = f"{citation.worker_leg}:" if citation.worker_leg else ""
    if len(supported_results) == 1:
        rec, exact_v, reason, reanchor, contract_v, obs_rule, disputed = supported_results[0]
        if exact_v == "matched" and citation.path != rec.path:
            exact_v = "bound"
            if reason == "exact_match":
                reason = "bound_normalized_path"
        return ReferenceVerdict(
            verdict_class="grounded",
            expected_exact_verdict=exact_v,
            supporting_handles=(f"{prefix}{rec.handle}",),
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
            supporting_handles=tuple(f"{prefix}{r.handle}" for r, *_ in supported_results),
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

    admitted_matches: list[str] = []
    contact_only_hit = False

    for rec in log.records:
        if rec.url != target_url:
            continue
        # Check whether record is in viewer's permitted union (own session or dispatched child leg).
        # A worker NEVER borrows parent URL evidence (GB02-R3).
        in_own = _record_belongs_to_viewer_own_scope(rec, scope)
        matched_leg = ""
        if not in_own:
            for leg in sorted(scope.all_permitted_legs):
                if _record_belongs_to_dispatched_leg(rec, leg, scope):
                    matched_leg = leg
                    break
        if not in_own and not matched_leg:
            continue

        if rec.kind in MEDIATED_CONTACT_KINDS:
            contact_only_hit = True
            continue
        if not rec.model_visible:
            continue
        if rec.kind in ADMITTED_URL_KINDS:
            admitted_matches.append(f"{matched_leg}:{rec.handle}" if matched_leg else rec.handle)

    if len(admitted_matches) == 1:
        return ReferenceVerdict(
            verdict_class="grounded",
            expected_exact_verdict="matched",
            supporting_handles=(admitted_matches[0],),
            reason="url_observed",
        )
    if len(admitted_matches) > 1:
        return ReferenceVerdict(
            verdict_class="grounded",
            expected_exact_verdict="matched",
            supporting_handles=tuple(admitted_matches),
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

    matches: list[str] = []
    for rec in log.records:
        if rec.artifact_id != art_id:
            continue
        in_union, display = _record_in_viewer_union(rec, scope)
        if in_union:
            matches.append(display)

    if not matches:
        return ReferenceVerdict(
            verdict_class="ungrounded",
            expected_exact_verdict="unverifiable",
            reason="artifact_absent",
        )

    # GB02-R4: None means unrestricted; an explicitly empty set denies all artifacts.
    if scope.permitted_artifacts is not None and art_id not in scope.permitted_artifacts:
        return ReferenceVerdict(
            verdict_class="ungrounded",
            expected_exact_verdict="unverifiable",
            reason="artifact_not_permitted",
        )

    return ReferenceVerdict(
        verdict_class="grounded",
        expected_exact_verdict="matched",
        supporting_handles=tuple(matches),
        reason="artifact_present",
    )


def resolve_all(
    log: DeliveryLog,
    citations: Iterable[Citation],
    scope: PermittedScope,
) -> list[ReferenceVerdict]:
    """Resolve a sequence of citations against the same DeliveryLog and scope."""
    return [resolve_citation(log, c, scope) for c in citations]
