import unittest
from report import select

class Contract(unittest.TestCase):
    def test_retained_behavior(self):
        rows=[{"name":"a","score":1},{"name":"b","score":2}]
        self.assertEqual(select(rows), list(reversed(rows)))
