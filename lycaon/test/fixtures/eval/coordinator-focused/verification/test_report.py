import unittest
from report import select

class ReportTest(unittest.TestCase):
    def test_explicit_limits_and_order(self):
        records = [{"name":"b","score":1},{"name":"a","score":1},{"name":"c","score":2}]
        self.assertEqual(select(records, 2), [records[2], records[1]])
        self.assertEqual(select(records, 0), [])
        self.assertEqual(select([], 2), [])
        self.assertEqual(records[0], {"name":"b","score":1})
