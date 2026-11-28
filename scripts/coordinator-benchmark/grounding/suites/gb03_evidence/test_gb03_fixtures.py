"""Unit tests for GB03 (Evidence recording and scope: V01..V07) fixture preparation."""
from __future__ import annotations

import pathlib
import tempfile
import unittest

from grounding.checker import ProductRecord, check_slot
from grounding.delivery_log import LineRange
from grounding.reference_resolver import resolve_citation
from grounding.suites.gb03_evidence import (
    D16_RAW_SECRET_CANARY,
    GB03_FIXED_SLOTS,
    SLOTS_BY_SELECTOR,
    V01_SLOTS,
    V02_GENERATED_SPEC,
    V03_SLOTS,
    V04_SLOTS,
    V05_SLOTS,
    V06_SLOTS,
    V07_SLOTS,
    build_all_gb03_fixtures,
    gb03_manifest_counts,
    get_gb03_fixture,
    iter_v02_sample,
    validate_gb03_catalog,
)
from grounding.suites.gb03_evidence.full_runner import (
    FIXED_SIDECAR_SELECTORS,
    SIDECAR_SLOT_HANDLERS,
    _decompress_zstd_fallback,
    _normalize_product_records,
    _run_v01_03,
    _run_v03_01,
    _run_v03_07,
    parse_families_arg,
    select_gb03_work_items,
)
from grounding.suites.gb03_evidence.v07_inprocess_runner import V07_INPROCESS_SELECTORS


class TestGB03CatalogManifest(unittest.TestCase):
    def test_catalog_matches_ledger_and_selectors(self) -> None:
        summary = validate_gb03_catalog()
        # V01(14) + V03(8) + V04(10, Decision 005/014) + V05(5) + V06(10) + V07(2+4) = 53
        self.assertEqual(summary["fixed_sidecar_slots"], 49)
        self.assertEqual(summary["fixed_in_process_slots"], 4)
        self.assertEqual(summary["fixed_total_slots"], 53)
        self.assertEqual(summary["v02_in_process_per_seed"], 3000)
        self.assertEqual(summary["v02_sidecar_per_seed"], 100)

        counts = gb03_manifest_counts()
        self.assertEqual(counts["V01"]["sidecar"], 14)
        self.assertEqual(counts["V03"]["sidecar"], 8)
        self.assertEqual(counts["V04"]["sidecar"], 10)
        self.assertEqual(counts["V05"]["sidecar"], 5)
        self.assertEqual(counts["V06"]["sidecar"], 10)
        self.assertEqual(counts["V07"]["sidecar"], 2)
        self.assertEqual(counts["V07"]["in_process"], 4)
        self.assertEqual(len(GB03_FIXED_SLOTS), 53)
        self.assertEqual(len(SLOTS_BY_SELECTOR), 53)


