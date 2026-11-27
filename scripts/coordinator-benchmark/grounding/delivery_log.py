"""Independent D3 delivery log schema and separate product-observation inputs.

This module defines the campaign's independent ground-truth delivery log (D3)
alongside strictly separate structs for product-minted records and verdicts.
The checker compares the two without ever copying product evidence into the
delivery log.
"""
from __future__ import annotations

import base64
from dataclasses import dataclass, field
import hashlib
import json
from typing import Any, Mapping, Sequence


GROUNDED_VERDICTS: frozenset[str] = frozenset({"matched", "traced", "bound"})
UNGROUNDED_VERDICTS: frozenset[str] = frozenset({"ambiguous", "unverifiable"})
ALL_VERDICTS: frozenset[str] = GROUNDED_VERDICTS | UNGROUNDED_VERDICTS
WORDINGS: tuple[str, str, str] = ("confident", "hedged", "completion_asserting")


def sha256_bytes(data: bytes) -> str:
    """Return the hex SHA-256 digest of raw bytes."""
    return hashlib.sha256(data).hexdigest()


def encode_raw_bytes(data: bytes) -> str | dict[str, str]:
    """Encode bytes as UTF-8 text when valid, or explicit base64 otherwise."""
    try:
        return data.decode("utf-8")
    except UnicodeDecodeError:
        return {"base64": base64.b64encode(data).decode("ascii")}


def decode_raw_bytes(value: bytes | str | Mapping[str, Any]) -> bytes:
    """Decode UTF-8 text, raw bytes, or explicit base64 mapping to bytes."""
    if isinstance(value, bytes):
        return value
    if isinstance(value, str):
        return value.encode("utf-8")
    if isinstance(value, Mapping) and set(value.keys()) == {"base64"}:
        return base64.b64decode(str(value["base64"]), validate=True)
    raise ValueError("raw_bytes must be bytes, UTF-8 str, or {'base64': ...}")


@dataclass(frozen=True)
class LineRange:
    """1-based inclusive line range delivered for a file-region observation."""

    start: int
    end: int

    def __post_init__(self) -> None:
        if not isinstance(self.start, int) or not isinstance(self.end, int):
            raise ValueError("LineRange bounds must be integers")
        if self.start < 1 or self.end < self.start:
            raise ValueError(f"Invalid 1-based inclusive LineRange: {self.start}-{self.end}")

    def contains_line(self, line: int) -> bool:
        return self.start <= line <= self.end

    def contains_range(self, start: int, end: int) -> bool:
        return self.start <= start <= end <= self.end

    @property
    def line_count(self) -> int:
        return self.end - self.start + 1

    def to_dict(self) -> dict[str, int]:
        return {"start": self.start, "end": self.end}

    @classmethod
    def from_dict(cls, data: Mapping[str, Any]) -> LineRange:
        return cls(start=int(data["start"]), end=int(data["end"]))


