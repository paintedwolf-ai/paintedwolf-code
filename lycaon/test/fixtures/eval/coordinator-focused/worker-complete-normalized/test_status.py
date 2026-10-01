import unittest
from status import display_status
class Contract(unittest.TestCase):
    def test_behavior(self):
        self.assertEqual(display_status(" DONE "), "Complete")
