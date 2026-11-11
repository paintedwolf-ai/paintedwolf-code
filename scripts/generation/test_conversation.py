"""Training and native rendering share ordered, lossless conversation fixtures."""
import json
from pathlib import Path
import tempfile
import unittest

from jinja2 import Environment


ROOT = Path(__file__).resolve().parents[2]
TEMPLATES = ROOT / "lycaon/internal/generation/templates"


class ConversationTests(unittest.TestCase):
    def test_ordered_conversations(self):
        environment = Environment()
        def reject(message):
            raise ValueError(message)
        environment.globals["raise_exception"] = reject
        template = environment.from_string((TEMPLATES / "gemma4-code-v1.jinja").read_text())
        for case in json.loads((TEMPLATES / "gemma4-code-v1.cases.json").read_text()):
            with self.subTest(case=case["name"]):
                if case.get("rejected"):
                    with self.assertRaises(ValueError):
                        template.render(**case, bos_token="<bos>")
                else:
                    self.assertEqual(template.render(**case, bos_token="<bos>"), case["expected"])

    def test_exported_tokenizer_retains_format_and_training_prefix(self):
        from tokenizers import Tokenizer, models
        from transformers import AutoTokenizer, PreTrainedTokenizerFast
        tokenizer = PreTrainedTokenizerFast(
            tokenizer_object=Tokenizer(models.WordLevel({"<unk>": 0, "word": 1, "<bos>": 2}, unk_token="<unk>")),
            bos_token="<bos>", unk_token="<unk>")
        tokenizer.chat_template = (TEMPLATES / "gemma4-code-v1.jinja").read_text()
        tokenizer.init_kwargs["paintedwolf_conversation_format"] = "gemma4-code-v1"
        with tempfile.TemporaryDirectory() as temporary:
            tokenizer.save_pretrained(temporary)
            restored = AutoTokenizer.from_pretrained(temporary, local_files_only=True)
            self.assertEqual(restored.chat_template, tokenizer.chat_template)
            self.assertEqual(restored.init_kwargs["paintedwolf_conversation_format"], "gemma4-code-v1")
            cases = json.loads((TEMPLATES / "gemma4-code-v1.cases.json").read_text())
            for case in cases:
                if case.get("rejected") or not case["add_generation_prompt"]:
                    continue
                with self.subTest(case=case["name"]):
                    prompt = restored.apply_chat_template(case["messages"], tokenize=False, add_generation_prompt=True)
                    full = restored.apply_chat_template(case["messages"] + [{"role": "assistant", "content": "Answer."}],
                                                        tokenize=False, add_generation_prompt=False)
                    self.assertTrue(full.startswith(prompt))
                    self.assertEqual(full[len(prompt):], "Answer.<turn|>\n")
