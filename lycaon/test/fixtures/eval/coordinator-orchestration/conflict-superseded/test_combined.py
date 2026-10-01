import unittest
from catalog import select_records
class CombinedTest(unittest.TestCase):
    def test_filter_before_page(self):
        rows = [{"name":"other"},{"name":"alpha"},{"name":"Beta"},{"name":"ALPINE"}]
        self.assertEqual(select_records(rows, "al", 1, 1), [rows[3]])
