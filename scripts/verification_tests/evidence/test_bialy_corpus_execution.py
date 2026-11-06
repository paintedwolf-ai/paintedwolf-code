"""Admission contracts for offline decision training; no model dependencies."""

import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]


def load_module(name):
    spec = importlib.util.spec_from_file_location("bialy_" + name, ROOT / "scripts/bialy" / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


corpus_module = load_module("corpus")
rows = load_module("rows")


def fixture():
    data = {
        "schema": corpus_module.SCHEMA, "catalog_revision": "fixture",
        "questions": {"tools": {"question": "tools?", "independent": True,
                                 "options": {"command": "Run", "capture_page": "Capture"}},
                      "guides": {"question": "guides?"}},
        "surfaces": [{"id": "worker", "floor": ["command"], "loadable": ["capture_page"], "guides": ["evidence"]}],
        "tools": [{"name": n, "description": n, "card": n} for n in ["command", "capture_page"]],
        "units": [{"id": "evidence", "option": "Evidence"}],
        "skills": [{"name": "verify", "card": "Verify"}],
    }
    row = {
        "schema": rows.SCHEMA,
        "offered": {"floor": ["command"], "loadable": ["capture_page"], "guides": ["evidence"]},
        "labels": {"tools": ["capture_page"], "guides": {"evidence": True}, "skills": ["verify"],
                   "tool_scores": {"capture_page": 4}, "skill_scores": {"verify": 3},
                   "requested_names": ["typo", "capture_page"],
                   "requests": [{"need": "preview", "exact": ["typo"], "after": ["capture_page"], "scores": {"capture_page": 4}}],
                   "second_scores": {"tools": {"capture_page": 3}, "skills": {"verify": 4},
                                     "requests": [{"capture_page": 3}, None]}},
    }
    return data, row


class BialyCorpusAdmissionTests(unittest.TestCase):
    def setUp(self):
        self.data, self.row = fixture()
        self.corpus = corpus_module.Corpus(self.data)

    def test_current_rows_preserve_unknown_requested_names_as_observations(self):
        original = copy.deepcopy(self.row)
        self.corpus.validate_row(self.row)
        questions = self.corpus.row_questions(self.row)
        self.assertEqual(questions["tools"]["options"], {"capture_page": "Capture"})
        self.assertEqual(questions["guides"]["options"], {"evidence": "Evidence"})
        self.assertEqual(rows.tool_targets(self.row), {"capture_page": 1})
        self.assertEqual(self.row, original)

    def test_unversioned_old_and_unknown_corpora_are_refused(self):
        for version in [None, "pw-decide-corpus/1", "pw-decide-corpus/3"]:
            with self.subTest(version=version):
                self.data["schema"] = version
                with self.assertRaisesRegex(ValueError, "corpus schema"):
                    corpus_module.Corpus(self.data)

    def test_frozen_preload_options_cannot_leak_into_current_roster(self):
        self.data["questions"]["tools"]["options"]["edit"] = "Edit"
        with self.assertRaisesRegex(ValueError, "callable tool roster"):
            corpus_module.Corpus(self.data)

    def test_unknown_candidates_are_never_silently_dropped(self):
        for kind, candidate in [("tool", "edit"), ("guide", "retired-guide")]:
            with self.subTest(kind=kind), self.assertRaisesRegex(ValueError, candidate):
                self.corpus.multi_question(kind, [candidate])

    def test_offered_and_label_vocabularies_are_checked(self):
        cases = [
            ("offered", "floor"), ("offered", "loadable"), ("offered", "guides"),
            ("labels", "tools"), ("labels", "tool_scores"), ("labels", "guides"),
            ("labels", "skills"), ("labels", "skill_scores"),
            ("labels", "second_scores", "tools"), ("labels", "second_scores", "skills"),
            ("labels", "requests", 0, "after"), ("labels", "requests", 0, "scores"),
            ("labels", "second_scores", "requests", 0),
        ]
        for path in cases:
            row = copy.deepcopy(self.row)
            parent = row
            for key in path[:-1]:
                parent = parent[key]
            parent[path[-1]] = {"retired": 1} if isinstance(parent[path[-1]], dict) else ["retired"]
            with self.subTest(path=path), self.assertRaisesRegex(ValueError, "retired"):
                self.corpus.validate_row(row)

    def test_row_file_reports_line_and_preserves_historical_bytes(self):
        self.row["offered"]["loadable"] = ["edit"]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "rows.jsonl"
            content = "\n" + json.dumps(self.row) + "\n"
            path.write_text(content)
            with self.assertRaisesRegex(ValueError, "rows.jsonl:2: offered.loadable"):
                rows.load(path, corpus=self.corpus)
            self.assertEqual(path.read_text(), content)
            self.assertEqual(rows.load(path), [self.row])

    def test_retention_rows_keep_their_separate_contract(self):
        row = {"schema": rows.RETENTION_SCHEMA, "candidates": []}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "retention.jsonl"
            path.write_text(json.dumps(row) + "\n")
            self.assertEqual(rows.load(path, rows.RETENTION_SCHEMA, corpus=self.corpus), [row])
            with self.assertRaisesRegex(ValueError, "schema"):
                rows.load(path, corpus=self.corpus)
