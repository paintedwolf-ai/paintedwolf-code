"""D10 friction observability probe and D1 source-backed measurement proposal.

D1 (main @ 3bb1464d) observability audit:
- `repair_attempts`: OBSERVABLE (partial/per-closeout) from `messages.grounding_json`
  (`retry_count`, `CitationGrounding.RetryCount`). Caveat: `closeout_retry_budget.go`
  separates `attempt` (citation + presentation) from `documentAttempt` (report doc)
  in memory; `retry_count` reflects the committed closeout's retry count.
- `friction_units`: IN-MEMORY ONLY (`unknown` on unmodified D1). Owned by
  `lycaon/internal/session/closeouts/closeout_lifecycle.go` (`Service.prompts` and
  `Service.cycles` LRU maps). `recordFrictionLocked` increments both counters in
  memory and returns only `Remaining`. Presentation repair spends `retry_count`
  without charging friction, while mid-turn notes (`mid_turn_note.go:59`), ambient
  audits (`ambient.go:225`), and `Delay` charge shared friction without incrementing
  closeout `retry_count`. Therefore neither retry count nor warning count can
  establish shared `friction_units`.
- `host_assembled_closeouts`: OBSERVABLE from `messages.grounding_json`
  (`host_assembled: true`, `CitationGrounding.HostAssembled`).
- `withheld_notes`: OBSERVABLE from persisted host/feedback messages and
  `event_outbox`/SSE frames carrying typed `MID_TURN_NOTE_*` codes.
- `breaker_advisories`: OBSERVABLE (emitted advisory/escalation notices) from
  persisted messages / `event_outbox` / SSE frames carrying
  `COORDINATOR_GROUNDING_ESCALATED`, `AMBIENT_GROUNDING_ESCALATED`,
  `COORDINATOR_UNGROUNDED_CLAIM`, or `AMBIENT_UNGROUNDED_COMPLETION`. Raw
  `StateStore.bySess` (`TotalWarnings`, `ConsecutiveWarnings`) in
  `lycaon/internal/grounding/state.go` is in-memory only.

D7-compliant measurement proposal:
- In-process lane: read exact `closeouts.Service` and `grounding.StateStore`
  counters via test-only Go harness files (no shipped binary hook).
- Sidecar lane: extract `repair_attempts`, `host_assembled_closeouts`,
  `withheld_notes`, and `breaker_advisories` strictly from `store.db` and
  `/v1/events`; mark `friction_units` explicitly `unknown` on unmodified D1 while
  preserving all known grounding defects. If exact sidecar `friction_units` is
  needed later, expose cycle/turn friction spent as a first-class persisted audit
  field on `CitationGrounding` / `turn_attempt_closeouts` (not a test-only seam).
"""
from __future__ import annotations

import contextlib
from dataclasses import dataclass
import json
import pathlib
import sqlite3
from typing import Any, Iterable, Mapping

from .checker import VALID_MID_TURN_NOTE_CODES
from .delivery_log import FrictionCounters


BREAKER_ADVISORY_CODES: frozenset[str] = frozenset(
    {
        "COORDINATOR_GROUNDING_ESCALATED",
        "AMBIENT_GROUNDING_ESCALATED",
        "COORDINATOR_UNGROUNDED_CLAIM",
        "AMBIENT_UNGROUNDED_COMPLETION",
    }
)


@dataclass(frozen=True)
class CounterObservation:
    """Typed observation for one D10 friction counter."""

    name: str
    value: int | None
    status: str  # "observed" | "unknown"
    source: str
    note: str = ""

    def to_dict(self) -> dict[str, Any]:
        return {
            "name": self.name,
            "value": self.value,
            "status": self.status,
            "source": self.source,
            "note": self.note,
        }


@dataclass(frozen=True)
class FrictionSnapshot:
    """Before/after D10 friction snapshot with explicit unknown-counter tracking."""

    repair_attempts: CounterObservation
    friction_units: CounterObservation
    host_assembled_closeouts: CounterObservation
    withheld_notes: CounterObservation
    breaker_advisories: CounterObservation

    def all_observations(self) -> tuple[CounterObservation, ...]:
        return (
            self.repair_attempts,
            self.friction_units,
            self.host_assembled_closeouts,
            self.withheld_notes,
            self.breaker_advisories,
        )

    def unknown_counters(self) -> tuple[str, ...]:
        return tuple(obs.name for obs in self.all_observations() if obs.status != "observed" or obs.value is None)

    def is_complete(self) -> bool:
        return not self.unknown_counters()

    def to_friction_counters(self) -> FrictionCounters | None:
        """Return FrictionCounters only when all 5 counters are observed; else None."""
        if not self.is_complete():
            return None
        return FrictionCounters(
            repair_attempts=int(self.repair_attempts.value or 0),
            friction_units=int(self.friction_units.value or 0),
            host_assembled_closeouts=int(self.host_assembled_closeouts.value or 0),
            withheld_notes=int(self.withheld_notes.value or 0),
            breaker_advisories=int(self.breaker_advisories.value or 0),
        )

    def delta_observed(self, before: FrictionSnapshot) -> dict[str, int | None]:
        """Compute per-counter deltas, returning None for any unknown counter."""
        out: dict[str, int | None] = {}
        for after_obs, before_obs in zip(self.all_observations(), before.all_observations()):
            if (
                after_obs.status == "observed"
                and before_obs.status == "observed"
                and after_obs.value is not None
                and before_obs.value is not None
            ):
                out[after_obs.name] = max(0, after_obs.value - before_obs.value)
            else:
                out[after_obs.name] = None
        return out

    def to_dict(self) -> dict[str, Any]:
        return {
            "complete": self.is_complete(),
            "unknown_counters": list(self.unknown_counters()),
            "counters": {obs.name: obs.to_dict() for obs in self.all_observations()},
        }


