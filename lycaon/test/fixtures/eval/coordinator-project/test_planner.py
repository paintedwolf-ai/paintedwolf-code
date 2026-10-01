import unittest
from planner import build_plan


class ExistingTests(unittest.TestCase):
    def test_empty(self):
        self.assertEqual(build_plan([]), {"stages": []})

    def test_input_preserved(self):
        jobs = [{"id": "a", "priority": 1, "depends_on": []}]
        before = repr(jobs)
        build_plan(jobs)
        self.assertEqual(repr(jobs), before)
