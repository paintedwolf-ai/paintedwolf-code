"""Offline fixture contracts; no models, providers, or external services."""
import json
from pathlib import Path
import tempfile
import unittest

from bundle import Bundle
from common import digest
from contracts import check
from data import encode, assert_disjoint, check_row
from receipts import candidate
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

    def build(self):
        self.written += 1
        bundle = Bundle(write_bundle(Path(self.temporary.name) / str(self.written), [self.record], self.operations,
                                     self.completions, self.decisions))
        if self.identity["contract_sha256"] is None:
            self.identity["contract_sha256"] = bundle.contract_sha256
        return candidate(bundle.captures[0], bundle, self.identity, "c" * 64)

    def row(self, candidate_row=None, **admission):
        built = candidate_row or self.build()
        quality = {"verdict": "pass", "method": "deterministic", "evidence_ids": ["receipt"],
                   "request_sha256": built["request_capture"]["sha256"], "response_sha256": digest(built["target"])}
        return {**built, "schema": "pw-training-row/1", "split_group": "family",
                "source": {"bundle_sha256": "d" * 64, "capture_sha256": "e" * 64}, "quality": quality,
                "admission": {"eligibility": "eligible", "category": "ordinary", "semantic_outcome": "success", **admission}}

    def test_student_thinking_must_be_explicitly_disabled(self):
        for effort in (None, "minimal", "low", "high"):
            with self.subTest(effort=effort):
                self.record["student_request"]["reasoning_effort"] = effort
                with self.assertRaisesRegex(ValueError, "request control"):
                    self.build()

    def test_a_candidate_states_each_calls_operation_facts_without_judging_them(self):
        facts = self.build()["execution"]["calls"][0]
        self.assertEqual((facts["recorded"], facts["offered"], facts["semantic_outcome"], facts["effect_state"]),
                         (True, True, "success", "known"))
        self.assertEqual(facts["operation_ids"], ["receipt"])
        self.operations[0].update(semantic_outcome="failure", failure_code="EXIT_STATUS")
        facts = self.build()["execution"]["calls"][0]
        self.assertEqual((facts["semantic_outcome"], facts["failure_code"]), ("failure", "EXIT_STATUS"))
        self.operations.append(operation("receipt-op0", parent="receipt", tool="cat", effects="unknown", settlement="unsettled"))
        facts = self.build()["execution"]["calls"][0]
        self.assertEqual((facts["settlement"], facts["effect_state"], facts["operation_ids"]), ("unsettled", "unknown", ["receipt", "receipt-op0"]))

    def test_an_unrecorded_or_mismatched_call_is_a_fact_not_an_error(self):
        for field, value in (("args_sha256", digest({"path": "different"})), ("tool", "write"), ("message_id", "other")):
            with self.subTest(field=field):
                original = self.operations[0][field]
                self.operations[0][field] = value
                self.assertFalse(self.build()["execution"]["calls"][0]["recorded"])
                self.operations[0][field] = original
        self.record["response"]["tool_calls"][0]["name"] = "write"
        facts = self.build()["execution"]["calls"][0]
        self.assertEqual((facts["offered"], facts["recorded"]), (False, False))

    def test_a_replay_after_unknown_effects_is_flagged(self):
        self.operations[0]["started_at"] = "2026-10-22T10:00:01Z"
        self.operations.append(operation("earlier", call="call0", args_sha256=digest(self.args), outcome="failure",
                                         started="2026-10-22T10:00:00.5Z"))
        self.operations.append(operation("earlier-op0", parent="earlier", tool="cat", effects="unknown"))
        self.assertTrue(self.build()["execution"]["calls"][0]["replays_unknown_effects"])
        self.operations[2]["effect_state"] = "known"
        self.assertFalse(self.build()["execution"]["calls"][0]["replays_unknown_effects"])

    def test_training_input_is_the_student_render_decoded_as_its_server_does(self):
        built = self.build()
        earlier = built["prompt"][2]
        self.assertEqual(earlier["content"], "")
        self.assertEqual(earlier["tool_calls"][0]["function"]["arguments"], {"path": "README.md"})
        self.assertEqual(built["prompt"][3], {"role": "tool", "content": "# readme", "tool_call_id": "earlier"})
        self.assertEqual(built["request_capture"]["dispatch_sha256"], digest({"teacher": "dispatch"}))
        self.assertEqual(built["request_capture"]["tools"], TOOLS)

    def test_identity_and_unbuildable_captures_raise(self):
        self.record["student_model"] = "other"
        with self.assertRaisesRegex(ValueError, "another student"):
            self.build()
        self.record["student_model"] = "student-e4b"
        self.record["student_kind"] = self.identity["student_kind"] = "ollama"
        with self.assertRaisesRegex(ValueError, "template parity"):
            self.build()
        self.record["student_kind"] = self.identity["student_kind"] = "openai-compatible"
        self.identity["contract_sha256"] = "f" * 64
        with self.assertRaisesRegex(ValueError, "another host contract"):
            self.build()

    def test_epoch_unsupported_wire_missing_bodies_and_scripted_output_raise(self):
        self.record["epoch"] = "fixture-2"
        with self.assertRaisesRegex(ValueError, "epoch"):
            self.build()
        self.record["epoch"] = "fixture-1"
        self.completions[0]["scripted"] = True
        with self.assertRaisesRegex(ValueError, "settled model output"):
            self.build()
        self.completions[0]["scripted"] = False
        self.record["student_request"]["images"] = []
        with self.assertRaisesRegex(ValueError, "student request shape"):
            self.build()
        del self.record["student_request"]
        with self.assertRaisesRegex(ValueError, "missing captured request"):
            self.build()

    def test_a_host_excluded_capture_becomes_a_stub_with_its_reason(self):
        self.record["exclusion_reason"] = "host_modified_response"
        self.assertEqual(self.build(), {"schema": "pw-training-candidate/1", "session": "session", "message_id": "message",
                                        "call_id": "request", "host_exclusion": "host_modified_response"})
        del self.record["exclusion_reason"]
        self.record["dispatches"] = 2
        self.assertEqual(self.build()["host_exclusion"], "unsettled_or_ambiguous_dispatch")

    def test_a_tool_free_reply_is_committed_only_when_the_host_committed_that_output(self):
        self.record["response"] = {"content": "The file says hello.", "tool_calls": []}
        self.assertFalse(self.build()["execution"]["committed"])
        commit = {"kind": "turn_committed", "session_id": "session", "message_id": "message", "decided_at": "2026-10-22T10:00:01Z"}
        self.decisions.append(dict(commit, output_id="output"))
        self.assertTrue(self.build()["execution"]["committed"])
        self.decisions[0] = dict(commit, output_id="retry")
        self.assertFalse(self.build()["execution"]["committed"])

    def test_training_admits_only_eligible_reviewed_rows(self):
        row = self.row()
        check_row(row)
        check("training-candidate", {**self.build(), "source": row["source"]})
        for broken in ({"admission": dict(row["admission"], eligibility="excluded")},
                       {"quality": dict(row["quality"], verdict="fail")},
                       {"quality": dict(row["quality"], response_sha256="0" * 64)},
                       {"response_sha256": "0" * 64}):
            with self.subTest(broken=broken):
                with self.assertRaises(ValueError):
                    check_row({**row, **broken})
        with self.assertRaisesRegex(ValueError, "unknown training schema"):
            check_row({**row, "schema": "pw-custom-sft/1"})

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
        with self.assertRaisesRegex(ValueError, "leakage"):
            assert_disjoint([row], [dict(row)])
        with self.assertRaisesRegex(ValueError, "lineage"):
            assert_disjoint([dict(row, split_group="")], [])


if __name__ == "__main__":
    unittest.main()
