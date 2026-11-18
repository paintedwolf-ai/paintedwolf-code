"""Student renderer selection and ordered training request contracts."""
import hashlib
import json
from pathlib import Path
import tempfile
import types
import unittest

from jinja2 import TemplateError

from data import EncodingExclusion, encode
from rendering import CASES, TEMPLATE, configure_training_renderer, template_sha256
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

    def test_template_identity_is_the_digest_of_its_file(self):
        self.assertEqual(template_sha256(TEMPLATE.read_text()), hashlib.sha256(TEMPLATE.read_bytes()).hexdigest())

    def test_served_fixtures_carry_no_tool_names(self):
        served = [case for case in json.loads(CASES.read_text()) if case.get("wire")]
        self.assertTrue(any(message["role"] == "tool" for case in served for message in case["messages"]))
        for case in served:
            self.assertTrue(case["add_generation_prompt"])
            self.assertFalse(any("name" in message for message in case["messages"]))

    def test_a_conversation_the_template_refuses_is_excluded_whole(self):
        class Refusing(Tokenizer):
            def apply_chat_template(self, messages, **kwargs):
                raise TemplateError("tool result requires its tool name or its call")

        row = {"prompt": [{"role": "tool", "tool_call_id": "gone", "content": "source"}],
               "request_capture": {"tools": [], "controls": {"reasoning_effort": "none"}}}
        with self.assertRaises(EncodingExclusion) as refused:
            encode(Refusing(), row, {"text": "Done.", "tool_calls": []}, 4096)
        self.assertEqual(refused.exception.reason, "template_rejected")
