import unittest
from catalog import select_records
class Contract(unittest.TestCase):
    def test_filter(self):
        rows = [{"name":"Beta"}, {"name":"alpha"}, {"name":"ALPINE"}]
        self.assertEqual(select_records(rows, "al"), rows[1:])
        self.assertEqual(select_records(rows), rows)
