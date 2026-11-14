"""Item construction for the turn heads, with the model libraries stubbed: it builds the
questions and targets the trainer feeds them, and needs no weights."""
import random
import sys
import types
import unittest


def stub_model_libraries():
    torch = types.ModuleType("torch")
    torch.nn = types.ModuleType("torch.nn")
    torch.nn.functional = types.ModuleType("torch.nn.functional")
    torch.utils = types.ModuleType("torch.utils")
    torch.utils.data = types.ModuleType("torch.utils.data")
    torch.utils.data.DataLoader = torch.utils.data.Dataset = object
    laya = types.ModuleType("laya")
    laya.common = types.ModuleType("laya.common")
    laya.common.QTYPES = {"choice": 0, "multi": 1, "score": 2}
    laya.common.build_sequence = lambda tok, state, question, max_len, head_max_len=None: (
        [0] + [10 + n for n in range(len(question["crit"]))] + [2, 9, 2], list(range(1, len(question["crit"]) + 1)))
    laya.common.collate_items = None
    modules = {"torch": torch, "torch.nn": torch.nn, "torch.nn.functional": torch.nn.functional, "torch.utils": torch.utils,
               "torch.utils.data": torch.utils.data, "laya": laya, "laya.common": laya.common}
    for name, module in modules.items():
        sys.modules.setdefault(name, module)


stub_model_libraries()
import train  # noqa: E402
from corpus import SCHEMA, Corpus  # noqa: E402


class Tokenizer:
    sep_token_id = 2


def corpus():
    return Corpus({"schema": SCHEMA, "catalog_revision": "r1", "surfaces": [],
                   "questions": {"tools": {"question": "Which tools?", "load_at": 0.5},
                                 "kind": {"instructions": "Which kind?", "options": {"build": "b", "answer": "a"}}},
                   "tools": [{"name": "command", "description": "run", "option": "run"},
                             {"name": "edit", "description": "edit", "option": "edit"}]})


ROW = {"host": "h", "state": {"user": ""}, "partial": False, "offered": {"loadable": ["edit", "command"]},
       "labels": {"tools": ["command"], "requested_names": [], "kind": "build", "requests": []}}


class Items(unittest.TestCase):
    def test_turn_heads_train_tools_and_kind_and_select_on_tools(self):
        self.assertEqual(train.FAMILIES, ("tools", "kind", "skills", "requests"))
        self.assertEqual(train.SELECT, ("tools", "retention"))

    def test_a_turn_trains_one_tools_item_over_the_sorted_options_and_one_kind_item(self):
        items = train.build_items([ROW], corpus(), random.Random(7), Tokenizer(), 512, 0, ("tools", "kind"), False, 0, "all",
                                  {}, "called", {"skills": False, "requests": False})
        self.assertEqual([item["family"] for item in items], ["tools", "kind"])
        self.assertEqual(items[0]["target"], [1.0, 0.0])
        self.assertEqual(items[1]["label"], ["answer", "build"].index("build"))


if __name__ == "__main__":
    unittest.main()
