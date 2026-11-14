"""The bialy decision-head scripts' mains, covered wherever this suite runs.

The changed-coverage gate measures scripts/bialy/replay_eval.py and
scripts/bialy/train.py against the suites that run on their change. This module
drives both mains over a fixture corpus with a stub laya package and a fake
engine, so the report, breakdown, and head-save lines execute without the real
checkpoint.
"""
import contextlib
import json
import os
import re
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest import mock

import torch

BIALY = Path(__file__).resolve().parents[1]

CORPUS = {
    "schema": "pw-decide-corpus/3",
    "catalog_revision": "fixture-r1",
    "surfaces": [],
    "questions": {
        "tools": {"question": "Which tools should load?", "load_at": 0.5, "option_words": 2},
        "kind": {"instructions": "Which kind of turn is this?",
                 "options": {"build": "change code", "answer": "reply only"}, "confidence_floor": 0.5},
    },
    "tools": [{"name": "command", "description": "run a shell command", "option": "run",
               "card": "Runs a shell command."}],
    "state": {"head_tokens": 512, "user_text_chars": 900},
}

FAKE_ENGINE = """#!/usr/bin/env python3
import json
import sys

for line in sys.stdin:
    req = json.loads(line)
    if req.get("method") == "hello":
        resp = {"id": req["id"], "engine": {"name": "fake-bialy", "model": "fixture", "device": "cpu"}}
    else:
        resp = {"id": req["id"], "answers": {"tools": {"probabilities": {"command": 0.9}},
                                             "kind": {"confidence": 0.9, "choice": "build"}}}
    sys.stdout.write(json.dumps(resp) + "\\n")
    sys.stdout.flush()
"""


def decide_row(host, user):
    """One pw-decide-row/2 turn whose labels match what the fake engine answers."""
    return {
        "schema": "pw-decide-row/2",
        "host": host,
        "session": "session-" + host,
        "receipt": 1,
        "surface": "chat",
        "lang": "en",
        "project": "fixture",
        "model": "fixture-model",
        "state": {"user": user},
        "partial": False,
        "engine": {"state": "abstained"},
        "offered": {"floor": [], "loadable": ["command"]},
        "labels": {"tools": ["command"], "requested_names": [], "kind": "build", "requests": []},
    }


class FixtureTokenizer:
    """A word tokenizer over a tiny vocabulary, with the offsets request_survival reads."""

    sep_token_id = 2
    pad_token_id = 0

    def __init__(self):
        self.vocab = {"<pad>": 0, "<cls>": 1, "<sep>": 2}
        self.unk = 3

    def __call__(self, text, add_special_tokens=True, return_offsets_mapping=False):
        ids, offsets = [], []
        for match in re.finditer(r"\S+", text):
            ids.append(self.vocab.get(match.group(), self.unk))
            offsets.append([match.start(), match.end()])
        out = {"ids": ids, "attention_mask": [1] * len(ids)}
        if return_offsets_mapping:
            out["offset_mapping"] = offsets
        return out


def stub_build_sequence(tok, state, question, max_len, head_max_len=None):
    """[CLS] instructions [SEP] options [SEP] state [SEP], markers pointing at the options."""
    options = list(question["crit"])
    ids = [1]
    for word in str(question.get("ins", "")).split():
        ids.append(tok.vocab.get(word, tok.unk))
    ids.append(tok.sep_token_id)
    markers = []
    for _ in options[: head_max_len or len(options)]:
        markers.append(len(ids))
        ids.append(1000 + len(markers))
    ids.append(tok.sep_token_id)
    budget = max(max_len - len(ids) - 1, 0)
    ids.extend(tok.vocab.get(word, tok.unk) for word in str(state).split()[:budget])
    return ids + [tok.sep_token_id], markers


def stub_collate_items(rows, pad):
    """The padded tensors precompute reads out of the laya collate."""
    flat = [item for group in rows for item in group]
    length = max(len(item["ids"]) for item in flat)
    width = max(len(item["markers"]) for item in flat)
    input_ids = torch.full((len(flat), length), pad, dtype=torch.long)
    attention_mask = torch.zeros((len(flat), length), dtype=torch.long)
    marker_pos = torch.zeros((len(flat), width), dtype=torch.long)
    marker_mask = torch.zeros((len(flat), width), dtype=torch.bool)
    for i, item in enumerate(flat):
        input_ids[i, : len(item["ids"])] = torch.tensor(item["ids"])
        attention_mask[i, : len(item["ids"])] = 1
        marker_pos[i, : len(item["markers"])] = torch.tensor(item["markers"])
        marker_mask[i, : len(item["markers"])] = True
    return {"input_ids": input_ids, "attention_mask": attention_mask,
            "marker_pos": marker_pos, "marker_mask": marker_mask}


class FixtureEncoder(torch.nn.Module):
    def __init__(self):
        super().__init__()
        self.embedding = torch.nn.Embedding(4096, 8)

    def forward(self, input_ids, attention_mask):
        return types.SimpleNamespace(last_hidden_state=self.embedding(input_ids))


class FixtureHead(torch.nn.Module):
    def __init__(self):
        super().__init__()
        self.layers = torch.nn.ModuleList(
            [torch.nn.TransformerEncoderLayer(d_model=8, nhead=2, dim_feedforward=16, batch_first=True)])


