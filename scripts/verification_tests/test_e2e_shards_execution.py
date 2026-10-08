import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("e2e_shards", Path(__file__).resolve().parents[1] / "e2e/deal-test-files.py")
shards = importlib.util.module_from_spec(spec)
spec.loader.exec_module(shards)


class E2EShardsTests(unittest.TestCase):
    def test_each_file_and_its_nested_locations_belong_to_exactly_one_shard(self):
        listing = {"suites": [
            {"file": f"spec-{number}.ts", "suites": [{"specs": [{"file": f"helper-{number}.ts"}]}]}
            for number in range(7)
        ]}
        selection = shards.deal(listing, 3)
        self.assertEqual(selection[0], ["spec-0.ts", "helper-0.ts", "spec-3.ts", "helper-3.ts", "spec-6.ts", "helper-6.ts"])
        files = [file for group in selection for file in group]
        self.assertEqual(len(files), 14)
        self.assertEqual(len(set(files)), 14)
        self.assertEqual(shards.deal({"suites": list(reversed(listing['suites']))}, 3), selection)

    def test_narrow_selection_leaves_empty_shards_without_expanding_the_suite(self):
        self.assertEqual(shards.deal({"suites": [{"file": "one.ts"}]}, 3), [["one.ts"], [], []])

    def test_specs_sharing_a_location_share_a_shard(self):
        listing = {"suites": [
            {"file": "a.ts"},
            {"file": "b.ts", "suites": [{"specs": [{"file": "journey.ts"}]}]},
            {"file": "c.ts"},
            {"file": "d.ts", "specs": [{"file": "journey.ts"}]},
        ]}
        self.assertEqual(shards.deal(listing, 3), [["a.ts"], ["b.ts", "d.ts", "journey.ts"], ["c.ts"]])

    def test_invalid_listings_cannot_report_partitioned_coverage(self):
        for listing, count in [
            ({"errors": [{"message": "discovery failed"}]}, 2),
            ({"suites": []}, 2),
            ({"suites": [{"file": "one.ts"}]}, 0),
        ]:
            with self.subTest(listing=listing, count=count), self.assertRaises(ValueError):
                shards.deal(listing, count)
