"""Encoding admission drops whole oversize rows and preserves split authority."""
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from prepare import prepare
from test_admission import fixture_source
from test_contracts import Tokenizer


def row(group, text="question"):
    return {"schema": "pw-custom-sft/1", "split_group": group,
            "prompt": [{"role": "system", "content": "host"}, {"role": "user", "content": group + text}],
            "request_capture": {"sha256": group, "tools": [], "controls": {}},
            "target": {"role": "assistant", "text": "answer", "tool_calls": []}}


class Preparation(unittest.TestCase):
    def fixture(self, root, train):
        values = {"train": train, "validation": [row("validation")], "holdout": [row("holdout")]}
        paths = {key: root / (key + ".jsonl") for key in values}
        for name, path in paths.items():
            path.write_text("".join(json.dumps(value) + "\n" for value in values[name]))
        key = fixture_source(root, paths)
        environment = patch.dict(os.environ, {"PW_TRAINING_SOURCE_KEY": str(key)})
        environment.start()
        self.addCleanup(environment.stop)
        return paths

    def read(self, path, partition):
        # Receipt and quality authority are covered by load_split contract tests.
        return [json.loads(line) for line in path.read_text().splitlines()]

    def test_drops_complete_oversize_rows_without_changing_the_kept_row(self):
        with tempfile.TemporaryDirectory() as directory, patch("prepare.load_split", self.read):
            root = Path(directory)
            kept = row("train")
            paths = self.fixture(root, [kept, row("large", "x" * 2000)])
            report = prepare(paths, root / "out", Tokenizer(), 500, {"fixture": True})
            self.assertTrue(report["ready"])
            self.assertEqual(len(report["excluded"]), 1)
            excluded = report["excluded"][0]
            self.assertEqual({key: value for key, value in excluded.items() if key != "tokens"},
                             {"split": "train", "request_sha256": "large", "reason": "oversize", "limit": 500})
            self.assertGreater(excluded["tokens"], 500)
            self.assertEqual(json.loads((root / "out/train.jsonl").read_text()), kept)

    def test_empty_fitting_split_is_not_ready(self):
        with tempfile.TemporaryDirectory() as directory, patch("prepare.load_split", self.read):
            root = Path(directory)
            report = prepare(self.fixture(root, [row("large", "x" * 2000)]), root / "out", Tokenizer(), 500, {})
            self.assertFalse(report["ready"])
            self.assertEqual(report["splits"]["train"]["kept_rows"], 0)

    def test_leakage_is_rejected_before_oversize_filtering(self):
        with tempfile.TemporaryDirectory() as directory, patch("prepare.load_split", self.read):
            root = Path(directory)
            paths = self.fixture(root, [row("holdout", "x" * 2000)])
            with self.assertRaisesRegex(ValueError, "leakage"):
                prepare(paths, root / "out", Tokenizer(), 500, {})

    def test_other_encoding_failures_are_not_treated_as_oversize(self):
        with tempfile.TemporaryDirectory() as directory, patch("prepare.load_split", self.read):
            root = Path(directory)
            value = row("train")
            value["request_capture"]["controls"] = {"reasoning_effort": "high"}
            with self.assertRaisesRegex(ValueError, "model-specific renderer"):
                prepare(self.fixture(root, [value]), root / "out", Tokenizer(), 500, {})

    def test_known_template_mismatch_excludes_the_whole_row(self):
        class MismatchTokenizer(Tokenizer):
            def apply_chat_template(self, messages, *, add_generation_prompt, **kwargs):
                text = super().apply_chat_template(messages, add_generation_prompt=add_generation_prompt, **kwargs)
                if not add_generation_prompt and any(m["content"].startswith("mismatch") for m in messages):
                    return "changed previous turn" + text
                return text
        with tempfile.TemporaryDirectory() as directory, patch("prepare.load_split", self.read):
            root = Path(directory)
            report = prepare(self.fixture(root, [row("train"), row("mismatch")]), root / "out", MismatchTokenizer(), 500, {})
            self.assertTrue(report["ready"])
            self.assertEqual(report["excluded"], [{"split": "train", "request_sha256": "mismatch", "reason": "generation_prefix_mismatch"}])
            self.assertEqual(json.loads((root / "out/train.jsonl").read_text()), row("train"))

    def test_changed_checkpoint_cannot_publish_a_ready_manifest(self):
        with tempfile.TemporaryDirectory() as directory, patch("prepare.load_split", self.read):
            root = Path(directory)
            with self.assertRaisesRegex(ValueError, "checkpoint changed"):
                prepare(self.fixture(root, [row("train")]), root / "out", Tokenizer(), 500, {}, lambda: False)
            self.assertFalse((root / "out/manifest.json").exists())