@dataclass(frozen=True)
class DeliveryRecord:
    """Independent D3 delivery log record captured at the model/tool boundary.

    Contains the exact 21 fields mandated by D3 and GB02 preflight:
    tool, arguments, raw_bytes, delivered_ranges, delivery_tags, line_numbered,
    printed_other_numbers, session_id, worker_leg, round_id, intent_window,
    source_revision, handle, kind, path, url, artifact_id, call_id,
    fixture_provenance, superseded_by, model_visible.
    """

    tool: str
    arguments: dict[str, Any]
    raw_bytes: bytes
    delivered_ranges: tuple[LineRange, ...]
    delivery_tags: tuple[str, ...]
    line_numbered: bool
    printed_other_numbers: bool
    session_id: str
    worker_leg: str
    round_id: str
    intent_window: str
    source_revision: str
    handle: str
    kind: str
    path: str
    url: str
    artifact_id: str
    call_id: str
    fixture_provenance: str
    superseded_by: str = ""
    model_visible: bool = True

    def __post_init__(self) -> None:
        if isinstance(self.raw_bytes, str):
            object.__setattr__(self, "raw_bytes", self.raw_bytes.encode("utf-8"))
        elif not isinstance(self.raw_bytes, bytes):
            raise TypeError("raw_bytes must be bytes")
        if not isinstance(self.delivered_ranges, tuple):
            object.__setattr__(self, "delivered_ranges", tuple(self.delivered_ranges))
        if not isinstance(self.delivery_tags, tuple):
            object.__setattr__(self, "delivery_tags", tuple(self.delivery_tags))
        if not isinstance(self.arguments, dict):
            object.__setattr__(self, "arguments", dict(self.arguments))

    @property
    def raw_text(self) -> str:
        return self.raw_bytes.decode("utf-8", errors="replace")

    @property
    def body_sha256(self) -> str:
        return sha256_bytes(self.raw_bytes)

    @property
    def is_superseded(self) -> bool:
        return bool(self.superseded_by)

    def to_dict(self) -> dict[str, Any]:
        return {
            "tool": self.tool,
            "arguments": self.arguments,
            "raw_bytes": encode_raw_bytes(self.raw_bytes),
            "delivered_ranges": [r.to_dict() for r in self.delivered_ranges],
            "delivery_tags": list(self.delivery_tags),
            "line_numbered": self.line_numbered,
            "printed_other_numbers": self.printed_other_numbers,
            "session_id": self.session_id,
            "worker_leg": self.worker_leg,
            "round_id": self.round_id,
            "intent_window": self.intent_window,
            "source_revision": self.source_revision,
            "handle": self.handle,
            "kind": self.kind,
            "path": self.path,
            "url": self.url,
            "artifact_id": self.artifact_id,
            "call_id": self.call_id,
            "fixture_provenance": self.fixture_provenance,
            "superseded_by": self.superseded_by,
            "model_visible": self.model_visible,
        }

    @classmethod
    def from_dict(cls, data: Mapping[str, Any]) -> DeliveryRecord:
        return cls(
            tool=str(data["tool"]),
            arguments=dict(data.get("arguments", {})),
            raw_bytes=decode_raw_bytes(data["raw_bytes"]),
            delivered_ranges=tuple(
                LineRange.from_dict(r) for r in data.get("delivered_ranges", ())
            ),
            delivery_tags=tuple(str(t) for t in data.get("delivery_tags", ())),
            line_numbered=bool(data["line_numbered"]),
            printed_other_numbers=bool(data["printed_other_numbers"]),
            session_id=str(data["session_id"]),
            worker_leg=str(data["worker_leg"]),
            round_id=str(data["round_id"]),
            intent_window=str(data["intent_window"]),
            source_revision=str(data["source_revision"]),
            handle=str(data["handle"]),
            kind=str(data["kind"]),
            path=str(data["path"]),
            url=str(data["url"]),
            artifact_id=str(data["artifact_id"]),
            call_id=str(data["call_id"]),
            fixture_provenance=str(data["fixture_provenance"]),
            superseded_by=str(data.get("superseded_by", "")),
            model_visible=bool(data.get("model_visible", True)),
        )


@dataclass(frozen=True)
class DeliveryLog:
    """Append-only independent delivery log for a slot or generated instance.

    `readable=False` models an unreadable evidence ledger (V07, S01, S06), which
    must never be treated as an empty ledger.
    """

    records: tuple[DeliveryRecord, ...] = ()
    readable: bool = True

    def __post_init__(self) -> None:
        if not isinstance(self.records, tuple):
            object.__setattr__(self, "records", tuple(self.records))
        for rec in self.records:
            if not isinstance(rec, DeliveryRecord):
                raise TypeError(
                    "DeliveryLog accepts only independent DeliveryRecord instances, "
                    f"got {type(rec).__name__}"
                )

    def append(self, record: DeliveryRecord) -> DeliveryLog:
        if not isinstance(record, DeliveryRecord):
            raise TypeError(
                "DeliveryLog accepts only independent DeliveryRecord instances"
            )
        return DeliveryLog(records=self.records + (record,), readable=self.readable)

    def with_records(self, records: Sequence[DeliveryRecord]) -> DeliveryLog:
        return DeliveryLog(records=tuple(records), readable=self.readable)

    def by_handle(self, handle: str, *, session_id: str | None = None, worker_leg: str | None = None) -> list[DeliveryRecord]:
        matches = []
        for rec in self.records:
            if rec.handle != handle:
                continue
            if session_id is not None and rec.session_id != session_id:
                continue
            if worker_leg is not None and rec.worker_leg != worker_leg:
                continue
            matches.append(rec)
        return matches

    def to_dict(self) -> dict[str, Any]:
        return {
            "readable": self.readable,
            "records": [r.to_dict() for r in self.records],
        }

    def to_json(self) -> str:
        return json.dumps(self.to_dict(), sort_keys=True, separators=(",", ":"))

    @property
    def sha256(self) -> str:
        return sha256_bytes(self.to_json().encode("utf-8"))

    @classmethod
    def from_dict(cls, data: Mapping[str, Any]) -> DeliveryLog:
        return cls(
            records=tuple(DeliveryRecord.from_dict(r) for r in data.get("records", ())),
            readable=bool(data.get("readable", True)),
        )

    @classmethod
    def from_json(cls, payload: str) -> DeliveryLog:
        return cls.from_dict(json.loads(payload))


