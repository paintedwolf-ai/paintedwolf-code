import argparse
import contextlib
import io
import hashlib
import json
import shutil
import struct
import tempfile
import unittest
from pathlib import Path

import adopt_heads

THRESHOLDS = {"turn.tools.load_at": 0.55, "turn.skills.preload_at": 2.6, "turn.skills.margin": 0.3,
              "request.load_at": 2.1, "request.max_loads": 3, "request.nearest_loads": 1,
              "lookup.read_at": 2.8, "lookup.margin": 0.3,
              "tool_event.read_at": 2.8, "tool_event.pointer_at": 2.4, "tool_event.margin": 0.3}


def head(path, label):
    header = json.dumps({"__metadata__": {"format": "pw-decide-head/1", "label": label}}).encode()
    path.write_bytes(struct.pack("<Q", len(header)) + header + label.encode())
    return hashlib.sha256(path.read_bytes()).hexdigest()


def arguments(**changes):
    values = {"release": "campaign-1", "heads_revision": "a" * 40, "dataset_tag": "campaign-1-data",
              "dataset_revision": "b" * 40, "factory_tag": "heads-campaign-1", "factory_commit": "c" * 40}
    values.update(changes)
    return argparse.Namespace(**values)


class Adoption(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.root)
        self.host = self.root / "host"
        shutil.copytree(adopt_heads.HOST, self.host, ignore=lambda directory, names: [
            name for name in names if name not in ("decisions.yaml", "decision-release.json")])
        self.heads = self.root / "heads"
        self.heads.mkdir()
        self.policy = {"decision_contract": 3, "thresholds": THRESHOLDS,
                       "heads": {name: {"sha256": head(self.heads / (name + ".safetensors"), "p0-" + name)} for name in adopt_heads.HEADS}}

    def test_the_policys_pins_and_thresholds_land_exactly_and_comments_survive(self):
        before = (self.host / "decisions.yaml").read_text()
        adopt_heads.adopt(self.policy, self.heads, arguments(), self.host)
        release = json.loads((self.host / "decision-release.json").read_text())
        self.assertEqual(release["release"], "campaign-1")
        self.assertEqual(release["source"]["heads"]["revision"], "a" * 40)
        self.assertEqual(release["heads"]["unit-rank"], {"sha256": self.policy["heads"]["unit-rank"]["sha256"], "label": "p0-unit-rank"})
        after = (self.host / "decisions.yaml").read_text()
        self.assertIn("    load_at: 0.55", after)
        self.assertIn("  max_loads: 3", after)
        self.assertEqual([line for line in before.split("\n") if line.lstrip().startswith("#")],
                         [line for line in after.split("\n") if line.lstrip().startswith("#")])

    def test_a_head_file_unlike_the_policy_is_refused(self):
        head(self.heads / "code-rank.safetensors", "retrained")
        with self.assertRaises(adopt_heads.AdoptionError):
            adopt_heads.adopt(self.policy, self.heads, arguments(), self.host)

    def test_another_contract_a_missing_threshold_or_a_short_revision_is_refused(self):
        with self.assertRaises(adopt_heads.AdoptionError):
            adopt_heads.adopt(dict(self.policy, decision_contract=2), self.heads, arguments(), self.host)
        with self.assertRaises(adopt_heads.AdoptionError):
            adopt_heads.adopt(dict(self.policy, thresholds={**THRESHOLDS, "turn.tools.unload_at": 1}), self.heads, arguments(), self.host)
        with self.assertRaises(adopt_heads.AdoptionError):
            adopt_heads.adopt(self.policy, self.heads, arguments(heads_revision="main"), self.host)

    def test_the_command_reports_the_adoption(self):
        policy = self.root / "p0.json"
        policy.write_text(json.dumps(self.policy))
        adopt_heads.HOST, original = self.host, adopt_heads.HOST
        self.addCleanup(setattr, adopt_heads, "HOST", original)
        argv = ["--policy", str(policy), "--heads", str(self.heads), "--release", "campaign-1", "--heads-revision", "a" * 40,
                "--dataset-tag", "d", "--dataset-revision", "b" * 40, "--factory-tag", "f", "--factory-commit", "c" * 40]
        with self.assertRaises(SystemExit) as refused:
            adopt_heads.main([*argv[:-1], "short"])
        self.assertEqual(refused.exception.code, 1)
        with contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(adopt_heads.main(argv), 0)
        self.assertEqual(json.loads(out.getvalue())["release"], "campaign-1")


if __name__ == "__main__":
    unittest.main()
