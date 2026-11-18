"""Failure-label rewrites import the helper for the destination package."""

import contextlib
import importlib.util
import io
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[2] / "rewrite-fail-messages.py"
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

    def test_check_refuses_a_bare_failure_in_any_form(self):
        forms = {
            "block": "if err != nil {\n\t\tt.Fatal(err)\n\t}",
            "multi-line condition": "if err := os.WriteFile(\"input\",\n\t\tnil, 0o600); err != nil {\n\t\tt.Fatal(err)\n\t}",
            "multi-statement body": "if err != nil {\n\t\tcleanup()\n\t\tt.Fatal(err)\n\t}",
            "fixture receiver": "if err != nil {\n\t\tfx.t.Fatal(err)\n\t}",
            "benchmark receiver": "if err != nil {\n\t\tb.Fatal(err)\n\t}",
            "single line": "if err != nil { t.Fatal(err) }",
            "inside an expression": "defer func() { t.Fatal( err ) }()",
        }
        for name, statement in forms.items():
            with self.subTest(form=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                path = root / "lycaon/internal/worker/example_test.go"
                path.parent.mkdir(parents=True)
                path.write_text(f"package worker\n\nfunc fixture() {{\n\terr := run()\n\t{statement}\n}}\n")
                self.assertEqual(self.check(root), 1)

    def test_check_accepts_labeled_failures_and_other_fatal_calls(self):
        accepted = [
            'testutil.FailErr(t, "run", err)',
            't.Fatalf("run: %v", err)',
            "log.Fatal(err)",
            "// t.Fatal(err) is refused.",
        ]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "lycaon/internal/worker/example_test.go"
            path.parent.mkdir(parents=True)
            path.write_text("package worker\n\nfunc fixture() {\n\t" + "\n\t".join(accepted) + "\n}\n")
            fixture = root / "lycaon/test/testdata/corpus/example.go"
            fixture.parent.mkdir(parents=True)
            fixture.write_text("package corpus\n\nfunc fixture() {\n\tt.Fatal(err)\n}\n")
            self.assertEqual(self.check(root), 0)

    def test_rewrite_labels_bare_failures_from_the_enclosing_check(self):
        sources = {
            "multi-statement body on a fixture receiver": (
                "\terr := os.WriteFile(path, nil, 0o600)\n\tif err != nil {\n\t\tcleanup()\n\t\tfx.t.Fatal(err)\n\t}\n",
                '\t\ttestutil.FailErr(fx.t, "write file", err)\n',
            ),
            "condition spanning lines": (
                "\tif _, err := queue.Enqueue(ctx, Job{\n\t\tID: id,\n\t}); err != nil {\n\t\tb.Fatal(err)\n\t}\n",
                '\t\ttestutil.FailErr(b, "queue.Enqueue failed", err)\n',
            ),
            "arguments spanning lines": (
                "\tif err := queue.Append(ctx, id,\n\t\tJob{ID: id},\n\t); err != nil {\n\t\tt.Fatal(err)\n\t}\n",
                '\t\ttestutil.FailErr(t, "queue.Append failed", err)\n',
            ),
        }
        for name, (source, labeled) in sources.items():
            with self.subTest(form=name):
                result, count = rewrite.transform(source, "testutil.FailErr")
                self.assertEqual(count, 1)
                self.assertIn(labeled, result)
                self.assertEqual(rewrite.bare_failures(result), [])

    def test_rewrite_leaves_a_failure_that_is_not_a_plain_err_check(self):
        sources = {
            "no err check": "\tswitch {\n\tcase failed:\n\t\tt.Fatal(err)\n\t}\n",
            "assertion on the error": "\tif _, err := reader.Read(buffer); !errors.Is(err, io.EOF) {\n\t\tt.Fatal(err)\n\t}\n",
            "assertion spanning lines": "\tif err := run(Options{\n\t\tID: id,\n\t}); !errors.Is(err, refused) {\n\t\tt.Fatal(err)\n\t}\n",
        }
        for name, source in sources.items():
            with self.subTest(form=name):
                result, count = rewrite.transform(source, "testutil.FailErr")
                self.assertEqual((result, count), (source, 0))
                self.assertEqual(len(rewrite.bare_failures(result)), 1)

    @staticmethod
    def check(root):
        with contextlib.redirect_stdout(io.StringIO()):
            return rewrite.main(["--check", "--root", str(root)])