# ---------------------------------------------------------------------------
# Separate product-observation and verdict input structs (never mixed into
# DeliveryLog; compared against DeliveryLog by checker.py).
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class ProductRecord:
    """Product-minted evidence record captured for independent comparison."""

    handle: str
    kind: str
    session_id: str
    worker_leg: str
    source_revision: str
    delivered_ranges: tuple[LineRange, ...] = ()
    body_sha256: str = ""
    raw_bytes: bytes | None = None
    path: str = ""
    url: str = ""
    artifact_id: str = ""
    round_id: str = ""
    trust: str = "trusted"
    superseded_by: str = ""

    def __post_init__(self) -> None:
        if not isinstance(self.delivered_ranges, tuple):
            object.__setattr__(self, "delivered_ranges", tuple(self.delivered_ranges))
        if self.raw_bytes is not None and not self.body_sha256:
            object.__setattr__(self, "body_sha256", sha256_bytes(self.raw_bytes))

    def effective_body_sha256(self) -> str:
        if self.body_sha256:
            return self.body_sha256
        if self.raw_bytes is not None:
            return sha256_bytes(self.raw_bytes)
        return ""


@dataclass(frozen=True)
class ProductVerdict:
    """Product grounding verdict for one citation occurrence."""

    citation_id: str
    verdict: str
    resolved_handle: str = ""
    code: str = ""
    feedback: str = ""
    reanchored_line: int | None = None

    def __post_init__(self) -> None:
        if self.verdict not in ALL_VERDICTS:
            raise ValueError(f"Unknown product verdict {self.verdict!r}; expected one of {sorted(ALL_VERDICTS)}")

    @property
    def is_grounded(self) -> bool:
        return self.verdict in GROUNDED_VERDICTS


@dataclass(frozen=True)
class FrictionCounters:
    """D10 friction measures recorded per slot and per surface."""

    repair_attempts: int = 0
    friction_units: int = 0
    host_assembled_closeouts: int = 0
    withheld_notes: int = 0
    breaker_advisories: int = 0

    def __post_init__(self) -> None:
        for name in (
            "repair_attempts",
            "friction_units",
            "host_assembled_closeouts",
            "withheld_notes",
            "breaker_advisories",
        ):
            val = getattr(self, name)
            if type(val) is not int or val < 0:
                raise ValueError(f"FrictionCounters.{name} must be a non-negative int, got {val!r}")

    def delta(self, before: FrictionCounters) -> FrictionCounters:
        """Compute non-negative counter delta (self - before)."""
        return FrictionCounters(
            repair_attempts=self.repair_attempts - before.repair_attempts,
            friction_units=self.friction_units - before.friction_units,
            host_assembled_closeouts=self.host_assembled_closeouts - before.host_assembled_closeouts,
            withheld_notes=self.withheld_notes - before.withheld_notes,
            breaker_advisories=self.breaker_advisories - before.breaker_advisories,
        )

    def to_dict(self) -> dict[str, int]:
        return {
            "repair_attempts": self.repair_attempts,
            "friction_units": self.friction_units,
            "host_assembled_closeouts": self.host_assembled_closeouts,
            "withheld_notes": self.withheld_notes,
            "breaker_advisories": self.breaker_advisories,
        }


@dataclass(frozen=True)
class SurfaceOutcome:
    """Product outcome for one slot run on a given surface and D5 wording."""

    surface: str
    wording: str = "confident"
    verdicts: tuple[ProductVerdict, ...] = ()
    records: tuple[ProductRecord, ...] = ()
    code: str = ""
    traced: bool = True
    evidence_optional: bool = False
    host_assembled: bool = False
    withheld: bool = False
    attached_handles: tuple[str, ...] = ()
    friction_before: FrictionCounters | None = field(default=None)
    friction_after: FrictionCounters | None = field(default=None)

    def __post_init__(self) -> None:
        if self.wording not in WORDINGS:
            raise ValueError(f"Unknown D5 wording {self.wording!r}; expected one of {WORDINGS}")
        if not isinstance(self.verdicts, tuple):
            object.__setattr__(self, "verdicts", tuple(self.verdicts))
        if not isinstance(self.records, tuple):
            object.__setattr__(self, "records", tuple(self.records))
        if not isinstance(self.attached_handles, tuple):
            object.__setattr__(self, "attached_handles", tuple(self.attached_handles))
