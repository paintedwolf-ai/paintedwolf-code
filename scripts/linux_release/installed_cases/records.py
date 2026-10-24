"""Retain the exact observations behind one installed case outcome."""
from __future__ import annotations

import copy
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
SUMMARY_FIELDS = frozenset({"event_count", "events_sha256", "physical_input_stream", "physical_input_sha256"})


def _is_physical_transaction(value: object) -> bool:
    return (isinstance(value, dict) and isinstance(value.get("events"), list)
            and isinstance(value.get("transaction_id"), str) and isinstance(value.get("operation"), str))


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
    detailed: list[dict] = []
    seen_by_key: dict[tuple[str, str], dict] = {}

    def collect(record: dict) -> None:
        key = (record["transaction_id"], record["operation"])
        if key in seen_by_key:
            if seen_by_key[key] != record:
                raise ValueError("conflicting physical input transactions share identity")
            return
        seen_by_key[key] = record
        detailed.append(record)

    for launch_key in ("launch", "launch_before", "launch_after"):
        launch = observations.get(launch_key)
        if isinstance(launch, dict) and isinstance(launch.get("fixed_input"), list):
            for item in launch["fixed_input"]:
                if _is_physical_transaction(item):
                    collect(item)
    for editor_key in ("editor_keyboard", "editor_search_keyboard"):
        editor = observations.get(editor_key)
        if isinstance(editor, dict) and _is_physical_transaction(editor.get("physical_vm_keyboard")):
            collect(editor["physical_vm_keyboard"])
    if _is_physical_transaction(observations.get("lock_transition")):
        collect(observations["lock_transition"])
    if isinstance(observations.get("transitions"), list):
        for item in observations["transitions"]:
            if _is_physical_transaction(item):
                collect(item)
    if _is_physical_transaction(observations.get("native_input")):
        collect(observations["native_input"])
    if not detailed:
        return observations, streams
    payload = {"format_version": 1, "transactions": detailed}
    payload_raw = (json.dumps(payload, sort_keys=True, separators=(",", ":")) + "\n").encode()
    stream_sha256 = hashlib.sha256(payload_raw).hexdigest()
    compacted = dict(observations)
    for launch_key in ("launch", "launch_before", "launch_after"):
        launch = compacted.get(launch_key)
        if isinstance(launch, dict) and isinstance(launch.get("fixed_input"), list):
            compacted[launch_key] = {
                **launch,
                "fixed_input": [
                    summarize_physical_transaction(item, stream_sha256) if _is_physical_transaction(item) else item
                    for item in launch["fixed_input"]
                ],
            }
    for editor_key in ("editor_keyboard", "editor_search_keyboard"):
        editor = compacted.get(editor_key)
        if isinstance(editor, dict) and _is_physical_transaction(editor.get("physical_vm_keyboard")):
            compacted[editor_key] = {
                **editor,
                "physical_vm_keyboard": summarize_physical_transaction(editor["physical_vm_keyboard"], stream_sha256),
            }
    if _is_physical_transaction(compacted.get("lock_transition")):
        compacted["lock_transition"] = summarize_physical_transaction(compacted["lock_transition"], stream_sha256)
    if isinstance(compacted.get("transitions"), list):
        compacted["transitions"] = [
            summarize_physical_transaction(item, stream_sha256) if _is_physical_transaction(item) else item
            for item in compacted["transitions"]
        ]
    if _is_physical_transaction(compacted.get("native_input")):
        compacted["native_input"] = summarize_physical_transaction(compacted["native_input"], stream_sha256)
    return compacted, {**streams, PHYSICAL_INPUT_STREAM: payload_raw}


def expand_physical_transactions(leaf: Path | None, selected: list[dict], *, require_all: bool = True) -> list[dict]:
    if not isinstance(selected, list):
        return selected
    has_summary = any(isinstance(row, dict) and bool(SUMMARY_FIELDS & set(row)) for row in selected)
    stream_path = leaf.with_name(PHYSICAL_INPUT_STREAM + ".bin") if leaf is not None else None
    if not has_summary:
        if require_all and stream_path is not None and stream_path.exists():
            raise ValueError("unreferenced physical input stream evidence is forbidden")
        return selected
    if stream_path is None or not stream_path.exists():
        raise ValueError("physical input stream evidence is missing")
    size = stream_path.stat().st_size
    if not 0 < size <= MAX_STREAM_BYTES:
        raise ValueError("physical input stream exceeds its bound")
    raw = stream_path.read_bytes()
    stream_sha256 = hashlib.sha256(raw).hexdigest()
    payload = json.loads(raw.decode("utf-8"))
    if (not isinstance(payload, dict) or set(payload) != {"format_version", "transactions"}
            or type(payload["format_version"]) is not int or payload["format_version"] != 1
            or not isinstance(payload["transactions"], list) or not payload["transactions"]):
        raise ValueError("physical input stream has an invalid shape")
    by_key: dict[tuple[str, str], dict] = {}
    for row in payload["transactions"]:
        if not _is_physical_transaction(row):
            raise ValueError("physical input stream transaction has an invalid shape")
        key = (row["transaction_id"], row["operation"])
        if key in by_key:
            raise ValueError("physical input stream contains duplicate transactions")
        by_key[key] = row
    expanded: list[dict] = []
    seen_keys: set[tuple[str, str]] = set()
    for record in selected:
        if not isinstance(record, dict) or "events" in record:
            raise ValueError("physical input summary record has an invalid shape")
        key = (record.get("transaction_id"), record.get("operation"))
        full = by_key.get(key)
        if full is None or summarize_physical_transaction(full, stream_sha256) != record:
            raise ValueError("physical input summary differs from its retained stream transaction")
        seen_keys.add(key)
        expanded.append(copy.deepcopy(full))
    if require_all and seen_keys != set(by_key):
        raise ValueError("physical input stream transactions differ from execution summary")
    return expanded


def retain(binding: dict, capability: str, case: str, evidence_root: Path, status: str,
           observations: dict, streams: dict[str, bytes] | None = None) -> dict:
    if status not in STATUSES or not all(IDENTIFIER.fullmatch(value) for value in (capability, case)):
        raise ValueError("installed case has an invalid identity or outcome")
    source(binding["source_commit"])
    if not isinstance(observations, dict):
        raise ValueError("installed case observations must be an object")
    observations, streams = compact_physical_input(observations, dict(streams or {}))
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
