"""Student renderer selection and ordered training request contracts."""
import json
from pathlib import Path
import tempfile
import types
import unittest

from data import encode
from rendering import TEMPLATE, configure_training_renderer, ordered_messages
from test_contracts import Tokenizer


class RenderingTests(unittest.TestCase):
    def test_every_gemma4_tier_trains_in_the_served_format(self):
        tiers = {"e4b": {"model_type": "gemma4_text", "hidden_size": 2560, "num_hidden_layers": 42, "num_kv_shared_layers": 18},
                 "12b": {"model_type": "gemma4_unified_text", "hidden_size": 3840, "num_kv_shared_layers": 0},
                 "26b-a4b": {"model_type": "gemma4_text", "hidden_size": 2816, "enable_moe_block": True}}
        with tempfile.TemporaryDirectory() as temporary:
            model = Path(temporary)
            for tier, text in tiers.items():
                with self.subTest(tier=tier):
                    (model / "config.json").write_text(json.dumps({"text_config": text}))
                    tokenizer = types.SimpleNamespace(chat_template="checkpoint", init_kwargs={})
                    self.assertEqual(configure_training_renderer(tokenizer, model), "gemma4-code-v1")
                    self.assertEqual(tokenizer.chat_template, TEMPLATE.read_text())
                    self.assertEqual(tokenizer.init_kwargs["paintedwolf_conversation_format"], "gemma4-code-v1")
            (model / "config.json").write_text(json.dumps({"model_type": "llama"}))
            tokenizer = types.SimpleNamespace(chat_template="different family", init_kwargs={})
            self.assertEqual(configure_training_renderer(tokenizer, model), "checkpoint")
            self.assertEqual(tokenizer.chat_template, "different family")

    def test_ordered_messages_name_results_by_their_call(self):
        call = {"role": "assistant", "tool_calls": [{"id": "c1", "function": {"name": "read"}}]}
        result = {"role": "tool", "tool_call_id": "c1", "content": "source"}
        history = [call, {"role": "user", "content": "wait"}, result]
        named = ordered_messages(history)
        self.assertEqual([item["role"] for item in named], ["assistant", "user", "tool"])
        self.assertEqual(named[2]["name"], "read")
        self.assertNotIn("name", result)
        self.assertEqual(ordered_messages([dict(result, name="read")])[0]["name"], "read")
        for invalid in [[result], [call, call], [call, dict(result, name="write")]]:
            with self.assertRaises(ValueError):
                ordered_messages(invalid)

    def test_encoding_names_results_without_changing_the_capture(self):
        class CaptureTokenizer(Tokenizer):
            init_kwargs = {"paintedwolf_conversation_format": "gemma4-code-v1"}
            seen = []

            def apply_chat_template(self, messages, **kwargs):
                self.seen.append(messages)
                return super().apply_chat_template(messages, **kwargs)

        tokenizer = CaptureTokenizer()
        prompt = [
            {"role": "assistant", "content": "", "tool_calls": [{"id": "c1", "function": {"name": "read", "arguments": {}}}]},
            {"role": "user", "content": "wait"},
            {"role": "tool", "tool_call_id": "c1", "content": "source"},
        ]
        row = {"prompt": prompt, "request_capture": {"tools": [], "controls": {"reasoning_effort": "none"}}}
        sample = encode(tokenizer, row, {"text": "Done.", "tool_calls": []}, 4096)
        self.assertTrue(sample["input_ids"])
        for messages in tokenizer.seen:
            self.assertEqual(messages[2]["name"], "read")
            self.assertEqual(messages[1], prompt[1])
        self.assertNotIn("name", prompt[2])
