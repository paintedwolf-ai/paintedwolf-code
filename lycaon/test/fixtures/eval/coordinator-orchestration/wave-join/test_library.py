import unittest
from library import normalize
class Contract(unittest.TestCase):
    def test_normalize(self):
        self.assertEqual(normalize(" alpha "), "alpha")
        self.assertEqual(normalize(""), "")
