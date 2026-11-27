"""Unit tests for GB04 (Citation resolution) fixture preparation (R01..R07)."""
from __future__ import annotations

import unittest

from grounding.checker import check_slot
from grounding.delivery_log import ALL_VERDICTS, ProductVerdict
from grounding.generators import BASE_SEEDS, R06_VARIATIONS, R07_MUTATIONS
from grounding.reference_resolver import resolve_citation
from grounding.selectors import FIXED_FAMILIES, GENERATED_FAMILIES
from grounding.suites.gb04_resolution import (
    ALL_GB04_FIXED_SLOTS,
    GB04_GENERATED_SPECS,
    GENERATED_BY_FAMILY,
    R01_SLOTS,
    R02_SLOTS,
    R03_SLOTS,
    R05_SLOTS,
    build_all_fixed_fixtures,
    build_fixed_fixture,
    build_platform_path_case_contrast,
    build_r04_instance,
    build_r04_syntax_vs_observation_cases,
    build_r06_instance,
    build_r07_instance,
    validate_gb04_catalog,
)


class TestGB04CatalogManifest(unittest.TestCase):
    """Verify GB04 catalog counts align with ledger.md, Decision 002, and Decision 018."""

    def test_exact_slot_counts_match_ledger(self) -> None:
        summary = validate_gb04_catalog()
        self.assertEqual(summary["R01_slots"], 10)
        self.assertEqual(summary["R02_slots"], 8)
        self.assertEqual(summary["R03_slots"], 10)
        self.assertEqual(summary["R05_slots"], 6)
        self.assertEqual(summary["total_fixed_slots"], 34)
        self.assertEqual(summary["fixed_sidecar_slots"], 16)
        self.assertEqual(summary["fixed_in_process_slots"], 18)
        self.assertEqual(summary["exact_verdict_slots"], 18)

        # Cross-check against core grounding/selectors.py ledger counts
        core_fixed = {f.family_id: f for f in FIXED_FAMILIES if f.card == "GB04"}
        self.assertEqual(core_fixed["R01"].sidecar_count, len(R01_SLOTS))
        self.assertEqual(core_fixed["R02"].in_process_count, len(R02_SLOTS))
        self.assertEqual(core_fixed["R03"].in_process_count, len(R03_SLOTS))
        self.assertEqual(core_fixed["R05"].sidecar_count, len(R05_SLOTS))

    def test_generated_family_counts_match_ledger(self) -> None:
        core_gen = {f.family_id: f for f in GENERATED_FAMILIES if f.card == "GB04"}
        self.assertEqual(len(GB04_GENERATED_SPECS), 3)
        for fam_id, expected_inproc, expected_sidecar in (
            ("R04", 10000, 200),
            ("R06", 5000, 200),
            ("R07", 5000, 200),
        ):
            spec = GENERATED_BY_FAMILY[fam_id]
            self.assertEqual(spec.in_process_count, expected_inproc)
            self.assertEqual(spec.sidecar_count, expected_sidecar)
            self.assertEqual(spec.base_seeds, BASE_SEEDS)
            self.assertEqual(core_gen[fam_id].in_process_count, expected_inproc)
            self.assertEqual(core_gen[fam_id].sidecar_count, expected_sidecar)

    def test_r02_and_r03_require_exact_verdicts(self) -> None:
        for slot in R02_SLOTS + R03_SLOTS:
            self.assertEqual(slot.lane, "in_process")
            self.assertTrue(slot.assert_exact_verdicts, slot.selector)
        for slot in R01_SLOTS + R05_SLOTS:
            self.assertEqual(slot.lane, "sidecar")
            self.assertFalse(slot.assert_exact_verdicts, slot.selector)


