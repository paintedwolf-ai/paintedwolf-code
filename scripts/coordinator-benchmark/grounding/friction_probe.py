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
import platform
import re
import sqlite3
from typing import Any, Iterable, Mapping

from .checker import VALID_MID_TURN_NOTE_CODES
from .delivery_log import FrictionCounters

_UUID_RE = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")


@dataclass(frozen=True)
class FrictionHit:
    """One exact-binary `recordFrictionLocked` charge attributed to (session_id, root_id)."""

    index: int
    session_id: str = ""
    root_id: str = ""


def build_exact_binary_debugger_prefix(
    work_dir: str | pathlib.Path,
    *,
    platform_system: str | None = None,
) -> list[str]:
    """Generate Decision 012 GDB (Linux) or LLDB (macOS) breakpoint wrapper for `recordFrictionLocked`."""
    root = pathlib.Path(work_dir)
    root.mkdir(parents=True, exist_ok=True)
    os_name = platform_system or platform.system()
    if os_name == "Darwin":
        setup = root / "trace.lldb"
        callback = root / "trace_callback.py"
        callback.write_text(
            'import re\n'
            'import lldb\n\n'
            'def hit(frame, bp_loc, internal_dict):\n'
            '    print("PW_GND_FRICTION_HIT", flush=True)\n'
            '    process = frame.GetThread().GetProcess()\n'
            '    for key, addr_register, len_register in (("sessionID", "x1", "x2"), ("rootID", "x3", "x4")):\n'
            '        address = frame.FindRegister(addr_register).GetValueAsUnsigned()\n'
            '        size = frame.FindRegister(len_register).GetValueAsUnsigned()\n'
            '        if size != 36 or not address:\n'
            '            continue\n'
            '        error = lldb.SBError()\n'
            '        raw = process.ReadMemory(address, size, error)\n'
            '        if error.Success() and re.fullmatch(rb"[0-9a-f-]{36}", raw):\n'
            '            print("PW_GND_ARG " + key + " " + raw.decode("ascii"), flush=True)\n'
            '    return False\n',
            encoding="utf-8",
        )
        setup.write_text(
            "settings set target.disable-aslr false\n"
            "breakpoint set --func-regex recordFrictionLocked\n"
            f"command script import {callback}\n"
            "breakpoint command add -F trace_callback.hit 1\n"
            "breakpoint modify --auto-continue true 1\n"
            "process launch --stop-at-entry\n"
            'script print("PW_GND_OWNED_PID="+str(lldb.debugger.GetSelectedTarget().GetProcess().GetProcessID()),flush=True)\n'
            "continue\n",
            encoding="utf-8",
        )
        return ["/usr/bin/lldb", "--batch", "-s", str(setup), "--"]

    setup = root / "trace.gdb"
    setup.write_text(
        "set pagination off\n"
        "set confirm off\n"
        "set disable-randomization off\n"
        "set auto-load libthread-db off\n"
        "set libthread-db-search-path /nonexistent\n"
        "set detach-on-fork on\n"
        "set follow-fork-mode parent\n"
        "starti\n"
        'python print("PW_GND_OWNED_PID="+str(gdb.selected_inferior().pid),flush=True)\n'
        "python\n"
        '_, locs = gdb.decode_line("github.com/lycaon/lycaon/internal/session/closeouts.(*Service).recordFrictionLocked")\n'
        "sal = locs[0]\n"
        'bp = gdb.Breakpoint(f"{sal.symtab.filename}:{sal.line + 2}")\n'
        "bp.silent = True\n"
        'bp.commands = \'printf "PW_GND_FRICTION_HIT\\\\n"\\ninfo args\\ncontinue\\n\'\n'
        "end\n"
        "continue\n",
        encoding="utf-8",
    )
    return ["/usr/bin/gdb", "-nh", "-nx", "-batch", "-iex", "set auto-load off", "-x", str(setup), "--args"]


def parse_friction_trace_log(log_text: str) -> tuple[int | None, tuple[FrictionHit, ...]]:
    """Parse `PW_GND_OWNED_PID` and attributed `PW_GND_FRICTION_HIT` entries from GDB/LLDB output."""
    owned_pid: int | None = None
    pid_match = re.search(r"^PW_GND_OWNED_PID=(\d+)$", log_text, re.MULTILINE)
    if pid_match:
        owned_pid = int(pid_match.group(1))

    hits: list[FrictionHit] = []
    cur_idx = -1
    cur_session = ""
    cur_root = ""

    def _flush() -> None:
        nonlocal cur_idx, cur_session, cur_root
        if cur_idx >= 0:
            hits.append(FrictionHit(index=cur_idx, session_id=cur_session, root_id=cur_root))
            cur_idx = -1
            cur_session = ""
            cur_root = ""

    for raw_line in log_text.splitlines():
        line = raw_line.strip()
        if line == "PW_GND_FRICTION_HIT":
            _flush()
            cur_idx = len(hits)
            continue
        if cur_idx < 0:
            continue
        if line.startswith("PW_GND_ARG sessionID "):
            m = _UUID_RE.search(line)
            if m:
                cur_session = m.group(0)
        elif line.startswith("PW_GND_ARG rootID "):
            m = _UUID_RE.search(line)
            if m:
                cur_root = m.group(0)
        elif line.startswith("sessionID ="):
            m = _UUID_RE.search(line)
            if m:
                cur_session = m.group(0)
        elif line.startswith("rootID ="):
            m = _UUID_RE.search(line)
            if m:
                cur_root = m.group(0)
    _flush()
    return owned_pid, tuple(hits)


