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
    return (isinstance(record, dict) and isinstance(record.get("events"), list)
            and isinstance(record.get("transaction_id"), str))


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
    detailed_list: list[dict] = []
    seen: set[tuple[object, object]] = set()

    def collect(record: object) -> None:
        if _is_physical_transaction(record):
            assert isinstance(record, dict)
            key = (record.get("transaction_id"), record.get("operation"))
            if key not in seen:
                seen.add(key)
                detailed_list.append(record)

    for launch_key in ("launch", "launch_before", "launch_after"):
        launch = observations.get(launch_key)
        if isinstance(launch, dict) and isinstance(launch.get("fixed_input"), list):
            for item in launch["fixed_input"]:
                collect(item)
    for list_key in ("fixed_input", "transitions"):
        rows = observations.get(list_key)
        if isinstance(rows, list):
            for item in rows:
                collect(item)
    for single_key in ("native_input", "lock_transition"):
        collect(observations.get(single_key))
    for editor_key in ("editor_keyboard", "editor_search_keyboard"):
        editor = observations.get(editor_key)
        if isinstance(editor, dict):
            collect(editor.get("physical_vm_keyboard"))
    if not detailed_list:
        return observations, streams
    payload_raw = (json.dumps({"format_version": 1, "transactions": detailed_list},
                              sort_keys=True, separators=(",", ":")) + "\n").encode()
    stream_sha256 = hashlib.sha256(payload_raw).hexdigest()
    compacted = dict(observations)
    for launch_key in ("launch", "launch_before", "launch_after"):
        launch = compacted.get(launch_key)
        if isinstance(launch, dict) and isinstance(launch.get("fixed_input"), list):
            compacted[launch_key] = {
                **launch,
                "fixed_input": [summarize_physical_transaction(row, stream_sha256)
                                if _is_physical_transaction(row) else row
                                for row in launch["fixed_input"]],
            }
    for list_key in ("fixed_input", "transitions"):
        rows = compacted.get(list_key)
        if isinstance(rows, list):
            compacted[list_key] = [summarize_physical_transaction(row, stream_sha256)
                                   if _is_physical_transaction(row) else row
                                   for row in rows]
    for single_key in ("native_input", "lock_transition"):
        if _is_physical_transaction(compacted.get(single_key)):
            compacted[single_key] = summarize_physical_transaction(compacted[single_key], stream_sha256)
    for editor_key in ("editor_keyboard", "editor_search_keyboard"):
        editor = compacted.get(editor_key)
        if isinstance(editor, dict) and _is_physical_transaction(editor.get("physical_vm_keyboard")):
            compacted[editor_key] = {
                **editor,
                "physical_vm_keyboard": summarize_physical_transaction(
                    editor["physical_vm_keyboard"], stream_sha256),
            }
    return compacted, {**streams, PHYSICAL_INPUT_STREAM: payload_raw}


def expand_physical_transactions(leaf: Path | None, selected: list[dict]) -> list[dict]:
    if not any(isinstance(record, dict) and ("physical_input_stream" in record or "events_sha256" in record
               or "physical_input_sha256" in record or "event_count" in record) for record in selected):
        return selected
    if leaf is None:
        raise ValueError("compacted physical input requires its retained stream path")
    path = leaf.with_name(PHYSICAL_INPUT_STREAM + ".bin")
    if not path.is_file() or not 0 < path.stat().st_size <= MAX_STREAM_BYTES:
        raise ValueError("physical input stream is missing or exceeds its bound")
    raw = path.read_bytes()
    stream_sha256 = hashlib.sha256(raw).hexdigest()

    def pairs(items: list[tuple[str, object]]) -> dict[str, object]:
        result: dict[str, object] = {}
        for key, value in items:
            if key in result:
                raise ValueError("physical input stream repeats a key")
            result[key] = value
        return result

    payload = json.loads(raw, object_pairs_hook=pairs,
                         parse_constant=lambda _: (_ for _ in ()).throw(ValueError("invalid JSON number")))
    if (not isinstance(payload, dict) or set(payload) != {"format_version", "transactions"}
            or type(payload["format_version"]) is not int or payload["format_version"] != 1
            or not isinstance(payload["transactions"], list)):
        raise ValueError("physical input stream has an invalid envelope")
    by_key: dict[tuple[str, str], dict] = {}
    for row in payload["transactions"]:
        if (not isinstance(row, dict) or not isinstance(row.get("events"), list)
                or not isinstance(row.get("transaction_id"), str)
                or not isinstance(row.get("operation"), str)):
            raise ValueError("physical input stream transaction has an invalid shape")
        key = (row["transaction_id"], row["operation"])
        if key in by_key:
            raise ValueError("physical input stream contains duplicate transactions")
        by_key[key] = row
    expanded: list[dict] = []
    for record in selected:
        if not isinstance(record, dict):
            raise ValueError("physical input summary must be an object")
        if "physical_input_stream" not in record and isinstance(record.get("events"), list):
            expanded.append(record)
            continue
        key = (record.get("transaction_id"), record.get("operation"))
        full = by_key.get(key)
        if full is None or summarize_physical_transaction(full, stream_sha256) != record:
            raise ValueError("physical input summary differs from its retained stream")
        expanded.append(full)
    return expanded


def retain(binding: dict, capability: str, case: str, evidence_root: Path, status: str,
           observations: dict, streams: dict[str, bytes] | None = None) -> dict:
    if status not in STATUSES or not all(IDENTIFIER.fullmatch(value) for value in (capability, case)):
        raise ValueError("installed case has an invalid identity or outcome")
    source(binding["source_commit"])
    if not isinstance(observations, dict):
        raise ValueError("installed case observations must be an object")
    streams = streams or {}
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
