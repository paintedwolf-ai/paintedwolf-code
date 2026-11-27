"""Tests for deterministic seeds, tree generator, change runner, and query generators."""

from __future__ import annotations

from pathlib import Path
import sys
import tempfile
import unittest

_BENCHMARK_DIR = str(Path(__file__).resolve().parents[2])
if _BENCHMARK_DIR not in sys.path:
    sys.path.insert(0, _BENCHMARK_DIR)

from catalog_feed_search.change_runner import (
    CHANGE_OP_KINDS,
    ChangeOp,
    ChangeRunner,
    ChangeScript,
    generate_change_script,
)
from catalog_feed_search.generators import (
    TreeGenOptions,
    TreeSpec,
    generate_tree_spec,
    load_repo_boundary_config,
    materialize_tree,
)
from catalog_feed_search.queries import (
    S01_QUERY_FORMS,
    S05_LANGUAGES,
    S06_TEXT_KINDS,
    CodeQuerySpec,
    generate_replace_spec,
    generate_s01_query,
    generate_s04_lookup,
    generate_s05_corpus,
    generate_s06_query_text,
)
from catalog_feed_search.seeds import (
    BASE_SEED_PRIMARY,
    BASE_SEED_SECONDARY,
    ReplaySelector,
    derive_seed,
)

REPO_ROOT = Path(__file__).resolve().parents[4]


class SeedAndTreeGeneratorTests(unittest.TestCase):
    def test_seed_derivation_is_deterministic_and_distinct(self) -> None:
        s1 = derive_seed(BASE_SEED_PRIMARY, "K01", 0)
        s2 = derive_seed(BASE_SEED_PRIMARY, "K01", 0)
        s3 = derive_seed(BASE_SEED_SECONDARY, "K01", 0)
        s4 = derive_seed(BASE_SEED_PRIMARY, "K01", 1)
        self.assertEqual(s1, s2)
        self.assertNotEqual(s1, s3)
        self.assertNotEqual(s1, s4)

        sel = ReplaySelector(BASE_SEED_PRIMARY, "S01", 42)
        self.assertEqual(sel.cli_args(), ["--seed", "20261127", "--family", "S01", "--instance", "42"])
        self.assertEqual(ReplaySelector.from_dict(sel.to_dict()), sel)

        with self.assertRaises(ValueError):
            derive_seed("", "K01", 0)
        with self.assertRaises(ValueError):
            derive_seed(BASE_SEED_PRIMARY, "K01", -1)

    def test_load_repo_boundary_config(self) -> None:
        cfg = load_repo_boundary_config(REPO_ROOT)
        self.assertTrue(cfg.is_boundary_dir("node_modules"))
        self.assertTrue(cfg.is_boundary_dir("packages/web/node_modules"))
        self.assertTrue(cfg.is_boundary_dir(".git"))
        self.assertTrue(cfg.is_boundary_dir("app/.yarn/cache"))
        self.assertTrue(cfg.is_boundary_dir("pkg/foo.egg-info"))
        self.assertTrue(cfg.is_boundary_dir("dotnet/Bin/Debug"))
        # vendored-source sets collapse: false, so vendor/third_party are NOT collapsed boundaries.
        self.assertFalse(cfg.is_boundary_dir("vendor"))
        self.assertFalse(cfg.is_boundary_dir("third_party"))
        self.assertFalse(cfg.is_boundary_dir("src"))

    def test_tree_generation_and_materialization_determinism(self) -> None:
        opts = TreeGenOptions(entry_count=20, max_depth=4, include_symlinks=True)
        spec_a = generate_tree_spec(BASE_SEED_PRIMARY, "K01", 5, opts)
        spec_b = generate_tree_spec(BASE_SEED_PRIMARY, "K01", 5, opts)
        spec_other = generate_tree_spec(BASE_SEED_SECONDARY, "K01", 5, opts)

        self.assertEqual(spec_a, spec_b)
        self.assertNotEqual(spec_a, spec_other)
        self.assertEqual(TreeSpec.from_dict(spec_a.to_dict()), spec_a)

        with tempfile.TemporaryDirectory() as d1, tempfile.TemporaryDirectory() as d2:
            m1 = materialize_tree(spec_a, Path(d1))
            m2 = materialize_tree(spec_b, Path(d2))
            self.assertEqual(m1.digest(), m2.digest())
            self.assertEqual(m1.entries, m2.entries)

            by_path = m1.by_path()
            self.assertIn(".git/HEAD", by_path)
            self.assertIn(".paintedwolf/source-scope.yaml", by_path)
            self.assertIn("subrepo/.git/HEAD", by_path)
            self.assertIn("empty_dir", by_path)
            self.assertEqual(by_path["empty_dir"].kind, "dir")
            self.assertIn("assets/blob.bin", by_path)
            self.assertIn("link_to_file", by_path)
            self.assertEqual(by_path["link_to_file"].kind, "symlink")
            self.assertIn(b"\r\n", (Path(d1) / "src/crlf_sample.txt").read_bytes())


