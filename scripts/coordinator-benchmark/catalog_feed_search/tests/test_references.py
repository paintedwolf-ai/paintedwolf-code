"""Tests for independent D3 references: walk, ripgrep 15.x search, byte replace, S04 lookup."""

from __future__ import annotations

import hashlib
from pathlib import Path
import tempfile
import unittest

from catalog_feed_search.generators import (
    BoundaryConfig,
    TreeEntrySpec,
    TreeGenOptions,
    TreeSpec,
    generate_tree_spec,
    load_repo_boundary_config,
    materialize_tree,
)
from catalog_feed_search.queries import CodeQuerySpec, ReplaceSpec
from catalog_feed_search.references import (
    PRODUCT_DIFFERENCES,
    SUPPORTED_REPLACE_ENCODINGS,
    UnsupportedEncodingError,
    capture_rg_version,
    reference_file_lookup,
    reference_replace,
    reference_search,
    reference_walk,
)
from catalog_feed_search.seeds import BASE_SEED_PRIMARY, ReplaySelector

REPO_ROOT = Path(__file__).resolve().parents[4]


class ReferenceWalkTests(unittest.TestCase):
    def test_walk_no_follow_symlinks_boundaries_and_exclusions(self) -> None:
        cfg = load_repo_boundary_config(REPO_ROOT)
        spec = generate_tree_spec(
            BASE_SEED_PRIMARY,
            "K01",
            1,
            TreeGenOptions(entry_count=16, include_symlinks=True, include_nested_checkouts=True),
        )
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            materialize_tree(spec, root)

            entries = reference_walk(root, boundary_config=cfg, include_hidden=True)
            by_path = {e.path: e for e in entries}

            # Symlinks recorded as symlinks and never descended.
            self.assertEqual(by_path["link_to_dir"].kind, "symlink")
            self.assertFalse(any(p.startswith("link_to_dir/") for p in by_path))

            # Boundary folders and nested checkouts recorded with boundary=True and not descended.
            self.assertTrue(by_path[".git"].boundary)
            self.assertFalse(any(p.startswith(".git/") for p in by_path))
            self.assertTrue(by_path["subrepo"].boundary)
            self.assertFalse(any(p.startswith("subrepo/") for p in by_path))

            # People see .paintedwolf; agent exclusions omit it.
            self.assertIn(".paintedwolf", by_path)
            agent_entries = reference_walk(
                root,
                boundary_config=cfg,
                include_hidden=True,
                agent_exclusions=(".paintedwolf/**", ".paintedwolf"),
            )
            self.assertFalse(any(e.path.startswith(".paintedwolf") for e in agent_entries))

            # Hidden exclusion projection omits dotfiles.
            visible_only = reference_walk(root, boundary_config=cfg, include_hidden=False)
            self.assertFalse(any(e.hidden for e in visible_only))


