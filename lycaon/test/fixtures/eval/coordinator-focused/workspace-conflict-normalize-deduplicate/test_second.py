import unittest
from catalog import select_records
class Contract(unittest.TestCase):
    def test_behavior(self):
        self.assertEqual(select_records(["a","b","a"]), ["a","b"])
