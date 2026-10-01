import unittest
from ranking import rank_profiles
from assembly import format_report


class ReportTests(unittest.TestCase):
    def test_format_empty(self):
        self.assertIn("Total: 0", format_report([]))

    def test_rank_does_not_mutate(self):
        rows = [{"name": "blue", "score": 42}, {"name": "amber", "score": 93}]
        original = list(rows)
        rank_profiles(rows)
        self.assertEqual(rows, original)


if __name__ == "__main__":
    unittest.main()
