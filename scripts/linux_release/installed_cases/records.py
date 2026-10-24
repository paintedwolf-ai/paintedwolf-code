"""Retain the exact observations behind one installed case outcome."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path

from ..installed_evidence import IDENTIFIER, source

class CaseUnavailable(ValueError):
    """A required native interaction cannot run in the selected guest state."""


class CaseFailed(ValueError):
    """A captured installed product or interaction failed its required contract."""


STATUSES = {"passed", "failed", "unverified"}
MAX_STREAMS = 16
MAX_STREAM_BYTES = 8 << 20
MAX_CASE_BYTES = 32 << 20
MAX_OBSERVATION_BYTES = 1 << 20
PHYSICAL_INPUT_STREAM = "physical-input"


def _is_physical_transaction(record: object) -> bool:
    return isinstance(record, dict) and isinstance(record.get("events"), list) and "transaction_id" in record


def summarize_physical_transaction(record: dict, stream_sha256: str) -> dict:
    events = record.get("events", [])
    events_raw = json.dumps(events, sort_keys=True, separators=(",", ":")).encode()
    summary = {key: value for key, value in record.items() if key != "events"}
    summary.update({
        "event_count": len(events),
        "events_sha256": hashlib.sha256(events_raw).hexdigest(),
        "physical_input_stream": PHYSICAL_INPUT_STREAM + ".bin",
        "physical_input_sha256": stream_sha256,
    })
    return summary


def compact_physical_input(observations: dict, streams: dict[str, bytes]) -> tuple[dict, dict[str, bytes]]:
    if PHYSICAL_INPUT_STREAM in streams:
        return observations, streams
    detailed, seen = [], set()
    def collect(record):
        if _is_physical_transaction(record):
            key = (record.get("transaction_id"), record.get("operation"))
            if key not in seen:
                seen.add(key)
                detailed.append(record)
    for launch_key in ("launch", "launch_before", "launch_after"):
        launch = observations.get(launch_key)
        if isinstance(launch, dict) and isinstance(launch.get("fixed_input"), list):
            for row in launch["fixed_input"]:
                collect(row)
    for list_key in ("fixed_input", "transitions"):
        rows = observations.get(list_key)
        if isinstance(rows, list):
            for row in rows:
                collect(row)
    for single_key in ("native_input", "lock_transition"):
        collect(observations.get(single_key))
    for editor_key in ("editor_keyboard", "editor_search_keyboard"):
        editor = observations.get(editor_key)
        if isinstance(editor, dict):
            collect(editor.get("physical_vm_keyboard"))
    if not detailed:
        return observations, streams
    payload_raw = (json.dumps({"format_version": 1, "transactions": detailed},
                              sort_keys=True, separators=(",", ":")) + "\n").encode()
    stream_sha256 = hashlib.sha256(payload_raw).hexdigest()
    compacted = dict(observations)
    for launch_key in ("launch", "launch_before", "launch_after"):
        launch = compacted.get(launch_key)
        if isinstance(launch, dict) and isinstance(launch.get("fixed_input"), list):
            compacted[launch_key] = {
                **launch,
                "fixed_input": [summarize_physical_transaction(row, stream_sha256)
                                if _is_physical_transaction(row) else row for row in launch["fixed_input"]],
            }
    for list_key in ("fixed_input", "transitions"):
        rows = compacted.get(list_key)
        if isinstance(rows, list):
            compacted[list_key] = [summarize_physical_transaction(row, stream_sha256)
                                   if _is_physical_transaction(row) else row for row in rows]
    for single_key in ("native_input", "lock_transition"):
        if _is_physical_transaction(compacted.get(single_key)):
            compacted[single_key] = summarize_physical_transaction(compacted[single_key], stream_sha256)
    for editor_key in ("editor_keyboard", "editor_search_keyboard"):
        editor = compacted.get(editor_key)
        if isinstance(editor, dict) and _is_physical_transaction(editor.get("physical_vm_keyboard")):
            compacted[editor_key] = {
                **editor,
                "physical_vm_keyboard": summarize_physical_transaction(editor["physical_vm_keyboard"], stream_sha256),
            }
    return compacted, {**streams, PHYSICAL_INPUT_STREAM: payload_raw}


def retain(binding: dict, capability: str, case: str, evidence_root: Path, status: str,
           observations: dict, streams: dict[str, bytes] | None = None) -> dict:
    if status not in STATUSES or not all(IDENTIFIER.fullmatch(value) for value in (capability, case)):
        raise ValueError("installed case has an invalid identity or outcome")
    source(binding["source_commit"])
    if not isinstance(observations, dict):
        raise ValueError("installed case observations must be an object")
    streams = dict(streams or {})
    if any(not isinstance(raw, bytes) for raw in streams.values()):
        raise ValueError("installed case evidence stream exceeds its contract")
    observations, streams = compact_physical_input(observations, streams)
    if (len(streams) > MAX_STREAMS or any(not IDENTIFIER.fullmatch(name) or not isinstance(raw, bytes)
            or len(raw) > MAX_STREAM_BYTES for name, raw in streams.items())
            or sum(map(len, streams.values())) > MAX_CASE_BYTES):
        raise ValueError("installed case evidence stream exceeds its contract")
    document = {"format_version": 1, "source_commit": binding["source_commit"],
                "artifact": binding["artifact"], "capability": capability,
                "case": case, "status": status, "observations": observations}
    document_raw = (json.dumps(document, indent=2, sort_keys=True) + "\n").encode()
    if len(document_raw) > MAX_OBSERVATION_BYTES or len(document_raw) + sum(map(len, streams.values())) > MAX_CASE_BYTES:
        raise ValueError("installed case observations exceed the aggregate evidence contract")
    directory = evidence_root / capability / case
    directory.mkdir(parents=True, exist_ok=False)
    records = []
    for name, raw in streams.items():
        path = directory / (name + ".bin")
        with path.open("xb") as stream:
            stream.write(raw)
        records.append({"path": path.relative_to(evidence_root).as_posix(),
                        "sha256": hashlib.sha256(raw).hexdigest()})
    path = directory / "execution.json"
    with path.open("xb") as stream:
        stream.write(document_raw)
    records.append({"path": path.relative_to(evidence_root).as_posix(),
                    "sha256": hashlib.sha256(document_raw).hexdigest()})
    return {"id": case, "status": status, "evidence": records}