class ReferenceSearchAndReplaceTests(unittest.TestCase):
    def test_rg_version_is_15_line(self) -> None:
        version = capture_rg_version()
        self.assertTrue(version.startswith("ripgrep 15."), f"unexpected rg version: {version}")
        self.assertIn("hidden_files_default_excluded", PRODUCT_DIFFERENCES)

    def test_reference_search_exact_byte_offsets_and_filters(self) -> None:
        sel = ReplaySelector(BASE_SEED_PRIMARY, "S01", 0)
        spec = TreeSpec(
            selector=sel,
            entries=(
                TreeEntrySpec("src/alpha.txt", "file", b"Hello Alpha_12 and alpha\nSecond line\n"),
                TreeEntrySpec("src/crlf.txt", "file", b"Alpha_99 here\r\nalpha_00\r\n"),
                TreeEntrySpec("docs/notes.md", "file", b"alpha_42 in docs\n"),
                TreeEntrySpec(".hidden.txt", "file", b"Alpha_77 hidden\n"),
                TreeEntrySpec("node_modules/pkg/index.js", "file", b"Alpha_55 boundary\n"),
            ),
        )
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            materialize_tree(spec, root)

            # Case-insensitive literal, default hidden & boundary excluded.
            res = reference_search(
                root,
                CodeQuerySpec(form="literal", pattern="alpha", case_sensitive=False),
                boundary_patterns=("node_modules",),
            )
            self.assertTrue(res.rg_version.startswith("ripgrep 15."))
            self.assertNotIn(".hidden.txt", res.matched_files())
            self.assertNotIn("node_modules/pkg/index.js", res.matched_files())
            self.assertEqual(res.matched_files(), {"src/alpha.txt", "src/crlf.txt", "docs/notes.md"})

            # Verify exact byte offsets against raw file bytes.
            for hit in res.hits:
                raw = (root / hit.path).read_bytes()
                self.assertEqual(raw[hit.byte_offset : hit.byte_end].decode("utf-8"), hit.match_text)

            # Case-sensitive literal.
            cs = reference_search(
                root,
                CodeQuerySpec(form="case_sensitive_literal", pattern="Alpha", case_sensitive=True),
                boundary_patterns=("node_modules",),
            )
            self.assertTrue(all(h.match_text == "Alpha" for h in cs.hits))

            # Whole-word.
            ww = reference_search(
                root,
                CodeQuerySpec(form="whole_word", pattern="alpha", case_sensitive=False, whole_word=True),
                boundary_patterns=("node_modules",),
            )
            self.assertEqual(len(ww.hits), 1)
            self.assertEqual(ww.hits[0].path, "src/alpha.txt")

            # Shared-syntax regex with numbered groups.
            rx = reference_search(
                root,
                CodeQuerySpec(form="regex", pattern=r"(alpha)_([0-9]{2})", regex=True, case_sensitive=False),
                boundary_patterns=("node_modules",),
            )
            self.assertEqual(len(rx.hits), 4)

            # Explicit hidden path filter admits hidden file.
            hid = reference_search(
                root,
                CodeQuerySpec(form="path_filtered", pattern="Alpha_77", path_filters=(".hidden.txt",)),
            )
            self.assertEqual(hid.matched_files(), {".hidden.txt"})

            # Glob-filtered query.
            gl = reference_search(
                root,
                CodeQuerySpec(form="glob_filtered", pattern="alpha", include_globs=("*.md",)),
            )
            self.assertEqual(gl.matched_files(), {"docs/notes.md"})

    def test_reference_replace_numbered_groups_and_crlf(self) -> None:
        sel = ReplaySelector(BASE_SEED_PRIMARY, "R03", 0)
        original = b"item Alpha_12 and Alpha_34\r\nsecond line\r\n"
        spec = TreeSpec(selector=sel, entries=(TreeEntrySpec("src/data.txt", "file", original),))
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            materialize_tree(spec, root)

            rep = reference_replace(
                root,
                ReplaceSpec(
                    pattern=r"(Alpha)_([0-9]{2})",
                    replacement="id=$2($1)$$",
                    regex=True,
                    case_sensitive=True,
                ),
            )
            self.assertIn("src/data.txt", rep)
            res = rep["src/data.txt"]
            expected = b"item id=12(Alpha)$ and id=34(Alpha)$\r\nsecond line\r\n"
            self.assertTrue(res.changed)
            self.assertEqual(res.match_count, 2)
            self.assertEqual(res.resulting_bytes, expected)
            self.assertEqual(res.before_sha256, hashlib.sha256(original).hexdigest())
            self.assertEqual(res.after_sha256, hashlib.sha256(expected).hexdigest())

    def test_capture_expansion_longest_name_and_braced_syntax(self) -> None:
        sel = ReplaySelector(BASE_SEED_PRIMARY, "R03", 1)
        spec = TreeSpec(selector=sel, entries=(TreeEntrySpec("src/sample.txt", "file", b"alpha_12\n"),))
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            materialize_tree(spec, root)

            # Longest-name rule (Go regexp.ExpandString / ripgrep 15.x): $2_ is unknown name -> empty.
            unbraced = reference_replace(
                root,
                ReplaceSpec(
                    pattern=r"(alpha)_([0-9]{1,2})",
                    replacement="rewritten_$2_$1",
                    regex=True,
                ),
            )
            self.assertEqual(unbraced["src/sample.txt"].resulting_bytes, b"rewritten_alpha\n")

            # Brace-delimited ${2} followed by _ expands group 2 cleanly.
            braced = reference_replace(
                root,
                ReplaceSpec(
                    pattern=r"(alpha)_([0-9]{1,2})",
                    replacement="rewritten_${2}_$1",
                    regex=True,
                ),
            )
            self.assertEqual(braced["src/sample.txt"].resulting_bytes, b"rewritten_12_alpha\n")

    def test_reference_replace_preserves_utf8_and_utf16_boms_and_rejects_unsupported(self) -> None:
        self.assertEqual(
            SUPPORTED_REPLACE_ENCODINGS,
            ("utf-8", "utf-8-bom", "utf-16-le-bom", "utf-16-be-bom"),
        )
        sel = ReplaySelector(BASE_SEED_PRIMARY, "R03", 2)
        spec = TreeSpec(
            selector=sel,
            entries=(
                TreeEntrySpec("u8.txt", "file", b"alpha\n"),
                TreeEntrySpec("u8bom.txt", "file", b"\xef\xbb\xbfalpha\n"),
                TreeEntrySpec("u16le.txt", "file", b"\xff\xfe" + "alpha\n".encode("utf-16-le")),
                TreeEntrySpec("u16be.txt", "file", b"\xfe\xff" + "alpha\r\n".encode("utf-16-be")),
            ),
        )
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            materialize_tree(spec, root)
            rep = reference_replace(root, ReplaceSpec(pattern="alpha", replacement="beta", regex=False))

            self.assertEqual(rep["u8.txt"].encoding, "utf-8")
            self.assertEqual(rep["u8.txt"].resulting_bytes, b"beta\n")

            self.assertEqual(rep["u8bom.txt"].encoding, "utf-8-bom")
            self.assertEqual(rep["u8bom.txt"].resulting_bytes, b"\xef\xbb\xbfbeta\n")

            self.assertEqual(rep["u16le.txt"].encoding, "utf-16-le-bom")
            self.assertEqual(rep["u16le.txt"].resulting_bytes, b"\xff\xfe" + "beta\n".encode("utf-16-le"))

            self.assertEqual(rep["u16be.txt"].encoding, "utf-16-be-bom")
            self.assertEqual(rep["u16be.txt"].resulting_bytes, b"\xfe\xff" + "beta\r\n".encode("utf-16-be"))

        # Unsupported encoding (UTF-32LE BOM) raises explicitly instead of corrupting bytes.
        from catalog_feed_search.references import _detect_and_decode_for_replace

        with self.assertRaises(UnsupportedEncodingError):
            _detect_and_decode_for_replace(b"\xff\xfe\x00\x00a\x00\x00\x00", "u32.txt")

    def test_legal_posix_backslash_filename_preserved_end_to_end(self) -> None:
        sel = ReplaySelector(BASE_SEED_PRIMARY, "K04", 0)
        bs_name = "foo\\bar.txt"
        spec = TreeSpec(selector=sel, entries=(TreeEntrySpec(bs_name, "file", b"alpha_1\n"),))
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            materialize_tree(spec, root)

            walked = reference_walk(root)
            self.assertEqual([e.path for e in walked], [bs_name])

            searched = reference_search(root, CodeQuerySpec(form="literal", pattern="alpha"))
            self.assertEqual(searched.matched_files(), {bs_name})

            replaced = reference_replace(root, ReplaceSpec(pattern="alpha", replacement="beta"))
            self.assertIn(bs_name, replaced)
            self.assertEqual(replaced[bs_name].resulting_bytes, b"beta_1\n")

            lookup_hits = reference_file_lookup([bs_name], "foo\\bar")
            self.assertEqual([h.path for h in lookup_hits], [bs_name])
            self.assertEqual(lookup_hits[0].tier, 0)


