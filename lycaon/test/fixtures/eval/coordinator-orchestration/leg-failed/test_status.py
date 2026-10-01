import unittest
from status import display_status
class Contract(unittest.TestCase):
    def test_status_labels(self):
        for status, label in [("pending","Waiting"),("active","Running"),("done","Complete")]:
            self.assertEqual(display_status(status), label)