class TestGB04FixedFixtures(unittest.TestCase):
    """Verify all 34 fixed GB04 fixtures build deterministically and pass check_slot."""

    def test_all_34_fixed_fixtures_pass_healthy_check_slot(self) -> None:
        fixtures = build_all_fixed_fixtures()
        self.assertEqual(len(fixtures), 34)
        for selector, fx in fixtures.items():
            outcome = fx.build_healthy_product_outcome()
            res = check_slot(
                fx.delivery_log,
                fx.citations,
                fx.scope,
                outcome,
                assert_exact_verdicts=fx.spec.assert_exact_verdicts,
            )
            self.assertTrue(res.passed, f"{selector} failed healthy check_slot: {res.defects}")
            self.assertEqual(res.status, "pass")

    def test_r01_covers_supported_and_unsupported_for_all_5_types(self) -> None:
        seen_types: dict[str, set[str]] = {}
        for slot in R01_SLOTS:
            fx = build_fixed_fixture(slot.selector)
            for cit, ref_v in zip(fx.citations, fx.expected_verdicts):
                seen_types.setdefault(cit.citation_type, set()).add(ref_v.verdict_class)
        self.assertEqual(set(seen_types.keys()), {"handle", "path_coord", "handle_path", "url", "artifact"})
        for ctype, classes in seen_types.items():
            self.assertIn("grounded", classes, f"R01 missing supported {ctype}")
            self.assertIn("ungrounded", classes, f"R01 missing unsupported {ctype}")

        # Verify mediated contact-only URL vs unobserved URL in R01-08
        fx_url = build_fixed_fixture("R01-08-url-unobserved-contact-only")
        self.assertEqual(fx_url.expected_verdicts[0].reason, "mediated_contact_only")
        self.assertEqual(fx_url.expected_verdicts[1].reason, "url_not_observed")

        # Verify denied-when-present vs absent artifact in R01-10
        fx_art = build_fixed_fixture("R01-10-artifact-denied-or-absent")
        self.assertEqual(fx_art.expected_verdicts[0].reason, "artifact_not_permitted")
        self.assertEqual(fx_art.expected_verdicts[1].reason, "artifact_absent")

    def test_r02_constructs_all_5_exact_verdicts_and_enforces_exact_mismatch(self) -> None:
        observed_exact: set[str] = set()
        for slot in R02_SLOTS:
            fx = build_fixed_fixture(slot.selector)
            for ref_v in fx.expected_verdicts:
                observed_exact.add(ref_v.expected_exact_verdict)
        self.assertEqual(observed_exact, ALL_VERDICTS)

        # Verify assert_exact_verdicts=True catches traced-vs-matched mismatch on R02-02
        fx_traced = build_fixed_fixture("R02-02-verdict-traced")
        wrong_outcome = fx_traced.build_healthy_product_outcome()
        wrong_outcome = type(wrong_outcome)(
            surface=wrong_outcome.surface,
            wording=wrong_outcome.wording,
            verdicts=(
                ProductVerdict(
                    citation_id="c1",
                    verdict="matched",  # expected "traced"
                    resolved_handle="read#1",
                ),
            ),
            records=wrong_outcome.records,
        )
        res = check_slot(
            fx_traced.delivery_log,
            fx_traced.citations,
            fx_traced.scope,
            wrong_outcome,
            assert_exact_verdicts=True,
        )
        self.assertFalse(res.passed)
        self.assertEqual(res.defects[0].kind, "surface_policy_defect")

        # Verify stale-ordinal binding vs wrong-leg / wrong-observation rejection
        fx_stale = build_fixed_fixture("R02-06-stale-ordinal-binds-uniquely")
        self.assertEqual(fx_stale.expected_verdicts[0].expected_exact_verdict, "bound")
        self.assertEqual(fx_stale.expected_verdicts[0].supporting_handles, ("read#1",))

        fx_leg = build_fixed_fixture("R02-07-wrong-leg-rejected")
        self.assertEqual([v.reason for v in fx_leg.expected_verdicts], ["wrong_leg", "wrong_leg"])

        fx_obs = build_fixed_fixture("R02-08-wrong-observation-rejected")
        self.assertEqual([v.reason for v in fx_obs.expected_verdicts], ["wrong_path", "excerpt_mismatch"])

    def test_r03_coordinates_excerpts_reanchoring_and_f3_contrast(self) -> None:
        # Multiline contiguous with blank line & clipping
        fx_multi = build_fixed_fixture("R03-01-multiline-contiguous-blank-lines-clipping")
        self.assertEqual(fx_multi.expected_verdicts[0].expected_exact_verdict, "matched")

        # Cross-observation & working-tree refused
        fx_cross = build_fixed_fixture("R03-02-cross-observation-bytes-refused")
        self.assertEqual(fx_cross.expected_verdicts[0].reason, "excerpt_mismatch")
        fx_wt = build_fixed_fixture("R03-03-working-tree-bytes-refused")
        self.assertEqual(fx_wt.expected_verdicts[0].reason, "excerpt_mismatch")
        self.assertTrue(fx_wt.working_tree_only_lines)

        # Unique-excerpt re-anchoring (1, 0, >1 matches)
        fx_reanchor_1 = build_fixed_fixture("R03-06-reanchor-unique-one-match")
        self.assertEqual(fx_reanchor_1.expected_verdicts[0].expected_exact_verdict, "matched")
        self.assertEqual(fx_reanchor_1.expected_verdicts[0].reanchored_line, 11)

        fx_reanchor_0 = build_fixed_fixture("R03-07-reanchor-zero-matches-rejected")
        self.assertEqual(fx_reanchor_0.expected_verdicts[0].expected_exact_verdict, "unverifiable")
        self.assertIsNone(fx_reanchor_0.expected_verdicts[0].reanchored_line)

        fx_reanchor_many = build_fixed_fixture("R03-08-reanchor-multiple-matches-rejected")
        self.assertEqual(fx_reanchor_many.expected_verdicts[0].reason, "excerpt_ambiguous")
        self.assertIsNone(fx_reanchor_many.expected_verdicts[0].reanchored_line)

        # GB02-R6 inclusive multi-line range with uncovered end line rejected
        fx_range = build_fixed_fixture("R03-09-inclusive-multiline-range-uncovered-end-rejected")
        self.assertEqual(fx_range.expected_verdicts[0].reason, "uncovered_range")

        # F3 PrintedOtherNumbers contrast cases
        fx_f3 = build_fixed_fixture("R03-10-f3-printed-other-numbers-contrast")
        v_no_exc, v_with_exc, v_copied_tag = fx_f3.expected_verdicts
        self.assertEqual(v_no_exc.expected_exact_verdict, "unverifiable")
        self.assertEqual(v_no_exc.contract_reading_verdict, "traced")
        self.assertTrue(v_no_exc.disputed_expectation)
        self.assertEqual(v_with_exc.expected_exact_verdict, "matched")
        self.assertFalse(v_with_exc.disputed_expectation)
        self.assertEqual(v_copied_tag.expected_exact_verdict, "matched")
        self.assertEqual(v_copied_tag.reason, "copied_host_delivery_tag")
        self.assertTrue(v_copied_tag.disputed_expectation)

    def test_r05_refusal_reasons_and_replacement_uniqueness(self) -> None:
        reasons: set[str] = set()
        for slot in R05_SLOTS:
            fx = build_fixed_fixture(slot.selector)
            self.assertFalse(fx.host_rewrites_citation)
            assert fx.expected_refusal_reason is not None
            reasons.add(fx.expected_refusal_reason)
        self.assertEqual(
            reasons,
            {"unknown_handle", "wrong_path", "uncovered_range", "excerpt_mismatch"},
        )

        self.assertEqual(
            build_fixed_fixture("R05-05-replacement-suggested-unique-one").expected_suggested_replacement,
            "read#2",
        )
        self.assertIsNone(
            build_fixed_fixture("R05-06-replacement-omitted-when-ambiguous-multiple").expected_suggested_replacement
        )


