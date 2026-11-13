"""Offline fixture contracts; no models, providers, or external services."""
import json
from pathlib import Path
import tempfile
import unittest

from bundle import Bundle
from common import digest
from contracts import check
from data import encode, assert_disjoint, check_row
from receipts import accepted_row
from test_bundle import operation, write_bundle


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
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.args = {"path": "file.txt"}
        student = {"model": "student-e4b", "reasoning_effort": "none", "stream": True, "tools": TOOLS, "messages": [
            {"role": "system", "content": "Be precise"},
            {"role": "user", "content": "Read file"},
            {"role": "assistant", "content": None, "tool_calls": [{"id": "earlier", "type": "function", "function": {"name": "read", "arguments": "{\"path\":\"README.md\"}"}}]},
            {"role": "tool", "tool_call_id": "earlier", "content": "# readme"}]}
        self.record = {"schema": "pw-training-turn/1", "epoch": "fixture-1", "dispatches": 1, "settled": True,
                       "output_id": "output", "session_id": "session", "message_id": "message", "call_id": "request",
                       "student_provider": "student", "student_kind": "openai-compatible", "student_model": "student-e4b",
                       "request": {"teacher": "dispatch"}, "student_request": student,
                       "response": {"content": "", "tool_calls": [{"id": "call", "name": "read", "args": self.args}]}}
        self.identity = {"epoch": "fixture-1", "host_commit": "a" * 40, "contract_sha256": None,
                         "student_provider": "student", "student_kind": "openai-compatible", "student_model": "student-e4b"}
        self.operations = [operation("receipt", call="call", args_sha256=digest(self.args))]
        self.completions = [{"id": "output", "session_id": "session", "message_id": "message", "turn_attempt_id": "attempt",
                             "scripted": False, "settled_at": "2026-10-22T10:00:00.123456789Z"}]
        self.decisions = []
        self.written = 0

    def row(self):
        self.written += 1
        bundle = Bundle(write_bundle(Path(self.temporary.name) / str(self.written), [self.record], self.operations,
                                     self.completions, self.decisions))
        if self.identity["contract_sha256"] is None:
            self.identity["contract_sha256"] = bundle.contract_sha256
        return accepted_row(bundle.captures[0], bundle, self.identity, "c" * 64)

    def test_student_thinking_must_be_explicitly_disabled(self):
        for effort in (None, "minimal", "low", "high"):
            with self.subTest(effort=effort):
                self.record["student_request"]["reasoning_effort"] = effort
                with self.assertRaisesRegex(ValueError, "request control"):
                    self.row()

    def test_an_operation_is_necessary_and_quality_is_separate(self):
        row = self.row()
        check("sft-row", dict(row, source={"bundle_sha256": "d" * 64, "capture_sha256": "e" * 64}))
        with self.assertRaisesRegex(ValueError, "quality"):
            check_row(row)
        self.operations[0]["semantic_outcome"] = "failure"
        with self.assertRaisesRegex(ValueError, "did not succeed"):
            self.row()
        self.operations.clear()
        with self.assertRaisesRegex(ValueError, "missing exact invocation operation"):
            self.row()

    def test_model_review_does_not_require_a_different_family(self):
        row = self.row()
        row['validation']['quality'] = {
            'method': 'model_judge', 'verdict': 'pass', 'judge_model': 'reviewer', 'judge_revision': 'pinned',
            'judge_family': 'gemma', 'teacher_family': 'gemma', 'evidence_ids': ['receipt'],
            'request_sha256': row['request_capture']['sha256'], 'response_sha256': digest(row['target']),
        }
        check_row(row)
        del row['validation']['quality']['judge_revision']
        with self.assertRaisesRegex(ValueError, 'judge identity missing'):
            check_row(row)

    def test_arguments_tool_and_message_must_match(self):
        for field, value in (("args_sha256", digest({"path": "different"})), ("tool", "write"), ("message_id", "other")):
            with self.subTest(field=field):
                original = self.operations[0][field]
                self.operations[0][field] = value
                with self.assertRaisesRegex(ValueError, "missing exact invocation operation"):
                    self.row()
                self.operations[0][field] = original

    def test_training_input_is_the_student_render_decoded_as_its_server_does(self):
        row = self.row()
        earlier = row["prompt"][2]
        self.assertEqual(earlier["content"], "")
        self.assertEqual(earlier["tool_calls"][0]["function"]["arguments"], {"path": "README.md"})
        self.assertEqual(row["prompt"][3], {"role": "tool", "content": "# readme", "tool_call_id": "earlier"})
        self.assertEqual(row["request_capture"]["dispatch_sha256"], digest({"teacher": "dispatch"}))
        self.assertEqual(row["request_capture"]["tools"], TOOLS)
        self.assertEqual(row["validation"]["operation_ids"], ["receipt"])

    def test_serving_kind_student_and_contract_identity_fail_closed(self):
        self.record["student_model"] = "other"
        with self.assertRaisesRegex(ValueError, "another student"):
            self.row()
        self.record["student_model"] = "student-e4b"
        self.record["student_kind"] = self.identity["student_kind"] = "ollama"
        with self.assertRaisesRegex(ValueError, "template parity"):
            self.row()
        self.identity["contract_sha256"] = "f" * 64
        with self.assertRaisesRegex(ValueError, "another host contract"):
            self.row()

    def test_epoch_unsupported_wire_and_missing_bodies_fail_closed(self):
        self.record["epoch"] = "fixture-2"
        with self.assertRaisesRegex(ValueError, "epoch"):
            self.row()
        self.record["epoch"] = "fixture-1"
        self.record["student_request"]["images"] = []
        with self.assertRaisesRegex(ValueError, "student request shape"):
            self.row()
        del self.record["student_request"]
        with self.assertRaisesRegex(ValueError, "missing captured request"):
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
        commit = {"kind": "turn_committed", "session_id": "session", "message_id": "message", "decided_at": "2026-10-22T10:00:01Z"}
        self.decisions.append(dict(commit, output_id="output"))
        row = self.row()
        self.assertEqual(row["validation"]["acceptance"], "turn_committed")
        self.decisions[0] = dict(commit, output_id="retry")
        with self.assertRaisesRegex(ValueError, "did not commit"):
            self.row()

    def test_a_scripted_or_unsettled_output_is_not_a_target(self):
        self.completions[0]["scripted"] = True
        with self.assertRaisesRegex(ValueError, "settled model output"):
            self.row()

    def test_every_child_must_settle_succeed_and_leave_known_effects(self):
        self.operations.append(operation("receipt-op0", parent="receipt", tool="cat"))
        self.assertEqual(self.row()["validation"]["operation_ids"], ["receipt", "receipt-op0"])
        for field, value, reason in (("settlement", "unsettled", "did not settle"), ("effect_state", "unknown", "unknown effects"),
                                     ("semantic_outcome", "failure", "did not succeed")):
            with self.subTest(field=field):
                self.operations[1][field] = value
                with self.assertRaisesRegex(ValueError, reason):
                    self.row()
                self.operations[1] = operation("receipt-op0", parent="receipt", tool="cat")

    def test_a_replay_after_unknown_effects_is_excluded(self):
        self.operations[0]["started_at"] = "2026-10-22T10:00:01Z"
        self.operations.append(operation("earlier", call="call0", args_sha256=digest(self.args), outcome="failure",
                                         started="2026-10-22T10:00:00.5Z"))
        self.operations.append(operation("earlier-op0", parent="earlier", tool="cat", effects="unknown"))
        with self.assertRaisesRegex(ValueError, "replay after unknown effects"):
            self.row()
        self.operations[2]["effect_state"] = "known"
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
