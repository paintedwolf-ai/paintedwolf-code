import unittest
from catalog import select_records
class PageTest(unittest.TestCase):
    def test_page(self):
        rows = [{"name":"alpha"},{"name":"Beta"},{"name":"ALPINE"}]
        self.assertEqual(select_records(rows, offset=1, limit=1), [rows[1]])
        self.assertEqual(select_records(rows, limit=0), [])
