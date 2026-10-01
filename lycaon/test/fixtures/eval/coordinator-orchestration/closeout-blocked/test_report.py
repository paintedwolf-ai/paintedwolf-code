import unittest
from report import select
class Contract(unittest.TestCase):
    def test_default_limit_and_order(self):
        rows=[{"name":"a","score":1},{"name":"b","score":4},{"name":"c","score":2},{"name":"d","score":3}]
        self.assertEqual(select(rows, 3), [rows[1],rows[3],rows[2]])
        self.assertEqual(select(rows, 1), [rows[1]])
        self.assertEqual(rows[0], {"name":"a","score":1})
