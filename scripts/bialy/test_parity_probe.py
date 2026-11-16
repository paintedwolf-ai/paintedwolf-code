"""What the parity probe encodes on the trainer's side, with the model libraries stubbed."""
import contextlib
import sys
import types
import unittest
from unittest import mock

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


class Tensor:
    """Stands in for a tensor: indexing and device moves return it, and it lists its values."""

    def __init__(self, values=()):
        self.values = list(values)

    def __getitem__(self, index):
        if index is None:
            return self
        return Tensor(self.values[index]) if isinstance(index, slice) else self.values[index]

    def to(self, device):
        return self

    def cpu(self):
        return self

    def tolist(self):
        return self.values


class TrainerProbabilities(unittest.TestCase):
    def probabilities(self, independent):
        agent = types.SimpleNamespace(cfg={"max_len": 1024}, tok=TokenizerStub())
        features = {key: Tensor() for key in ("h", "att", "marker_pos", "marker_mask")}
        calls = []

        def forward(model, *inputs):
            calls.append(inputs)
            return Tensor([Tensor([0.25, 0.75][: 1 if independent else 2])])
        torch = types.SimpleNamespace(no_grad=contextlib.nullcontext, tensor=lambda values, device=None: Tensor(values),
                                      sigmoid=lambda logits: logits)
        with mock.patch.object(parity_probe, "torch", torch), mock.patch.object(parity_probe, "forward_head", forward), \
                mock.patch.object(parity_probe, "precompute", lambda agent, items, device: [features for _ in items]):
            return parity_probe.torch_probs(agent, None, corpus(independent), ROW, "cpu"), len(calls)

    def test_each_option_of_an_independent_head_gets_its_own_forward(self):
        self.assertEqual(self.probabilities(True), ({"command": 0.25, "grep": 0.25}, 2))

    def test_a_joint_head_answers_every_option_in_one_forward(self):
        self.assertEqual(self.probabilities(False), ({"command": 0.25, "grep": 0.75}, 1))


if __name__ == "__main__":
    unittest.main()
