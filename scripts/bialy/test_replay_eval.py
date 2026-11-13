import unittest

import replay_eval
from corpus import SCHEMA, Corpus


def corpus(tools):
    return Corpus({"schema": SCHEMA, "catalog_revision": "r1", "surfaces": [],
                   "questions": {"tools": {"load_at": 0.5}, "kind": {"confidence_floor": 0.5, "instructions": "", "options": {}}},
                   "tools": [{"name": name, "description": name, "card": name} for name in tools]})


class ReplayEval(unittest.TestCase):
    def test_saved_bytes_are_counted_from_tool_schemas_alone(self):
        sizes = replay_eval.load_sizes(corpus(["command", "no_such_tool"]))
        self.assertEqual(set(sizes), {"command"})
        self.assertGreater(sizes["command"], 0)

    def test_the_report_has_no_guidance_section(self):
        report = replay_eval.score([], None, corpus(["command"]), {"command": 10}, None, "consensus", timed=False)
        self.assertEqual(report["turns"], 0)
        self.assertNotIn("guides", report)
        self.assertIn("tools", report)


if __name__ == "__main__":
    unittest.main()
