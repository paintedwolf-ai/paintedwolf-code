"""Budget tracking remains independent of reporter intake and stale closure."""
from pathlib import Path
import subprocess
import unittest


class IssuePolicyTests(unittest.TestCase):
    def test_only_bot_authored_budget_markers_bypass_reporter_lifecycle(self):
        source = r'''
const assert = require("node:assert/strict");
const { isExempt, markStale } = require("./.github/scripts/issue-policy.cjs");
const tracked = { number: 1, user: { login: "github-actions[bot]" },
  body: "<!-- paintedwolf-maintainability:v1 -->", labels: [],
  updated_at: "2000-01-01T00:00:00Z" };
assert.equal(isExempt(tracked), true);
assert.equal(isExempt({ ...tracked, user: { login: "someone" } }), false);
assert.equal(isExempt({ ...tracked, body: "ordinary automated report" }), false);
const writes = [];
const github = { paginate: async () => [tracked], rest: { issues: {
  listForRepo: {}, addLabels: async (...args) => writes.push(args),
  createComment: async (...args) => writes.push(args),
} } };
markStale({ github, context: { repo: { owner: "owner", repo: "repo" } },
  core: { info: () => {} } }).then(() => assert.deepEqual(writes, []));
'''
        result = subprocess.run(['node', '-e', source], cwd=Path(__file__).resolve().parents[2],
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
