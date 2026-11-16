"""A base checkpoint is staged in the training conversation format without touching its source."""
import json
from pathlib import Path
import tempfile
import unittest

from base_checkpoint import stage
from common import inventory
from rendering import TEMPLATE, template_sha256


class BaseCheckpoint(unittest.TestCase):
    def test_stock_weights_gain_the_training_template(self):
        from tokenizers import Tokenizer, models
        from transformers import AutoTokenizer, PreTrainedTokenizerFast
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            model = root / "stock"
            tokenizer = PreTrainedTokenizerFast(
                tokenizer_object=Tokenizer(models.WordLevel({"<unk>": 0, "<bos>": 2}, unk_token="<unk>")),
                bos_token="<bos>", unk_token="<unk>")
            tokenizer.chat_template = "stock template"
            tokenizer.save_pretrained(model)
            (model / "config.json").write_text(json.dumps({"text_config": {"model_type": "gemma4_text"}}))
            (model / "model.safetensors").write_bytes(b"weights")
            before = inventory(model)
            manifest = stage(model, root / "base")
            self.assertEqual(inventory(model), before)
            self.assertEqual(manifest["source"], before)
            self.assertEqual(manifest["files"], inventory(root / "base/model"))
            self.assertEqual(manifest["rendering_profile"], "gemma4-code-v1")
            self.assertEqual(manifest["template_sha256"], template_sha256(TEMPLATE.read_text()))
            self.assertEqual(manifest["files"]["model.safetensors"], before["model.safetensors"])
            self.assertEqual(manifest["files"]["config.json"], before["config.json"])
            staged = AutoTokenizer.from_pretrained(root / "base/model", local_files_only=True)
            self.assertEqual(staged.chat_template, TEMPLATE.read_text())
            self.assertEqual(json.loads((root / "base/manifest.json").read_text()), manifest)
            with self.assertRaises(FileExistsError):
                stage(model, root / "base")


if __name__ == "__main__":
    unittest.main()
