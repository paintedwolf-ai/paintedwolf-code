"""Retain the exact observations behind one installed case outcome."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path

from ..installed_evidence import IDENTIFIER, source

class CaseUnavailable(ValueError):
    """A required native interaction cannot run in the selected guest state."""


STATUSES = {"passed", "failed", "unverified"}


def retain(binding: dict, capability: str, case: str, evidence_root: Path, status: str,
           observations: dict, streams: dict[str, bytes] | None = None) -> dict:
    if status not in STATUSES or not all(IDENTIFIER.fullmatch(value) for value in (capability, case)):
        raise ValueError("installed case has an invalid identity or outcome")
    source(binding["source_commit"])
    if not isinstance(observations, dict):
        raise ValueError("installed case observations must be an object")
    streams = streams or {}
    if any(not IDENTIFIER.fullmatch(name) or not isinstance(raw, bytes) or len(raw) > 8 << 20
           for name, raw in streams.items()):
        raise ValueError("installed case evidence stream exceeds its contract")
    directory = evidence_root / capability / case
    directory.mkdir(parents=True, exist_ok=False)
    records = []
    for name, raw in streams.items():
        path = directory / (name + ".bin")
        with path.open("xb") as stream:
            stream.write(raw)
        records.append({"path": path.relative_to(evidence_root).as_posix(),
                        "sha256": hashlib.sha256(raw).hexdigest()})
    document = {"format_version": 1, "source_commit": binding["source_commit"],
                "artifact": binding["artifact"], "capability": capability,
                "case": case, "status": status, "observations": observations}
    raw = (json.dumps(document, indent=2, sort_keys=True) + "\n").encode()
    path = directory / "execution.json"
    with path.open("xb") as stream:
        stream.write(raw)
    records.append({"path": path.relative_to(evidence_root).as_posix(),
                    "sha256": hashlib.sha256(raw).hexdigest()})
    return {"id": case, "status": status, "evidence": records}
