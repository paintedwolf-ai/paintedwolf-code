"""One host episode bundle, with every file and blob checked against its manifest."""
from datetime import datetime
import hashlib
import json
from pathlib import Path

from contracts import check

SCHEMA = "pw-training-episode-bundle/1"
LINES = {"sessions.jsonl": "session", "captures.jsonl": "capture", "operations.jsonl": "operation",
         "observations.jsonl": "observation", "effects.jsonl": "effect", "decisions.jsonl": "decision",
         "completions.jsonl": "completion"}


def _sha256(raw):
    return hashlib.sha256(raw).hexdigest()


def instant(value):
    """Go RFC 3339 timestamps carry up to nanoseconds; Python parses microseconds."""
    head, _, rest = value.rstrip("Z").partition(".")
    fraction = (rest + "000000")[:6] if rest else "000000"
    return datetime.fromisoformat(head + "." + fraction + "+00:00")


class Bundle:
    def __init__(self, root):
        self.root = Path(root)
        raw = (self.root / "manifest.json").read_bytes()
        self.manifest_sha256 = _sha256(raw)
        self.manifest = json.loads(raw)
        check("episode-bundle", self.manifest)
        for name, expected in self.manifest["files"].items():
            if _sha256((self.root / name).read_bytes()) != expected:
                raise ValueError("bundle file changed: " + name)
        catalog_raw = (self.root / "catalog.json").read_bytes()
        self.catalog = json.loads(catalog_raw)
        self.contract_sha256 = _sha256(catalog_raw)
        check("training-contract", self.catalog)
        if self.catalog["host_commit"] != self.manifest["host_commit"]:
            raise ValueError("bundle catalog is from another host build")
        self.lines = {}
        for name, definition in LINES.items():
            values = [json.loads(line) for line in (self.root / name).read_text().splitlines() if line.strip()]
            for value in values:
                check("episode-bundle", value, definition)
            self.lines[definition] = values
        self.sessions = {item["id"]: item for item in self.lines["session"]}
        self.completions = {item["id"]: item for item in self.lines["completion"]}
        self.commits = {(item["session_id"], item["message_id"]): item.get("output_id", "") for item in self.lines["decision"]
                        if item["kind"] == "turn_committed"}
        self.calls, self.children = {}, {}
        for operation in self.lines["operation"]:
            if operation.get("parent_id"):
                self.children.setdefault(operation["parent_id"], []).append(operation)
            elif operation.get("tool_call_id"):
                self.calls[(operation["session_id"], operation["tool_call_id"])] = operation

    def blob(self, name):
        if name not in self.manifest["blobs"]:
            raise ValueError("blob outside the bundle manifest")
        raw = (self.root / "blobs" / name).read_bytes()
        if _sha256(raw) != name:
            raise ValueError("bundle blob changed")
        return raw

    def body(self, record, field):
        """A capture body moved to a blob; None when the record carries none."""
        name = record.get(field + "_blob")
        return None if name is None else json.loads(self.blob(name))

    @property
    def captures(self):
        return self.lines["capture"]

    def earlier(self, operation):
        """Top-level operations of the same session started before this one."""
        started = instant(operation["started_at"])
        return [other for other in self.calls.values() if other["session_id"] == operation["session_id"]
                and instant(other["started_at"]) < started]