class TestGB03FixedFixtures(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.fixtures = build_all_gb03_fixtures()

    def test_all_53_fixed_fixtures_resolve_and_pass_healthy_check_slot(self) -> None:
        self.assertEqual(len(self.fixtures), 53)
        for selector, fx in self.fixtures.items():
            with self.subTest(selector=selector):
                self.assertEqual(fx.selector, selector)
                for exp in fx.expectations:
                    verdict = resolve_citation(fx.delivery_log, exp.citation, fx.scope)
                    self.assertEqual(
                        verdict.verdict_class,
                        exp.expected_verdict_class,
                        f"{selector} {exp.citation.citation_id}: class mismatch ({verdict})",
                    )
                    self.assertEqual(
                        verdict.expected_exact_verdict,
                        exp.expected_exact_verdict,
                        f"{selector} {exp.citation.citation_id}: exact verdict mismatch ({verdict})",
                    )
                    if exp.expected_reason:
                        self.assertEqual(
                            verdict.reason,
                            exp.expected_reason,
                            f"{selector} {exp.citation.citation_id}: reason mismatch ({verdict})",
                        )
                    if exp.expected_supporting_handles:
                        self.assertEqual(
                            verdict.supporting_handles,
                            exp.expected_supporting_handles,
                            f"{selector} {exp.citation.citation_id}: handles mismatch",
                        )

                # Healthy surface check_slot passes with 0 defects
                outcome = fx.healthy_surface_outcome()
                report = check_slot(
                    fx.delivery_log,
                    tuple(e.citation for e in fx.expectations),
                    fx.scope,
                    outcome,
                )
                self.assertTrue(
                    report.passed,
                    f"{selector} healthy check_slot failed: {report.defects}",
                )

    def test_v04_includes_copied_second_file_tag_and_f3_contrast(self) -> None:
        v04_selectors = [s.selector for s in V04_SLOTS]
        self.assertEqual(len(v04_selectors), 10)
        copied = [s for s in v04_selectors if "copied" in s]
        self.assertTrue(copied, f"Expected copied-second-file-tag slot in {v04_selectors}")
        fx = get_gb03_fixture(copied[0])
        self.assertTrue(fx.expectations)

    def test_v05_secret_canary_screening(self) -> None:
        canary_slots = [
            fx for fx in self.fixtures.values()
            if fx.family_id == "V05" and fx.expected_forbidden_body_substrings
        ]
        self.assertTrue(canary_slots, "Expected D16 secret canary slot in V05")
        for fx in canary_slots:
            self.assertIn(D16_RAW_SECRET_CANARY, fx.expected_forbidden_body_substrings)
            for rec in fx.delivery_log.records:
                if rec.model_visible:
                    self.assertNotIn(
                        D16_RAW_SECRET_CANARY.encode("utf-8"),
                        rec.raw_bytes,
                        f"Secret canary leaked into model-visible record in {fx.selector}",
                    )

    def test_materialize_workspace_writes_deterministic_files(self) -> None:
        fx = get_gb03_fixture(V01_SLOTS[0].selector)
        with tempfile.TemporaryDirectory(prefix="gb03-ws-") as tmp:
            root = fx.materialize_workspace(tmp)
            for rel_path, expected in fx.workspace_files.items():
                actual = (pathlib.Path(root) / rel_path).read_text(encoding="utf-8")
                self.assertEqual(actual, expected)


class TestGB03V02GeneratedLookalikes(unittest.TestCase):
    def test_v02_lookalike_sample_creates_zero_promoted_citations(self) -> None:
        samples = iter_v02_sample(count_per_seed=10)
        self.assertEqual(len(samples), 20)
        for sample in samples:
            self.assertEqual(sample.case.expected_minted_tags, ())
            for cit in sample.spoofed_citations:
                verdict = resolve_citation(sample.delivery_log, cit, sample.scope)
                self.assertEqual(verdict.verdict_class, "ungrounded")
                self.assertEqual(verdict.expected_exact_verdict, "unverifiable")


class TestGB03RunnerCoverageAndSharding(unittest.TestCase):
    def test_all_53_fixed_slots_are_mapped_across_sidecar_and_inprocess_runners(self) -> None:
        sidecar_expected = {s.selector for s in GB03_FIXED_SLOTS if s.lane == "sidecar"}
        inproc_expected = {s.selector for s in GB03_FIXED_SLOTS if s.lane == "in_process"}

        self.assertEqual(len(sidecar_expected), 49)
        self.assertEqual(len(inproc_expected), 4)
        self.assertEqual(set(FIXED_SIDECAR_SELECTORS), sidecar_expected)
        self.assertEqual(set(SIDECAR_SLOT_HANDLERS.keys()), sidecar_expected)
        self.assertEqual(set(V07_INPROCESS_SELECTORS), inproc_expected)
        self.assertTrue(sidecar_expected.isdisjoint(inproc_expected))
        self.assertEqual(sidecar_expected | inproc_expected, set(SLOTS_BY_SELECTOR.keys()))

        overlay = (
            pathlib.Path(__file__).resolve().parents[2]
            / "overlays"
            / "gb03_v07_inprocess_test.go"
        )
        self.assertTrue(overlay.is_file(), f"Missing V07 in-process overlay: {overlay}")

    def test_sharding_is_disjoint_and_complete(self) -> None:
        all_items = select_gb03_work_items(
            families=("V01", "V02", "V03", "V04", "V05", "V06", "V07"),
            base_seed="20261127",
            limit_v02=100,
            shard=0,
            shards=1,
        )
        # 49 fixed sidecar slots + 100 V02 sidecar instances = 149
        self.assertEqual(len(all_items), 149)
        all_selectors = [it.selector for it in all_items]
        self.assertEqual(len(set(all_selectors)), 149)

        for total_shards in (2, 3, 4, 7):
            seen: list[str] = []
            for k in range(total_shards):
                part = select_gb03_work_items(
                    families=("V01", "V02", "V03", "V04", "V05", "V06", "V07"),
                    base_seed="20261127",
                    limit_v02=100,
                    shard=k,
                    shards=total_shards,
                )
                part_sels = [it.selector for it in part]
                # Disjoint from previously seen shards
                self.assertTrue(set(seen).isdisjoint(set(part_sels)))
                seen.extend(part_sels)
            self.assertEqual(set(seen), set(all_selectors))

    def test_parse_families_arg_validation(self) -> None:
        self.assertEqual(parse_families_arg("V01,V03,V07"), ("V01", "V03", "V07"))
        self.assertEqual(parse_families_arg("v04, v04, v05"), ("V04", "V05"))
        with self.assertRaises(ValueError):
            parse_families_arg("V01,V99")
        with self.assertRaises(ValueError):
            select_gb03_work_items(shard=2, shards=2)
        with self.assertRaises(ValueError):
            select_gb03_work_items(shard=0, shards=0)

    def test_normalize_product_records_decompresses_zstd_and_is_noop_on_plaintext(self) -> None:
        import hashlib
        import json
        import shutil
        import subprocess

        plain_src = b"package app\n\nfunc Alpha() int {\n    return 42\n}\n"
        src_sha = hashlib.sha256(plain_src).hexdigest()
        plain_rec = ProductRecord(
            handle="read#1",
            kind="read",
            session_id="s1",
            worker_leg="",
            source_revision=src_sha,
            delivered_ranges=(LineRange(1, 5),),
            body_sha256=src_sha,
            raw_bytes=plain_src,
            path="internal/a.go",
            shape="file_region",
        )
        # Already-decompressed record is returned unchanged
        self.assertEqual(_normalize_product_records((plain_rec,)), (plain_rec,))

        # Zstd-framed JSON []string blob is transparently decompressed when zstd is available
        payload = json.dumps(
            [
                "     1\tpackage app",
                "     2\t",
                "     3\tfunc Alpha() int {",
                "     4\t    return 42",
                "     5\t}",
            ]
        ).encode("utf-8")
        zstd_bin = shutil.which("zstd")
        if zstd_bin:
            compressed = subprocess.run(
                [zstd_bin, "-c"], input=payload, capture_output=True, check=True
            ).stdout
            if _decompress_zstd_fallback(compressed) is not None:
                zstd_rec = ProductRecord(
                    handle="read#1",
                    kind="read",
                    session_id="s1",
                    worker_leg="",
                    source_revision=src_sha,
                    delivered_ranges=(LineRange(1, 5),),
                    body_sha256=hashlib.sha256(compressed).hexdigest(),
                    raw_bytes=compressed,
                    path="internal/a.go",
                    shape="file_region",
                )
                norm = _normalize_product_records((zstd_rec,))
                self.assertEqual(norm[0].raw_bytes, plain_src)
                self.assertEqual(norm[0].body_sha256, src_sha)

    def test_two_phase_supersession_mutates_workspace_between_prompts_without_pending_progress(self) -> None:
        from types import SimpleNamespace

        with tempfile.TemporaryDirectory(prefix="gb03-two-phase-") as tmp:
            ws = pathlib.Path(tmp)
            (ws / "internal").mkdir(parents=True, exist_ok=True)
            calls: list[dict] = []

            class FakeCtx:
                workspace = ws

                def run_session(self, **kwargs):
                    sid = kwargs.get("session_id") or f"sess-{len(calls) + 1}"
                    # Verify no unclosed update_progress tool call is scripted
                    for t in kwargs["turns"]:
                        for tc in t.tool_calls:
                            self.assert_not_progress(tc)
                    rel_files = {
                        p.name: p.read_text(encoding="utf-8")
                        for p in (ws / "internal").glob("*.go")
                    }
                    calls.append({"sid": sid, "kwargs": kwargs, "files": rel_files})
                    r1 = ProductRecord(
                        handle="read#1",
                        kind="read",
                        session_id=sid,
                        worker_leg="",
                        source_revision="",
                        body_sha256="a" * 64,
                        raw_bytes=b"package app\n\nfunc Val() int { return 100 }\n",
                        path="internal/v03_01.go",
                        shape="file_region",
                        superseded_by="read#2",
                    )
                    r2 = ProductRecord(
                        handle="read#2",
                        kind="read",
                        session_id=sid,
                        worker_leg="",
                        source_revision="",
                        body_sha256="b" * 64,
                        raw_bytes=b"package app\n\nfunc Val() int { return 200 }\n",
                        path="internal/v03_01.go",
                        shape="file_region",
                    )
                    art = SimpleNamespace(session_id=sid, product_records=(r1, r2))
                    info = {
                        "minted_handles": ["command#1", "read#1", "command#2", "read#2"],
                        "refused": False,
                        "cited_evidence_count": 2,
                        "minting_parity_defects": [],
                    }
                    return art, info, {}, {}, {}

                def assert_not_progress(self, tc):
                    if tc["name"] == "update_progress":
                        raise AssertionError("Unexpected update_progress call")

            ctx = FakeCtx()
            rep01, div01 = _run_v03_01(ctx)  # type: ignore[arg-type]
            self.assertTrue(rep01["passed"], div01)
            self.assertEqual(len(calls), 2)
            self.assertEqual(calls[0]["sid"], calls[1]["sid"])
            self.assertIn("return 100", calls[0]["files"]["v03_01.go"])
            self.assertIn("return 200", calls[1]["files"]["v03_01.go"])

            calls.clear()
            rep03, div03 = _run_v01_03(ctx)  # type: ignore[arg-type]
            self.assertTrue(rep03["passed"], div03)
            self.assertEqual(len(calls), 2)
            self.assertEqual(calls[0]["sid"], calls[1]["sid"])
            self.assertIn("const V = 1", calls[0]["files"]["mut03.go"])
            self.assertIn("const V = 2", calls[1]["files"]["mut03.go"])

            calls.clear()
            rep07, div07 = _run_v03_07(ctx)  # type: ignore[arg-type]
            self.assertTrue(rep07["passed"], div07)
            self.assertEqual(len(calls), 2)
            self.assertEqual(calls[0]["sid"], calls[1]["sid"])
            self.assertIn("return 100", calls[0]["files"]["v03_07.go"])
            self.assertIn("return 200", calls[1]["files"]["v03_07.go"])


if __name__ == "__main__":
    unittest.main()