class ReferenceFileLookupTests(unittest.TestCase):
    def test_s04_documented_ranking_order(self) -> None:
        paths = [
            "very/long/path/containing_cat_in_middle.txt",  # tier 1 (basename substring, longer)
            "a/my_cat.txt",                                 # tier 1 (basename substring, shorter)
            "z/deep/catalog.go",                            # tier 0 (basename prefix, longer)
            "src/cat.go",                                   # tier 0 (basename prefix, shorter)
            "c/a/t/file.txt",                               # tier 2 (full-path window len 5)
            "c/xxxx/a/yyyy/t/file.txt",                     # tier 2 (full-path window len 15)
            "unrelated/file.txt",                           # no match
        ]
        hits = reference_file_lookup(paths, "cat")
        self.assertEqual(
            [h.path for h in hits],
            [
                "src/cat.go",
                "z/deep/catalog.go",
                "a/my_cat.txt",
                "very/long/path/containing_cat_in_middle.txt",
                "c/a/t/file.txt",
                "c/xxxx/a/yyyy/t/file.txt",
            ],
        )
        self.assertEqual(hits[0].tier, 0)
        self.assertEqual(hits[0].matched_ranges, ((4, 7),))
        self.assertEqual(hits[2].tier, 1)
        self.assertEqual(hits[4].tier, 2)
        self.assertLess(hits[4].window_len, hits[5].window_len)


if __name__ == "__main__":
    unittest.main()
