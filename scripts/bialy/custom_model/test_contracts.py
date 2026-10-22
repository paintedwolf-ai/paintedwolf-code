"""Offline fixture contracts; no models, providers, or external services."""
import json
import sqlite3
import unittest

from common import digest
from data import encode, assert_disjoint, check_row
from receipts import accepted_row


class Tokenizer:
    def apply_chat_template(self, messages, *, add_generation_prompt, **kwargs):
        text = json.dumps(kwargs["tools"], sort_keys=True)
        for message in messages:
            text += "<" + message["role"] + ">" + message["content"]
            if message.get("tool_calls"):
                for call in message["tool_calls"]:
                    # As Gemma 4's template does: arguments must be a mapping.
                    if not isinstance(call["function"]["arguments"], dict):
                        raise ValueError("chat_template: arguments must be a JSON object")
                text += json.dumps(message["tool_calls"], sort_keys=True)
        return text + ("<assistant>" if add_generation_prompt else "<end>")

    def __call__(self, text, **kwargs):
        return {"input_ids": list(text.encode())}


TOOLS = [{"type": "function", "function": {"name": "read", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]}}}]


class Contracts(unittest.TestCase):
    def setUp(self):
        self.db = sqlite3.connect(":memory:")
        self.db.row_factory = sqlite3.Row
        self.db.executescript("""
        CREATE TABLE model_outputs(id,session_id,message_id,scripted,settled_at,turn_attempt_id);
        INSERT INTO model_outputs VALUES('output','session','message',0,'2026-10-22T10:00:00.123456789Z','attempt');
        CREATE TABLE invocation_receipts(id,session_id,assistant_message_id,tool_call_id,tool_name,status,invoked,settled_at,args_digest,started_at);
        """)
        self.args = {"path": "file.txt"}
        self.db.execute("INSERT INTO invocation_receipts VALUES(?,?,?,?,?,?,?,?,?,?)",
                        ('receipt', 'session', 'message', 'call', 'read', 'completed', 1, 'now', digest(self.args), '2'))
        student = {"model": "student-e4b", "stream": True, "tools": TOOLS, "messages": [
            {"role": "system", "content": "Be precise"},
            {"role": "user", "content": "Read file"},
            {"role": "assistant", "content": None, "tool_calls": [{"id": "earlier", "type": "function", "function": {"name": "read", "arguments": "{\"path\":\"README.md\"}"}}]},
            {"role": "tool", "tool_call_id": "earlier", "content": "# readme"}]}
        self.record = {"schema": "pw-training-turn/1", "epoch": "legacy-1", "dispatches": 1, "settled": True,
                       "output_id": "output", "session_id": "session", "message_id": "message", "call_id": "request",
                       "student_provider": "student", "student_kind": "openai-compatible", "student_model": "student-e4b",
                       "request": {"teacher": "dispatch"}, "student_request": student,
                       "response": {"content": "", "tool_calls": [{"id": "call", "name": "read", "args": self.args}]}}
        self.identity = {"epoch": "legacy-1", "host_commit": "a" * 40, "tool_surface": "legacy",
                         "student_provider": "student", "student_kind": "openai-compatible", "student_model": "student-e4b"}
        self.commits = {}

    def tearDown(self):
        self.db.close()

    def row(self):
        return accepted_row(self.record, self.db, self.identity, "c" * 64, self.commits)

    def test_receipt_is_necessary_and_quality_is_separate(self):
        row = self.row()
        with self.assertRaisesRegex(ValueError, "quality"):
            check_row(row)
        self.db.execute("UPDATE invocation_receipts SET status='rejected'")
        with self.assertRaisesRegex(ValueError, "successful invocation"):
            self.row()

    def test_arguments_and_call_identity_must_match(self):
        self.args["path"] = "different"
        with self.assertRaisesRegex(ValueError, "successful invocation"):
            self.row()

    def test_training_input_is_the_student_render_decoded_as_its_server_does(self):
        row = self.row()
        earlier = row["prompt"][2]
        self.assertEqual(earlier["content"], "")
        self.assertEqual(earlier["tool_calls"][0]["function"]["arguments"], {"path": "README.md"})
        self.assertEqual(row["prompt"][3], {"role": "tool", "content": "# readme", "tool_call_id": "earlier"})
        self.assertEqual(row["request_capture"]["dispatch_sha256"], digest({"teacher": "dispatch"}))
        self.assertEqual(row["request_capture"]["tools"], TOOLS)

    def test_serving_kind_and_student_identity_fail_closed(self):
        self.record["student_model"] = "other"
        with self.assertRaisesRegex(ValueError, "another student"):
            self.row()
        self.record["student_model"] = "student-e4b"
        self.record["student_kind"] = self.identity["student_kind"] = "ollama"
        with self.assertRaisesRegex(ValueError, "template parity"):
            self.row()

    def test_epoch_and_unsupported_wire_fail_closed(self):
        self.record["epoch"] = "command-1"
        with self.assertRaisesRegex(ValueError, "epoch"):
            self.row()
        self.record["epoch"] = "legacy-1"
        self.record["student_request"]["images"] = []
        with self.assertRaisesRegex(ValueError, "student request shape"):
            self.row()

    def test_host_exclusion_reason_and_disabled_tools_are_reported(self):
        self.record["exclusion_reason"] = "student_cannot_be_served"
        with self.assertRaisesRegex(ValueError, "excluded: student_cannot_be_served"):
            self.row()
        del self.record["exclusion_reason"]
        self.record["student_request"]["tool_choice"] = "none"
        with self.assertRaisesRegex(ValueError, "request control"):
            self.row()

    def test_a_tool_free_reply_needs_the_hosts_commit_of_that_output(self):
        self.record["response"] = {"content": "The file says hello.", "tool_calls": []}
        with self.assertRaisesRegex(ValueError, "did not commit"):
            self.row()
        self.commits[("session", "message")] = "2026-10-22T10:00:01Z"
        row = self.row()
        self.assertEqual(row["validation"]["acceptance"], "turn_committed")
        self.db.execute("INSERT INTO model_outputs VALUES('retry','session','message',0,'2026-10-22T10:00:00.9Z','attempt')")
        with self.assertRaisesRegex(ValueError, "another output"):
            self.row()

    def command_call(self, surface):
        self.identity["tool_surface"] = surface
        self.args.clear()
        self.args["program"] = "go test ./..."
        self.record["response"]["tool_calls"][0]["name"] = "command"
        self.db.execute("UPDATE invocation_receipts SET tool_name='command', args_digest=?", (digest(self.args),))

    def children(self, receipt_id, *effects):
        self.db.executescript("CREATE TABLE IF NOT EXISTS program_operations(program_receipt_id, operation, status, effect);")
        for index, effect in enumerate(effects):
            self.db.execute("INSERT INTO program_operations VALUES(?,?,?,?)", (receipt_id, "%s-op%d" % (receipt_id, index), "completed", effect))

    def test_command_surface_requires_settled_child_effects(self):
        self.command_call("command-v1")
        with self.assertRaisesRegex(ValueError, "child receipts unavailable"):
            self.row()
        self.children("receipt")
        with self.assertRaisesRegex(ValueError, "no child operation receipts"):
            self.row()
        self.children("receipt", "completed", "none")
        self.assertEqual(self.row()["validation"]["invocation_receipt_ids"], ["receipt", "receipt-op0", "receipt-op1"])
        self.children("receipt", "unknown")
        with self.assertRaisesRegex(ValueError, "partial or unknown effects"):
            self.row()

    def test_whole_program_replay_after_partial_effects_is_excluded(self):
        self.command_call("command-v1")
        self.children("receipt", "completed")
        self.db.execute("INSERT INTO invocation_receipts VALUES(?,?,?,?,?,?,?,?,?,?)",
                        ('earlier', 'session', 'message', 'call0', 'command', 'error', 1, 'then', digest(self.args), '1'))
        self.children("earlier", "completed", "partial")
        with self.assertRaisesRegex(ValueError, "whole-program replay"):
            self.row()

    def test_legacy_surface_admits_command_receipts_without_child_facts(self):
        self.command_call("legacy")
        self.assertEqual(self.row()["validation"]["invocation_receipt_ids"], ["receipt"])

    def test_masks_prompt_and_preserves_tool_arguments(self):
        row = self.row()
        sample = encode(Tokenizer(), row, row["target"], 10000)
        target = bytes(x for x in sample["labels"] if x != -100).decode()
        self.assertIn('file.txt', target)
        self.assertNotIn('Be precise', target)
        self.assertTrue(all(x == -100 for x in sample["labels"][:20]))
        with self.assertRaisesRegex(ValueError, "oversize"):
            encode(Tokenizer(), row, row["target"], 20)

    def test_split_leakage_across_epochs(self):
        row = self.row()
        row["split_group"] = "family"
        with self.assertRaisesRegex(ValueError, "leakage"):
            assert_disjoint([row], [dict(row)])
        with self.assertRaisesRegex(ValueError, "lineage"):
            assert_disjoint([self.row()], [])


if __name__ == "__main__":
    unittest.main()
