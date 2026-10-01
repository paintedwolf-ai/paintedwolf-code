import unittest
from reconcile import reconcile
class ReconcileTest(unittest.TestCase):
    def test_default(self):
        self.assertEqual(reconcile([{"id":"x","account":"a","cents":0}]), [{"account":"a","cents":0}])
