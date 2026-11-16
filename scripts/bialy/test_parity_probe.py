"""What the parity probe encodes on the trainer's side, with the model libraries stubbed."""
import sys
import types
import unittest

import test_train_items  # noqa: F401 - stubs torch and laya before the trainer is imported

safetensors = types.ModuleType("safetensors")
safetensors.torch = types.ModuleType("safetensors.torch")
safetensors.torch.load_file = None
sys.modules.setdefault("safetensors", safetensors)
sys.modules.setdefault("safetensors.torch", safetensors.torch)

import parity_probe  # noqa: E402
from corpus import SCHEMA, Corpus  # noqa: E402


def corpus(independent):
    tools = {"question": "Which tools?", "option_words": 60, "options": {"command": "Run a command", "edit": "Edit a file", "grep": "Search"}}
    if independent:
        tools["independent"] = True
    return Corpus({"schema": SCHEMA, "catalog_revision": "c0ffee", "state": {"head_tokens": 512},
                   "questions": {"tools": tools, "kind": {"instructions": "Kind?", "options": {}}}, "surfaces": [],
                   "tools": [{"name": name, "description": text} for name, text in tools["options"].items()]})


ROW = {"host": "coordinator", "state": {"user": "run the tests", "host": "coordinator"}, "offered": {"floor": [], "loadable": ["grep", "command"]}}


class TokenizerStub:
    sep_token_id = 2

    def __call__(self, text, **kwargs):
        return {"offset_mapping": [(0, len(text))]}


class ProbeItems(unittest.TestCase):
    def test_an_independent_head_is_probed_one_option_per_item_as_it_was_trained(self):
        rows, items = parity_probe.probe_items(TokenizerStub(), corpus(True), ROW, 1024, 512)
        self.assertEqual(rows, [["command"], ["grep"]])
        self.assertEqual([len(item["markers"]) for item in items], [1, 1])

    def test_a_joint_head_is_probed_with_every_option_in_one_item(self):
        rows, items = parity_probe.probe_items(TokenizerStub(), corpus(False), ROW, 1024, 512)
        self.assertEqual(rows, [["command", "grep"]])
        self.assertEqual(len(items[0]["markers"]), 2)


if __name__ == "__main__":
    unittest.main()
