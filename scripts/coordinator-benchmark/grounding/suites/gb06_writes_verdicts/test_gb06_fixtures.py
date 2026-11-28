"""Unit tests for GB06 (Findings, verdicts, worker results: W01..W04, D5 tripled)."""
from __future__ import annotations

import unittest

from grounding.checker import check_slot
from grounding.reference_resolver import resolve_citation
from grounding.selectors import WORDING_VARIANTS
from grounding.suites.gb06_writes_verdicts import (
    ALL_GB06_SLOTS,
    SLOTS_BY_SELECTOR,
    build_all_gb06_fixtures,
    build_gb06_fixture,
    validate_gb06_catalog,
)
from grounding.suites.gb06_writes_verdicts.full_runner import (
    GB06_EXECUTION_SPECS_BY_SELECTOR,
    evaluate_wording_invariance,
    select_gb06_runs,
    validate_gb06_execution_map,
)


class TestGB06CatalogAndFixtures(unittest.TestCase):
    def test_catalog_matches_ledger_and_d5_tripling(self) -> None:
        summary = validate_gb06_catalog()
        self.assertEqual(summary["sidecar_slots"], 32)
        self.assertEqual(summary["in_process_slots"], 0)
        self.assertEqual(summary["base_slots"], 32)
        self.assertEqual(summary["d5_tripled_per_platform"], 96)
        self.assertEqual(len(ALL_GB06_SLOTS), 32)
        self.assertEqual(len(SLOTS_BY_SELECTOR), 32)

    def test_all_93_wording_fixtures_pass_and_are_wording_invariant(self) -> None:
        fixtures = build_all_gb06_fixtures()
        self.assertEqual(len(fixtures), 96)
        for spec in ALL_GB06_SLOTS:
            verdicts = []
            for w in WORDING_VARIANTS:
                fx = fixtures[(spec.selector, w)]
                res = check_slot(fx.delivery_log, fx.citations, fx.scope, fx.build_healthy_outcome())
                self.assertTrue(res.passed, f"{spec.selector}/{w} failed: {res.defects}")
                verdicts.append(tuple(resolve_citation(fx.delivery_log, c, fx.scope).verdict_class for c in fx.citations))
            self.assertEqual(verdicts[0], verdicts[1])
            self.assertEqual(verdicts[0], verdicts[2])

    def test_gb01_f1_active_and_deprecated_compatibility_code(self) -> None:
        fx = build_gb06_fixture("W03-04-implementer-missing-artifact-active-code")
        self.assertEqual(fx.expected_code, "WORKER_SUMMARY_NO_ARTIFACT")
        self.assertEqual(fx.deprecated_compat_code, "WORKER_IMPLEMENT_NO_ARTIFACT")
        self.assertFalse(fx.expected_stored)

    def test_w04_artifact_provenance_separates_host_and_spoofed_uuid(self) -> None:
        fx = build_gb06_fixture("W04-06-artifact-provenance-host-vs-fixture")
        v_host = resolve_citation(fx.delivery_log, fx.citations[0], fx.scope)
        v_spoof = resolve_citation(fx.delivery_log, fx.citations[1], fx.scope)
        self.assertEqual(v_host.verdict_class, "grounded")
        self.assertEqual(v_spoof.verdict_class, "ungrounded")

    def test_full_runner_maps_all_32_base_slots(self) -> None:
        summary = validate_gb06_execution_map()
        self.assertEqual(summary["base_slots"], 32)
        self.assertEqual(summary["d5_runs"], 96)
        self.assertEqual(set(GB06_EXECUTION_SPECS_BY_SELECTOR.keys()), set(SLOTS_BY_SELECTOR.keys()))
        # Decision 031 W03-10 and GB01-F1 W03-04 must be worker slots
        w03_10 = GB06_EXECUTION_SPECS_BY_SELECTOR["W03-10-worker-undelivered-line-false-accept-contrast"]
        self.assertTrue(w03_10.is_worker_slot)
        self.assertTrue(w03_10.expect_refusal)
        w03_04 = GB06_EXECUTION_SPECS_BY_SELECTOR["W03-04-implementer-missing-artifact-active-code"]
        self.assertTrue(w03_04.is_worker_slot)
        self.assertEqual(w03_04.expected_codes, ("WORKER_SUMMARY_NO_ARTIFACT",))
        self.assertEqual(w03_04.deprecated_compat_code, "WORKER_IMPLEMENT_NO_ARTIFACT")

    def test_full_runner_sharding_partitions_96_d5_runs_disjointly(self) -> None:
        all_runs = select_gb06_runs()
        self.assertEqual(len(all_runs), 96)
        expected_pairs = {(spec.selector, w) for spec in ALL_GB06_SLOTS for w in WORDING_VARIANTS}
        self.assertEqual({(u.spec.selector, u.wording) for u in all_runs}, expected_pairs)

        for total_shards in (1, 2, 3, 4, 8):
            seen_pairs: set[tuple[str, str]] = set()
            selector_to_shard: dict[str, int] = {}
            for shard_idx in range(total_shards):
                shard_runs = select_gb06_runs(shard=shard_idx, shards=total_shards)
                for u in shard_runs:
                    pair = (u.spec.selector, u.wording)
                    self.assertNotIn(pair, seen_pairs, f"Duplicate pair {pair} in shards={total_shards}")
                    seen_pairs.add(pair)
                    if u.spec.selector in selector_to_shard:
                        self.assertEqual(
                            selector_to_shard[u.spec.selector],
                            shard_idx,
                            f"Slot {u.spec.selector} split across shards for shards={total_shards}",
                        )
                    else:
                        selector_to_shard[u.spec.selector] = shard_idx
            self.assertEqual(seen_pairs, expected_pairs)
            self.assertEqual(len(selector_to_shard), 32)

    def test_full_runner_filters_and_wording_invariance_checker(self) -> None:
        w03_only = select_gb06_runs(families=("W03",))
        self.assertEqual(len(w03_only), 30)  # 10 W03 slots x 3 wordings
        w01_conf = select_gb06_runs(families=("W01",), wordings=("confident",))
        self.assertEqual(len(w01_conf), 6)

        with self.assertRaises(ValueError):
            select_gb06_runs(shard=2, shards=2)
        with self.assertRaises(ValueError):
            select_gb06_runs(families=("NOPE",))

        invariant_rows = [
            {
                "selector": "W03-04-implementer-missing-artifact-active-code",
                "wording": w,
                "passed": True,
                "observed_codes": ["WORKER_SUMMARY_NO_ARTIFACT"],
                "stored_findings_count": 0,
                "worker_report_refusals": {"worker_report_refusals": 1, "finally_accepted": True},
            }
            for w in WORDING_VARIANTS
        ]
        inv = evaluate_wording_invariance(invariant_rows)
        self.assertEqual(len(inv), 1)
        self.assertTrue(inv[0]["checked"])
        self.assertTrue(inv[0]["invariant"])

        divergent_rows = list(invariant_rows)
        divergent_rows[2] = dict(divergent_rows[2], passed=False)
        inv_div = evaluate_wording_invariance(divergent_rows)
        self.assertFalse(inv_div[0]["invariant"])


if __name__ == "__main__":
    unittest.main()
