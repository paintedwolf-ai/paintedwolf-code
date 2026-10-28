"""Seed selection for the live command differential."""

import contextlib
import importlib.util
import io
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("conformance_live", ROOT / "conformance-live.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class ConformanceSelectionTests(unittest.TestCase):
    def test_default_and_explicit_selection(self):
        default = MODULE.selection([])
        self.assertEqual((default.seed, default.programs, default.generated_only), ([], 16, False))
        explicit = MODULE.selection(["--generated-only", "--seed", "0", "--seed", str(2**64 - 1), "--programs", "64"])
        self.assertEqual(explicit.seed, [0, 2**64 - 1])
        self.assertEqual(explicit.programs, 64)
        self.assertTrue(explicit.generated_only)

    def test_rejects_invalid_or_unbounded_selection(self):
        for args in (["--seed", "-1"], ["--seed", str(2**64)], ["--seed", "1", "--seed", "1"],
                     ["--programs", "0"], ["--programs", "65"], ["--unknown"],
                     [item for seed in range(65) for item in ("--seed", str(seed))]):
            with self.subTest(args=args), contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as caught:
                    MODULE.selection(args)
                self.assertEqual(caught.exception.code, 2)
