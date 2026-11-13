"""Episode bundles are read only when every file and blob matches the manifest."""
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from bundle import Bundle, instant
from contracts import check

CONTRACT = {"schema": "pw-training-contract/1", "host_commit": "a" * 40, "host_modified": False,
            "bundle": "pw-training-episode-bundle/1", "capture": "pw-training-turn/1", "fact": "pw-training-fact/1",
            "files": ["catalog.json", "sessions.jsonl", "captures.jsonl", "operations.jsonl", "observations.jsonl",
                      "effects.jsonl", "decisions.jsonl", "completions.jsonl", "source-inventory.json"],
            "sources": [{"source": "command-program", "contract": "pw-operation-program/1"},
                        {"source": "invocation-receipt", "contract": "pw-operation-receipt/1"}],
            "settlement": ["settled", "unsettled"], "semantic_outcome": ["success", "failure", "unknown"],
            "effect_state": ["none", "known", "unknown"], "offer_sources": ["standing", "predicted", "requested"]}
BODIES = ("request", "student_request", "response")


def operation(identifier, call="", parent="", tool="read", args_sha256="", outcome="success", effects="known",
              settlement="settled", started="2026-10-22T10:00:00Z", message="message"):
    value = {"id": identifier, "session_id": "session", "message_id": message, "tool": tool, "args_sha256": args_sha256,
             "source": "invocation-receipt", "contract": "pw-operation-receipt/1", "invoked": True, "settlement": settlement,
             "semantic_outcome": outcome, "effect_state": effects, "started_at": started}
    if call:
        value["tool_call_id"] = call
    if parent:
        value["parent_id"] = parent
    return value


def write_bundle(root, captures=(), operations=(), completions=(), decisions=(), contract=None):
    """Write a bundle as the host does: bodies and transcripts in blobs, every file bound."""
    root = Path(root)
    (root / "blobs").mkdir(parents=True)
    blobs = []

    def blob(value):
        raw = json.dumps(value).encode()
        name = hashlib.sha256(raw).hexdigest()
        (root / "blobs" / name).write_bytes(raw)
        blobs.append(name)
        return name

    lines = {"sessions.jsonl": [{"id": "session", "project_id": "project", "messages_blob": blob([])}],
             "captures.jsonl": [], "operations.jsonl": list(operations), "observations.jsonl": [],
             "effects.jsonl": [], "decisions.jsonl": list(decisions), "completions.jsonl": []}
    for record in captures:
        record = dict(record)
        for field in BODIES:
            if field in record:
                record[field + "_blob"] = blob(record.pop(field))
        lines["captures.jsonl"].append(record)
    for completion in completions:
        lines["completions.jsonl"].append(dict(completion, body_blob=blob({"content": "", "tool_calls": []})))
    files = {}
    for name, values in lines.items():
        (root / name).write_text("".join(json.dumps(value) + "\n" for value in values))
    (root / "catalog.json").write_text(json.dumps(contract or CONTRACT))
    (root / "source-inventory.json").write_text(json.dumps({"delivered_paths": [], "snapshots": {}, "promotions": {}}))
    for name in CONTRACT["files"]:
        files[name] = hashlib.sha256((root / name).read_bytes()).hexdigest()
    manifest = {"schema": "pw-training-episode-bundle/1", "host_commit": "a" * 40, "root_session_id": "session",
                "project_id": "project", "store_sha256": "b" * 64, "files": files, "blobs": sorted(set(blobs))}
    (root / "manifest.json").write_text(json.dumps(manifest))
    return root


class BundleIntegrity(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        capture = {"schema": "pw-training-turn/1", "epoch": "e", "session_id": "session", "message_id": "message",
                   "output_id": "output", "call_id": "call", "dispatches": 1, "settled": True,
                   "student_provider": "p", "student_model": "m", "response": {"content": "hi"}}
        self.root = write_bundle(Path(self.temporary.name) / "bundle", captures=[capture],
                                 operations=[operation("r1", call="c1"), operation("e1", parent="r1")],
                                 completions=[{"id": "output", "session_id": "session", "message_id": "message",
                                               "turn_attempt_id": "a", "scripted": False, "settled_at": "2026-10-22T10:00:00Z"}],
                                 decisions=[{"kind": "turn_committed", "session_id": "session", "message_id": "message",
                                             "output_id": "output", "decided_at": "2026-10-22T10:00:01Z"}])

    def test_a_complete_bundle_indexes_calls_children_outputs_and_commits(self):
        bundle = Bundle(self.root)
        self.assertEqual(bundle.calls[("session", "c1")]["id"], "r1")
        self.assertEqual([child["id"] for child in bundle.children["r1"]], ["e1"])
        self.assertEqual(bundle.commits[("session", "message")], "output")
        self.assertEqual(bundle.body(bundle.captures[0], "response"), {"content": "hi"})
        self.assertIsNone(bundle.body(bundle.captures[0], "request"))
        self.assertEqual(bundle.contract_sha256, hashlib.sha256((self.root / "catalog.json").read_bytes()).hexdigest())

    def test_a_changed_file_blob_or_unlisted_blob_is_refused(self):
        bundle = Bundle(self.root)
        blob = self.root / "blobs" / bundle.captures[0]["response_blob"]
        blob.write_text('{"content": "changed"}')
        with self.assertRaisesRegex(ValueError, "bundle blob changed"):
            bundle.body(bundle.captures[0], "response")
        with self.assertRaisesRegex(ValueError, "outside the bundle manifest"):
            bundle.blob("f" * 64)
        with (self.root / "operations.jsonl").open("a") as stream:
            stream.write("{}\n")
        with self.assertRaisesRegex(ValueError, "bundle file changed: operations.jsonl"):
            Bundle(self.root)

    def test_records_that_break_the_contract_are_refused(self):
        root = write_bundle(Path(self.temporary.name) / "inline", captures=[
            {"schema": "pw-training-turn/1", "session_id": "session", "request": {}}])
        manifest = json.loads((root / "manifest.json").read_text())
        records = [json.loads(line) for line in (root / "captures.jsonl").read_text().splitlines()]
        records[0]["request"] = {"inline": True}
        (root / "captures.jsonl").write_text(json.dumps(records[0]) + "\n")
        manifest["files"]["captures.jsonl"] = hashlib.sha256((root / "captures.jsonl").read_bytes()).hexdigest()
        (root / "manifest.json").write_text(json.dumps(manifest))
        with self.assertRaisesRegex(ValueError, "capture contract violated"):
            Bundle(root)
        other = write_bundle(Path(self.temporary.name) / "foreign", contract=dict(CONTRACT, host_commit="b" * 40))
        with self.assertRaisesRegex(ValueError, "another host build"):
            Bundle(other)
        broken = write_bundle(Path(self.temporary.name) / "broken", contract=dict(CONTRACT, effect_state=["none"]))
        with self.assertRaisesRegex(ValueError, "training-contract contract violated"):
            Bundle(broken)

    def test_go_timestamps_order_by_instant_not_text(self):
        self.assertLess(instant("2026-10-22T10:00:00Z"), instant("2026-10-22T10:00:00.5Z"))
        self.assertEqual(instant("2026-10-22T10:00:00.123456789Z").microsecond, 123456)

    def test_check_names_the_contract_and_keeps_the_cause(self):
        check("training-contract", CONTRACT)
        with self.assertRaisesRegex(ValueError, "training-contract contract violated") as raised:
            check("training-contract", dict(CONTRACT, schema="other"))
        self.assertIsInstance(raised.exception.__cause__, ValueError)


if __name__ == "__main__":
    unittest.main()