class ChangeRunnerTests(unittest.TestCase):
    def test_change_script_determinism_and_independent_write_log(self) -> None:
        spec = generate_tree_spec(BASE_SEED_PRIMARY, "F01", 3, TreeGenOptions(entry_count=12))
        files = spec.file_paths()
        script_a = generate_change_script(BASE_SEED_PRIMARY, "F01", 3, files, op_count=15)
        script_b = generate_change_script(BASE_SEED_PRIMARY, "F01", 3, files, op_count=15)
        self.assertEqual(script_a, script_b)
        self.assertEqual(ChangeScript.from_dict(script_a.to_dict()), script_a)

        # Explicitly exercise all 6 op kinds with a deterministic manual clock.
        explicit_ops = (
            ChangeOp("create", "work/new.txt", content=b"v1\n"),
            ChangeOp("edit", "work/new.txt", content=b"v2\n"),
            ChangeOp("chmod", "work/new.txt", mode=0o755),
            ChangeOp("atomic_replace", "work/new.txt", content=b"v3\n"),
            ChangeOp("rename", "work/new.txt", new_path="work/moved.txt"),
            ChangeOp("delete", "work/moved.txt"),
        )
        explicit_script = ChangeScript(selector=spec.selector, ops=explicit_ops)
        self.assertEqual({op.kind for op in explicit_ops}, set(CHANGE_OP_KINDS))

        ticks = iter(range(1000, 2000, 100))
        runner = ChangeRunner(clock_fn=lambda: next(ticks), clock_domain="manual")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            log = runner.run(root, explicit_script)
            self.assertEqual(log.clock_domain, "manual")
            self.assertEqual([e.seq for e in log.entries], [1, 2, 3, 4, 5, 6])
            self.assertEqual([e.timestamp_ns for e in log.entries], [1000, 1100, 1200, 1300, 1400, 1500])
            # Create had no before_sha256, edit had before==create.after, delete had after==None.
            self.assertIsNone(log.entries[0].before_sha256)
            self.assertEqual(log.entries[1].before_sha256, log.entries[0].after_sha256)
            self.assertEqual(log.entries[2].after_mode, 0o755)
            self.assertEqual(log.entries[4].new_path, "work/moved.txt")
            self.assertIsNone(log.entries[5].after_sha256)
            self.assertFalse((root / "work/moved.txt").exists())
            self.assertEqual(log.touched_paths(), {"work/new.txt", "work/moved.txt"})


class QueryGeneratorTests(unittest.TestCase):
    def test_s01_all_six_query_forms_are_deterministic(self) -> None:
        paths = ["src/a.txt", "pkg/b.go", "docs/readme.md"]
        for form in S01_QUERY_FORMS:
            q1 = generate_s01_query(BASE_SEED_PRIMARY, "S01", 2, form, paths=paths)
            q2 = generate_s01_query(BASE_SEED_PRIMARY, "S01", 2, form, paths=paths)
            self.assertEqual(q1, q2)
            self.assertEqual(CodeQuerySpec.from_dict(q1.to_dict()), q1)

    def test_s04_s05_s06_and_replace_generators(self) -> None:
        lookup = generate_s04_lookup(BASE_SEED_PRIMARY, "S04", 1, ["src/catalog/index.go", "docs/search.md"])
        self.assertTrue(lookup.query_text)

        files, decls = generate_s05_corpus(BASE_SEED_PRIMARY, "S05", 1, S05_LANGUAGES, declarations_per_language=2)
        self.assertEqual(len(files), 6)
        self.assertEqual(len(decls), 12)
        for decl in decls:
            body_lines = files[decl.path].splitlines()
            self.assertIn(decl.name, body_lines[decl.line_number - 1])
            # Also confirm a non-declaration comment mention exists in the file.
            self.assertIn(f"mention of {decl.name}", files[decl.path])

        for kind in S06_TEXT_KINDS:
            case = generate_s06_query_text(BASE_SEED_PRIMARY, "S06", 1, kind)
            self.assertEqual(case.expect_parse_error, kind == "invalid_closed_field")
            self.assertTrue(case.text)

        rep_groups = generate_replace_spec(BASE_SEED_PRIMARY, "R03", 1, with_groups=True)
        self.assertTrue(rep_groups.regex)
        self.assertIn("$1", rep_groups.replacement)
        self.assertIn("${2}", rep_groups.replacement)

        rep_lit = generate_replace_spec(BASE_SEED_PRIMARY, "R03", 1, with_groups=False)
        self.assertFalse(rep_lit.regex)


if __name__ == "__main__":
    unittest.main()
