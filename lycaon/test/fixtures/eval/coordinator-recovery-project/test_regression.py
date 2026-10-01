import unittest
from ranking import rank_profiles


class RankingRegression(unittest.TestCase):
    def test_highest_score_with_alphabetical_ties(self):
        records = [{"name": "coral", "score": 93}, {"name": "amber", "score": 93}, {"name": "blue", "score": 42}]
        self.assertEqual([r["name"] for r in rank_profiles(records)], ["amber", "coral", "blue"])
