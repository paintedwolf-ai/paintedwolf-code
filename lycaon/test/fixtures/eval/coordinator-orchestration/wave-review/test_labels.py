import unittest
from labels import display_status
class Contract(unittest.TestCase):
    def test_labels(self):
        self.assertEqual([display_status(x) for x in ["pending", "active", "done", "other"]], ["Waiting", "Running", "Complete", "Unknown"])
