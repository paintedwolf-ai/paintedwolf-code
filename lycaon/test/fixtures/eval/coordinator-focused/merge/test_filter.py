import unittest
from catalog import select_records
class FilterTest(unittest.TestCase):
    def test_filter(self):
        rows = [{"name":"alpha"},{"name":"Beta"},{"name":"ALPINE"}]
        self.assertEqual(select_records(rows, "AL"), [rows[0],rows[2]])