class TestGB04GeneratedFamilies(unittest.TestCase):
    """Verify R04, R06, and R07 generated fixture builders and platform contrasts."""

    def test_r04_syntax_vs_observation_separation(self) -> None:
        cases = build_r04_syntax_vs_observation_cases()
        by_id = {c.case_id: c for c in cases}

        # Indented code is literal, never a citation attempt
        indented = by_id["R04-COV02-indented-code-literal"]
        self.assertFalse(indented.expected_syntax_attempt)
        self.assertFalse(indented.spans[0].is_citation_attempt)

        # Known vs unknown bare path: syntax attempt, but neither is line/excerpt evidence
        for cid in ("R04-COV02-known-bare-path", "R04-COV02-unknown-bare-path"):
            c = by_id[cid]
            self.assertTrue(c.expected_syntax_attempt)
            self.assertTrue(c.expected_syntax_valid)
            assert c.citation_under_test is not None
            v = resolve_citation(c.delivery_log, c.citation_under_test, c.scope)
            self.assertEqual(v.verdict_class, "ungrounded")
            self.assertEqual(v.reason, "bare_path_not_line_evidence")

        # Ordinary HTTP(S) link has valid syntax even when destination URL is unobserved
        http_case = by_id["R04-COV02-http-link-valid-syntax-unobserved-dest"]
        self.assertTrue(http_case.spans[0].is_citation_attempt)
        self.assertTrue(http_case.spans[0].syntax_valid)
        self.assertFalse(http_case.spans[0].destination_observed)
        assert http_case.citation_under_test is not None
        v_http = resolve_citation(http_case.delivery_log, http_case.citation_under_test, http_case.scope)
        self.assertEqual(v_http.verdict_class, "ungrounded")
        self.assertEqual(v_http.reason, "url_not_observed")

        # >4096B inline node is a structured issue, never truncated into acceptance
        big = by_id["R04-COV02-oversized-inline-node-gt-4096"]
        self.assertGreater(len(big.markdown.encode("utf-8")), 4096)
        self.assertFalse(big.spans[0].syntax_valid)

        # Seeded R04 determinism & span offsets across both base seeds
        for base_seed in BASE_SEEDS:
            c0 = build_r04_instance(base_seed, 0)
            c0_repeat = build_r04_instance(base_seed, 0)
            self.assertEqual(c0, c0_repeat)
            self.assertTrue(c0.oversized_inline_nodes)  # index 0 % 17 == 0
            for span in c0.spans:
                self.assertEqual(c0.markdown[span.start : span.end], span.text)

    def test_r06_all_8_variations_grounded_on_darwin_and_linux(self) -> None:
        for base_seed in BASE_SEEDS:
            for platform in ("darwin", "linux"):
                for idx, variation in enumerate(R06_VARIATIONS):
                    prepared = build_r06_instance(
                        base_seed,
                        idx,
                        variation=variation,
                        platform=platform,
                    )
                    self.assertEqual(
                        prepared.expected_verdict.verdict_class,
                        "grounded",
                        f"R06 {base_seed}/{platform}/{variation} was not grounded: {prepared.expected_verdict}",
                    )
                    self.assertEqual(prepared.case_insensitive_paths, platform == "darwin")

    def test_r07_all_6_mutations_ungrounded_on_darwin_and_linux(self) -> None:
        for base_seed in BASE_SEEDS:
            for platform in ("darwin", "linux"):
                for idx, mutation in enumerate(R07_MUTATIONS):
                    prepared = build_r07_instance(
                        base_seed,
                        idx,
                        mutation=mutation,
                        platform=platform,
                    )
                    self.assertEqual(
                        prepared.expected_verdict.verdict_class,
                        "ungrounded",
                        f"R07 {base_seed}/{platform}/{mutation} unexpectedly grounded: {prepared.expected_verdict}",
                    )
                    self.assertFalse(prepared.reanchor_possible)
                    self.assertFalse(prepared.binding_possible)

    def test_darwin_vs_linux_path_case_contrast(self) -> None:
        for base_seed in BASE_SEEDS:
            darwin_case, linux_case = build_platform_path_case_contrast(base_seed, 7)
            self.assertTrue(darwin_case.case_insensitive_paths)
            self.assertEqual(darwin_case.expected_verdict.verdict_class, "grounded")
            self.assertEqual(darwin_case.expected_verdict.expected_exact_verdict, "bound")

            self.assertFalse(linux_case.case_insensitive_paths)
            self.assertEqual(linux_case.expected_verdict.verdict_class, "ungrounded")
            self.assertEqual(linux_case.expected_verdict.reason, "path_not_observed")


if __name__ == "__main__":
    unittest.main()