class FixtureModel(torch.nn.Module):
    def __init__(self):
        super().__init__()
        self.encoder = FixtureEncoder()
        self.type_emb = torch.nn.Embedding(4, 8)
        self.head = FixtureHead()
        self.scorer = torch.nn.Linear(8, 1)


class FixtureAgent:
    def __init__(self):
        self.tok = FixtureTokenizer()
        self.model = FixtureModel()
        self.cfg = {"max_len": 512}


@contextlib.contextmanager
def bialy_scripts():
    """The bialy replay_eval and train modules with a stub laya, leaving the interpreter's
    path and modules as they were (this suite imports the custom-model train)."""
    names = ("laya", "laya.common", "corpus", "rows", "headfile", "replay_eval", "train")
    saved_path = list(sys.path)
    saved_modules = {name: sys.modules.get(name) for name in names}
    for name in names:
        sys.modules.pop(name, None)
    laya = types.ModuleType("laya")
    common = types.ModuleType("laya.common")
    common.QTYPES = {"choice": 0, "multi": 1, "score": 2}
    common.build_sequence = stub_build_sequence
    common.collate_items = stub_collate_items
    laya.load = lambda model_path, device="cpu": FixtureAgent()
    laya.common = common
    sys.modules["laya"] = laya
    sys.modules["laya.common"] = common
    sys.path.insert(0, str(BIALY))
    try:
        import replay_eval  # noqa: PLC0415
        import train  # noqa: PLC0415

        yield types.SimpleNamespace(replay_eval=replay_eval, train=train)
    finally:
        sys.path[:] = saved_path
        for name, module in saved_modules.items():
            if module is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = module


@unittest.skipUnless(os.name == "posix", "the fake engine and the head lock are posix-only")
class DecideScriptsMainTest(unittest.TestCase):
    def write_corpus(self, root):
        corpus = root / "corpus.json"
        corpus.write_text(json.dumps(CORPUS), encoding="utf-8")
        return corpus

    def test_replay_eval_main_reports_overall_and_by_host(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            corpus = self.write_corpus(root)
            examples = root / "examples.jsonl"
            examples.write_text("".join(json.dumps(decide_row(host, user)) + "\n" for host, user in
                                        (("alpha", "run the tests"), ("beta", "run the build"))), encoding="utf-8")
            engine = root / "fake-bialy-engine"
            engine.write_text(FAKE_ENGINE, encoding="utf-8")
            engine.chmod(0o755)
            report_path = root / "report.json"
            argv = sys.argv
            sys.argv = ["replay_eval.py", "--corpus", str(corpus), "--examples", str(examples),
                        "--engine", str(engine), "--json", str(report_path)]
            try:
                with bialy_scripts() as scripts:
                    scripts.replay_eval.main()
            finally:
                sys.argv = argv
            report = json.loads(report_path.read_text(encoding="utf-8"))
        self.assertEqual(report["engine"]["name"], "fake-bialy")
        self.assertEqual(report["corpus"], "fixture-r1")
        self.assertEqual(report["overall"]["turns"], 2)
        self.assertEqual(sorted(report["by_host"]), ["alpha", "beta"])
        self.assertEqual(report["by_host"]["alpha"]["turns"], 1)

    def test_train_main_rejects_missing_corpus(self):
        argv = sys.argv
        sys.argv = ["train.py"]
        try:
            with bialy_scripts() as scripts:
                with self.assertRaises(SystemExit):
                    scripts.train.main()
        finally:
            sys.argv = argv

    def test_train_main_trains_and_saves_a_head(self):
        torch.set_num_threads(1)
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            corpus = self.write_corpus(root)
            train_rows = root / "train.jsonl"
            train_rows.write_text("".join(json.dumps(decide_row("alpha", "run the tests")) + "\n"
                                          for _ in range(2)), encoding="utf-8")
            val_rows = root / "val.jsonl"
            val_rows.write_text(json.dumps(decide_row("beta", "run the build")) + "\n", encoding="utf-8")
            model_dir = root / "model"
            model_dir.mkdir()
            (model_dir / "rl_agent_config.json").write_text(json.dumps({"encoder": "fixture-encoder"}), encoding="utf-8")
            out = root / "heads" / "turn-load.safetensors"
            argv = sys.argv
            sys.argv = ["train.py", "--corpus", str(corpus), "--train", str(train_rows), "--val", str(val_rows),
                        "--out", str(out), "--model", str(model_dir), "--epochs", "1", "--batch-size", "2"]
            try:
                with mock.patch.dict(os.environ, {"LYCAON_DECIDE_DEVICE": "cpu"}), bialy_scripts() as scripts:
                    scripts.train.main()
                    meta, tensors = scripts.train.headfile.read_metadata(out)
            finally:
                sys.argv = argv
            self.assertEqual(meta["backbone"], "fixture-encoder")
            self.assertEqual(meta["corpus"], "fixture-r1")
            self.assertIn("type_emb.weight", tensors)
            self.assertIn("scorer.weight", tensors)


if __name__ == "__main__":
    unittest.main()
