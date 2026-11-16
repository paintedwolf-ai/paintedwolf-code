import ast
import json
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
CONTRACT = json.loads((HERE / "factory-contract.json").read_text())
# A script's parser may take arguments a sibling module declares for it.
SOURCES = {"rerank/train_rerank.py": ("rerank/train_rerank.py", "rerank/partition.py")}


def declared(script):
    """Every flag the script's argument parser declares, read from its source."""
    flags = set()
    for source in SOURCES.get(script, (script,)):
        for node in ast.walk(ast.parse((HERE / source).read_text())):
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) and node.func.attr == "add_argument":
                flags.update(arg.value for arg in node.args if isinstance(arg, ast.Constant) and str(arg.value).startswith("--"))
    return flags


class FactoryContract(unittest.TestCase):
    def test_the_contract_lists_exactly_the_flags_each_script_accepts(self):
        self.assertEqual(CONTRACT["schema"], "pw-factory-cli/1")
        for script, flags in CONTRACT["scripts"].items():
            with self.subTest(script=script):
                self.assertEqual(sorted(flags), sorted(declared(script)))
                self.assertEqual(flags, sorted(flags))

    def test_every_script_the_factory_runs_is_in_the_contract(self):
        self.assertEqual(sorted(CONTRACT["scripts"]), ["calibrate.py", "parity_probe.py", "preload.py", "replay_eval.py",
                                                       "rerank/train_rerank.py", "retention_eval.py", "train.py"])


if __name__ == "__main__":
    unittest.main()