def _extract_codes_from_json(blob: str | None) -> set[str]:
    if not blob:
        return set()
    try:
        parsed = json.loads(blob)
    except (TypeError, ValueError):
        return set()
    codes: set[str] = set()

    def _walk(obj: Any) -> None:
        if isinstance(obj, Mapping):
            for key, val in obj.items():
                if key in {"code", "hint_code", "grounding_verdict", "rejection_code"} and isinstance(val, str):
                    codes.add(val.strip())
                else:
                    _walk(val)
        elif isinstance(obj, list):
            for item in obj:
                _walk(item)

    _walk(parsed)
    return codes


def probe_store_db(
    db_path: str | pathlib.Path,
    session_id: str,
    *,
    sse_events: Iterable[Mapping[str, Any]] = (),
) -> FrictionSnapshot:
    """Extract all D1-observable D10 counters from `store.db` and SSE events.

    Marks `friction_units` explicitly `unknown` because D1 stores shared cycle
    and prompt friction strictly in memory (`closeout_lifecycle.go`).
    """
    db_file = pathlib.Path(db_path)
    if not db_file.is_file():
        return FrictionSnapshot(
            repair_attempts=CounterObservation("repair_attempts", None, "unknown", "store.db", "store.db not found"),
            friction_units=CounterObservation("friction_units", None, "unknown", "in_memory_only", "closeout_lifecycle.go LRU is in-memory only on D1"),
            host_assembled_closeouts=CounterObservation("host_assembled_closeouts", None, "unknown", "store.db", "store.db not found"),
            withheld_notes=CounterObservation("withheld_notes", None, "unknown", "store.db", "store.db not found"),
            breaker_advisories=CounterObservation("breaker_advisories", None, "unknown", "store.db", "store.db not found"),
        )

    repair_attempts = 0
    host_assembled = 0
    withheld_keys: set[str] = set()
    breaker_keys: set[str] = set()

    with contextlib.closing(sqlite3.connect(f"file:{db_file}?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        tables = {
            row[0]
            for row in conn.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()
        }

        if "messages" in tables:
            cols = {
                row[1]
                for row in conn.execute("PRAGMA table_info(messages)").fetchall()
            }
            select_cols = ["id"]
            for opt in ("grounding_json", "workflow_feedback_json", "host_signal_id", "kind"):
                if opt in cols:
                    select_cols.append(opt)
            query = f"SELECT {', '.join(select_cols)} FROM messages WHERE session_id = ?"
            for row in conn.execute(query, (session_id,)).fetchall():
                msg_id = str(row["id"])
                g_raw = row["grounding_json"] if "grounding_json" in cols else ""
                if g_raw:
                    try:
                        g_obj = json.loads(g_raw)
                    except (TypeError, ValueError):
                        g_obj = {}
                    if isinstance(g_obj, Mapping):
                        rc = g_obj.get("retry_count")
                        if isinstance(rc, int) and rc > 0:
                            repair_attempts += rc
                        if g_obj.get("host_assembled") is True:
                            host_assembled += 1

                codes = set()
                if "grounding_json" in cols:
                    codes |= _extract_codes_from_json(row["grounding_json"])
                if "workflow_feedback_json" in cols:
                    codes |= _extract_codes_from_json(row["workflow_feedback_json"])
                if "host_signal_id" in cols and row["host_signal_id"]:
                    codes.add(str(row["host_signal_id"]).strip())

                for code in codes:
                    if code in VALID_MID_TURN_NOTE_CODES:
                        withheld_keys.add(f"msg:{msg_id}:{code}")
                    if code in BREAKER_ADVISORY_CODES:
                        breaker_keys.add(f"msg:{msg_id}:{code}")

        if "event_outbox" in tables:
            cols = {
                row[1]
                for row in conn.execute("PRAGMA table_info(event_outbox)").fetchall()
            }
            payload_col = next((c for c in ("payload_json", "payload", "data") if c in cols), None)
            id_col = "id" if "id" in cols else "rowid"
            if payload_col:
                for row in conn.execute(f"SELECT {id_col} AS eid, {payload_col} AS payload FROM event_outbox").fetchall():
                    payload = str(row["payload"] or "")
                    if session_id and session_id not in payload:
                        continue
                    for code in _extract_codes_from_json(payload):
                        if code in VALID_MID_TURN_NOTE_CODES:
                            withheld_keys.add(f"outbox:{row['eid']}:{code}")
                        if code in BREAKER_ADVISORY_CODES:
                            breaker_keys.add(f"outbox:{row['eid']}:{code}")

    for idx, ev in enumerate(sse_events):
        ev_session = str(ev.get("session_id") or "")
        if ev_session and ev_session != session_id:
            continue
        ev_id = str(ev.get("id") or f"sse-{idx}")
        for code in _extract_codes_from_json(json.dumps(ev)):
            if code in VALID_MID_TURN_NOTE_CODES:
                withheld_keys.add(f"sse:{ev_id}:{code}")
            if code in BREAKER_ADVISORY_CODES:
                breaker_keys.add(f"sse:{ev_id}:{code}")

    return FrictionSnapshot(
        repair_attempts=CounterObservation(
            name="repair_attempts",
            value=repair_attempts,
            status="observed",
            source="store.db:messages.grounding_json.retry_count",
            note="Committed closeout retry_count; in-flight documentAttempt split is in-memory only.",
        ),
        friction_units=CounterObservation(
            name="friction_units",
            value=None,
            status="unknown",
            source="in_memory_only:closeout_lifecycle.go",
            note="Shared prompt/cycle groundingRejects live in scopedstore.LRU in memory on D1.",
        ),
        host_assembled_closeouts=CounterObservation(
            name="host_assembled_closeouts",
            value=host_assembled,
            status="observed",
            source="store.db:messages.grounding_json.host_assembled",
        ),
        withheld_notes=CounterObservation(
            name="withheld_notes",
            value=len(withheld_keys),
            status="observed",
            source="store.db:messages+event_outbox+sse(MID_TURN_NOTE_*)",
        ),
        breaker_advisories=CounterObservation(
            name="breaker_advisories",
            value=len(breaker_keys),
            status="observed",
            source="store.db:messages+event_outbox+sse(GROUNDING_ESCALATED/UNGROUNDED_*)",
            note="Counts emitted typed advisories; raw StateStore warning streak is in-memory only.",
        ),
    )


def friction_observability_report() -> dict[str, Any]:
    """Return the D10 friction observability matrix and D7 measurement proposal."""
    return {
        "baseline": "D1 (main @ 3bb1464d)",
        "counters": {
            "repair_attempts": {
                "sidecar_observable": True,
                "source": "store.db:messages.grounding_json.retry_count",
                "caveat": "Does not separate citation vs report-document attempts when documentAttempt is in-flight.",
            },
            "friction_units": {
                "sidecar_observable": False,
                "status_on_unmodified_d1": "unknown",
                "owner": "lycaon/internal/session/closeouts/closeout_lifecycle.go (Service.prompts, Service.cycles)",
                "why_not_derivable": (
                    "Presentation repair spends retry_count without charging friction, while mid-turn notes "
                    "(mid_turn_note.go:59), ambient audits (ambient.go:225), and Delay charge shared friction "
                    "without incrementing closeout retry_count."
                ),
            },
            "host_assembled_closeouts": {
                "sidecar_observable": True,
                "source": "store.db:messages.grounding_json.host_assembled",
            },
            "withheld_notes": {
                "sidecar_observable": True,
                "source": "store.db:messages + event_outbox + /v1/events (MID_TURN_NOTE_* codes)",
            },
            "breaker_advisories": {
                "sidecar_observable": True,
                "source": "store.db:messages + event_outbox + /v1/events (COORDINATOR_*/AMBIENT_* codes)",
                "caveat": "Raw StateStore.bySess streak counters in lycaon/internal/grounding/state.go are in-memory only.",
            },
        },
        "d7_compliant_proposal": {
            "in_process_lane": (
                "Measure exact closeouts.Service prompt/cycle groundingRejects and grounding.StateStore "
                "counters directly via test-only Go harness files without adding any seam to the shipped binary."
            ),
            "sidecar_lane": (
                "Extract the 4 observable counters from store.db + /v1/events and record friction_units "
                "explicitly as unknown while preserving all known grounding defects. If exact sidecar "
                "friction_units is later required, add a durable user/operator audit field on "
                "CitationGrounding or turn_attempt_closeouts rather than a test-only hook."
            ),
        },
    }
