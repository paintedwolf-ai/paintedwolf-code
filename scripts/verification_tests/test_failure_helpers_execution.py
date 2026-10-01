"""Failure-label rewrites import the helper for the destination package."""

import importlib.util
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "rewrite-fail-messages.py"
SPEC = importlib.util.spec_from_file_location("rewrite_fail_messages", SCRIPT)
rewrite = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(rewrite)


class FailureHelperTests(unittest.TestCase):
    def test_relocated_contracts_import_shared_helper_without_self_imports(self):
        source = '''package fixture

import "os"

func fixture(t *testing.T) {
    _, err := os.ReadFile("input")
    if err != nil {
        t.Fatal(err)
    }
}
'''
        cases = [
            ("lycaon/test/contract/host/example_test.go", "contractcheck.FailErr", rewrite.CONTRACT_IMPORT),
            ("lycaon/test/contract/internal/check/example_test.go", "FailErr", None),
            ("lycaon/internal/worker/example_test.go", "testutil.FailErr", rewrite.TESTUTIL_IMPORT),
        ]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for relative, helper, imported in cases:
                with self.subTest(path=relative):
                    path = root / relative
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_text(source)
                    self.assertEqual(rewrite.rewrite_file(path, root), 1)
                    result = path.read_text()
                    self.assertIn(f'{helper}(t, "read file", err)', result)
                    if imported:
                        self.assertIn(f'"{imported}"', result)
                    else:
                        self.assertNotIn(rewrite.CONTRACT_IMPORT, result)
                    self.assertEqual(rewrite.rewrite_file(path, root), 0)

    def test_shared_contract_failure_label_can_be_refined(self):
        source = '''    _, err := os.ReadFile("input")
    contractcheck.FailErr(t, "operation failed", err)
'''
        result, count = rewrite.relabel(source, "contractcheck.FailErr")
        self.assertEqual(count, 1)
        self.assertIn('contractcheck.FailErr(t, "read file", err)', result)
