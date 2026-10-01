import unittest
from distinct import unique
class Contract(unittest.TestCase):
    def test_unique(self):
        self.assertEqual(unique(["a", "a", "b"]), ["a", "b"])
        self.assertEqual(unique([]), [])
