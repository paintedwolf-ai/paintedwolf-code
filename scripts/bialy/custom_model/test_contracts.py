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
                text += json.dumps(message["tool_calls"], sort_keys=True)
        return text + ("<assistant>" if add_generation_prompt else "<end>")

    def __call__(self, text, **kwargs):
        return {"input_ids": list(text.encode())}


class Contracts(unittest.TestCase):
    def setUp(self):
        self.db = sqlite3.connect(":memory:")
        self.db.row_factory = sqlite3.Row
        self.db.executescript("""
        CREATE TABLE model_outputs(id,session_id,message_id,scripted,settled_at,turn_attempt_id);
        INSERT INTO model_outputs VALUES('output','session','message',0,'now','attempt');
        CREATE TABLE invocation_receipts(id,session_id,assistant_message_id,tool_call_id,tool_name,status,invoked,settled_at,args_digest);
        """)
        self.args = {"path":"file.txt"}
        self.db.execute("INSERT INTO invocation_receipts VALUES(?,?,?,?,?,?,?,?,?)",
                        ('receipt','session','message','call','read','completed',1,'now',digest(self.args)))
        self.record = {"schema":"pw-training-turn/1","epoch":"legacy-1","dispatches":1,"settled":True,
                       "output_id":"output","session_id":"session","message_id":"message","call_id":"request",
                       "request":{"messages":[{"role":"system","content":"Be precise"},{"role":"user","content":"Read file"}],
                                  "tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}]},
                       "response":{"content":"", "tool_calls":[{"id":"call","name":"read","args":self.args}]}}
        self.identity = {"epoch":"legacy-1","host_commit":"a"*40,"tool_surface":"legacy"}

    def tearDown(self):
        self.db.close()

    def row(self):
        return accepted_row(self.record, self.db, self.identity, "c"*64)

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

    def test_epoch_and_unsupported_wire_fail_closed(self):
        self.record["epoch"] = "command-1"
        with self.assertRaisesRegex(ValueError, "epoch"):
            self.row()
        self.record["epoch"] = "legacy-1"
        self.record["request"]["images"] = []
        with self.assertRaisesRegex(ValueError, "provider request"):
            self.row()

    def test_host_exclusion_reason_and_disabled_tools_are_reported(self):
        self.record["exclusion_reason"] = "unsupported_response"
        with self.assertRaisesRegex(ValueError, "excluded: unsupported_response"):
            self.row()
        del self.record["exclusion_reason"]
        self.record["request"]["tool_choice"] = "none"
        with self.assertRaisesRegex(ValueError, "request control"):
            self.row()

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
