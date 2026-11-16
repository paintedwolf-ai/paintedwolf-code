import contextlib
import io
import json
import shutil
import tempfile
import unittest
from pathlib import Path

import preload
from corpus import Corpus

CORPUS = {"schema": "pw-decide-corpus/3", "catalog_revision": "c0ffee", "state": {"head_tokens": 512},
          "questions": {"tools": {"question": "Which tools?", "independent": True, "option_words": 60,
                                  "options": {"page_open": "Open a page", "command": "Run a command"}}},
          "surfaces": [], "tools": [{"name": "command", "description": "Run a command"}, {"name": "page_open", "description": "Open a page"}]}
BLOCK = {"catalog_revision": "c0ffee", "encoding": "independent", "option_words": 60, "max_len": 1024, "head_tokens": 512,
         "options": {"command": "Run a command", "page_open": "Open a page"}}
TRAINED = {"corpus": "c0ffee", "tool_encoding": "independent", "tool_option_words": "60", "max_len": "1024", "head_max_len": "512"}


class Preload(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.root)
        (self.root / "corpus.json").write_text(json.dumps(CORPUS))
        (self.root / "rl_agent_config.json").write_text(json.dumps({"encoder": "tiny", "max_len": 1024}))

    def test_the_block_is_the_corpus_vocabulary_at_the_checkpoints_context(self):
        self.assertEqual(preload.derive(Corpus(CORPUS), preload.context(self.root)), BLOCK)
        self.assertEqual(list(preload.derive(Corpus(CORPUS), 1024)["options"]), ["command", "page_open"])
        joint = json.loads(json.dumps(CORPUS))
        del joint["questions"]["tools"]["independent"]
        self.assertEqual(preload.derive(Corpus(joint), 512)["encoding"], "joint")

    def test_a_block_that_cannot_be_served_names_its_faults(self):
        self.assertEqual(preload.faults(BLOCK), [])
        self.assertTrue(preload.faults({key: value for key, value in BLOCK.items() if key != "max_len"}))
        self.assertTrue(preload.faults(None))
        for change in ({"encoding": "pairwise"}, {"head_tokens": 0}, {"option_words": True}, {"options": {}},
                       {"options": {"command": 3}}, {"catalog_revision": ""}):
            with self.subTest(change=change):
                self.assertEqual(len(preload.faults({**BLOCK, **change})), 1)

    def test_a_head_must_have_been_trained_under_the_block(self):
        self.assertEqual(preload.head_faults(BLOCK, TRAINED), [])
        self.assertEqual(len(preload.head_faults(BLOCK, {**TRAINED, "corpus": "older", "head_max_len": "192"})), 2)
        self.assertEqual(len(preload.head_faults(BLOCK, {key: value for key, value in TRAINED.items() if key != "tool_encoding"})), 1)

    def test_the_command_prints_the_block_and_refuses_an_unservable_corpus(self):
        argv = ["--corpus", str(self.root / "corpus.json"), "--model", str(self.root)]
        with contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(preload.main(argv), 0)
        self.assertEqual(json.loads(out.getvalue()), BLOCK)
        broken = json.loads(json.dumps(CORPUS))
        broken["state"]["head_tokens"] = 0
        (self.root / "corpus.json").write_text(json.dumps(broken))
        with self.assertRaises(SystemExit) as refused, contextlib.redirect_stderr(io.StringIO()):
            preload.main(argv)
        self.assertEqual(refused.exception.code, 1)


if __name__ == "__main__":
    unittest.main()
