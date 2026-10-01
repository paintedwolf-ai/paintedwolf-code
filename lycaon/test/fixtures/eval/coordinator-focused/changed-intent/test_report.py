import unittest
from report import summarize
class ReportTest(unittest.TestCase):
    def test_retained_behavior(self):
        records = [{"name":"b","active":True},{"name":"A","active":True},{"name":"a","active":True}]
        self.assertEqual(summarize(records), [records[1], records[2]])
        self.assertEqual(summarize(records, 1), [records[1]])
        self.assertEqual(summarize(records, 0), [])
        self.assertEqual(records[0]["name"], "b")
