import unittest
from consumer import select
class Contract(unittest.TestCase):
    def test_select(self):
        self.assertEqual(select([" A ","a","B"," b ","a"]), ["a","b"])
        self.assertEqual(select([" Beta ","alpha","BETA"]), ["beta","alpha"])
        self.assertEqual(select(["Straße","STRASSE"]), ["strasse"])
        self.assertEqual(select([]), [])
