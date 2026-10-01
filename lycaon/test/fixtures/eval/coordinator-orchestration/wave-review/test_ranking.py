import unittest
from ranking import rank
class Contract(unittest.TestCase):
    def test_rank(self):
        rows=[{"name":"b","score":1},{"name":"a","score":1},{"name":"c","score":2}]
        self.assertEqual(rank(rows), [rows[2],rows[1],rows[0]])
        self.assertEqual(rank([]), [])
        self.assertEqual(rows[0], {"name":"b","score":1})
