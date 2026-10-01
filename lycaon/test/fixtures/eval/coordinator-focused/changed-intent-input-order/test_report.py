import unittest
from report import summarize

class Contract(unittest.TestCase):
    def test_retained_behavior(self):
        rows=[{"name":"a","active":True},{"name":"b","active":True}]
        self.assertEqual(summarize(rows), rows)
        self.assertEqual(summarize(rows,limit=0), [])