def count_attributed_friction_hits(
    hits: Iterable[FrictionHit],
    *,
    session_id: str = "",
    root_id: str = "",
) -> int:
    """Count exact-binary friction hits attributed to `session_id` and/or `root_id`."""
    total = 0
    for h in hits:
        if session_id and h.session_id and h.session_id != session_id:
            continue
        if root_id and h.root_id and h.root_id != root_id:
            continue
        if session_id and not h.session_id and root_id and not h.root_id:
            continue
        if session_id and not h.session_id and not root_id:
            continue
        total += 1
    return total


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
    trace_log_path: str | pathlib.Path | None = None,
    root_id: str = "",
) -> FrictionSnapshot:
    """Extract all D1-observable D10 counters from `store.db`, SSE events, and optional Decision 012 trace.

    De-duplicates typed events across `event_outbox` and SSE by canonical `(event_id, code)`.
    When `trace_log_path` is provided, extracts exact `friction_units` attributed to `(session_id, root_id)`
    from Decision 012's `recordFrictionLocked` GDB/LLDB trace; otherwise marks `friction_units` `unknown`.
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
            for opt in ("grounding_json", "workflow_feedback_json", "host_signal_id", "kind", "content"):
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
                if "content" in cols and row["content"]:
                    c_text = str(row["content"])
                    for known_code in VALID_MID_TURN_NOTE_CODES | BREAKER_ADVISORY_CODES:
                        if known_code in c_text:
                            codes.add(known_code)

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
            payload_col = next((c for c in ("data_json", "payload_json", "payload", "data") if c in cols), None)
            id_col = "event_id" if "event_id" in cols else ("id" if "id" in cols else "rowid")
            has_sess_col = "session_id" in cols
            if payload_col:
                sel = f"SELECT {id_col} AS eid, {payload_col} AS payload" + (", session_id AS sid" if has_sess_col else "") + " FROM event_outbox"
                for row in conn.execute(sel).fetchall():
                    payload = str(row["payload"] or "")
                    row_sid = str(row["sid"] or "") if has_sess_col else ""
                    if session_id and row_sid != session_id and session_id not in payload:
                        continue
                    for code in _extract_codes_from_json(payload):
                        if code in VALID_MID_TURN_NOTE_CODES:
                            withheld_keys.add(f"event:{row['eid']}:{code}")
                        if code in BREAKER_ADVISORY_CODES:
                            breaker_keys.add(f"event:{row['eid']}:{code}")

    for idx, ev in enumerate(sse_events):
        data_obj = ev.get("data") if isinstance(ev.get("data"), Mapping) else {}
        msg_obj = data_obj.get("message") if isinstance(data_obj.get("message"), Mapping) else {}
        ev_session = str(
            ev.get("session_id")
            or data_obj.get("session_id")
            or msg_obj.get("session_id")
            or ""
        )
        if ev_session and ev_session != session_id:
            continue
        ev_msg_id = str(
            ev.get("message_id")
            or data_obj.get("message_id")
            or msg_obj.get("id")
            or ""
        )
        ev_id = str(ev.get("event_id") or ev.get("id") or f"sse-{idx}")
        key_prefix = f"msg:{ev_msg_id}" if ev_msg_id else f"event:{ev_id}"
        ev_blob = json.dumps(ev)
        ev_codes = _extract_codes_from_json(ev_blob)
        for known_code in VALID_MID_TURN_NOTE_CODES | BREAKER_ADVISORY_CODES:
            if known_code in ev_blob:
                ev_codes.add(known_code)
        for code in ev_codes:
            if code in VALID_MID_TURN_NOTE_CODES:
                withheld_keys.add(f"{key_prefix}:{code}")
            if code in BREAKER_ADVISORY_CODES:
                breaker_keys.add(f"{key_prefix}:{code}")

    if trace_log_path is not None and pathlib.Path(trace_log_path).is_file():
        trace_text = pathlib.Path(trace_log_path).read_text(encoding="utf-8", errors="replace")
        _, hits = parse_friction_trace_log(trace_text)
        attributed_units = count_attributed_friction_hits(hits, session_id=session_id, root_id=root_id)
        friction_obs = CounterObservation(
            name="friction_units",
            value=attributed_units,
            status="observed",
            source="debugger:recordFrictionLocked(sessionID,rootID)",
            note=f"Exact-binary Decision 012 trace ({len(hits)} total process hits; {attributed_units} attributed).",
        )
    else:
        friction_obs = CounterObservation(
            name="friction_units",
            value=None,
            status="unknown",
            source="in_memory_only:closeout_lifecycle.go",
            note="Shared prompt/cycle groundingRejects live in scopedstore.LRU in memory on D1.",
        )

    return FrictionSnapshot(
        repair_attempts=CounterObservation(
            name="repair_attempts",
            value=repair_attempts,
            status="observed",
            source="store.db:messages.grounding_json.retry_count",
            note="Committed closeout retry_count; in-flight documentAttempt split is in-memory only.",
        ),
        friction_units=friction_obs,
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
