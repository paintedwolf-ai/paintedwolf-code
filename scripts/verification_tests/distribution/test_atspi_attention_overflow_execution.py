"""The regular native probe reaches the overflow without earlier release state."""

import json
from pathlib import Path
import subprocess
import unittest

from harness.webkit_atspi_scenarios import HOME_OVERFLOW, diagnostic_scenario


class AtspiAttentionOverflowTests(unittest.TestCase):
    def scenario(self, failure=""):
        # The fixture executes the same page body used by the native GI probe.
        script = """
let sessions = 0;
const failure = FAILURE;
global.__harness = {
  openProject: async () => ({ok: failure !== 'project'}),
  newSession: async () => { sessions++; return {ok: failure !== 'session'}; },
  goto: async () => ({ok: failure !== 'checkpoint'}),
  goHome: async () => ({ok: failure !== 'home'}),
};
global.document = {
  querySelector: () => failure !== 'overflow' && sessions === 8 ? {textContent: 'and 4 more'} : null,
  querySelectorAll: () => Array(4),
};
global.setTimeout = callback => callback();
(async () => {
  const result = await (new Function('return (async () => {' + BODY + '})()'))();
  console.log(JSON.stringify({result, sessions}));
})().catch(error => { console.error(error); process.exitCode = 1; });
""".replace("FAILURE", json.dumps(failure)).replace("BODY", json.dumps(HOME_OVERFLOW))
        result = subprocess.run(["node", "-e", script], capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def test_fresh_probe_seeds_distinct_waiting_chats_and_asserts_overflow(self):
        value = self.scenario()
        self.assertEqual(value["sessions"], 8)
        self.assertEqual(value["result"], {"ok": True, "attention": {"seeded": 8, "shown": 4, "overflow": "and 4 more"}})

    def test_missing_setup_or_overflow_is_unverified_instead_of_a_pass(self):
        for failure in ("project", "session", "checkpoint", "home", "overflow"):
            with self.subTest(failure=failure):
                self.assertFalse(self.scenario(failure)["result"]["ok"])

    def test_native_probe_imports_the_required_scenario(self):
        probe = Path(__file__).resolve().parents[2] / "harness/webkit_atspi_text_attributes.py"
        self.assertIn('(\"home\", HOME_OVERFLOW)', probe.read_text())

    def test_diagnostic_preserves_scenario_outcome_and_captures_hidden_selection(self):
        body = diagnostic_scenario("return {ok: true};")
        script = r"""
const anchor = {nodeType: 1, tagName: 'DIV', dataset: {testid: 'editor'},
  className: 'cm-content', isConnected: true, getClientRects: () => [], closest: () => ({})};
global.document = {getSelection: () => ({rangeCount: 1, isCollapsed: true,
  anchorNode: anchor, focusNode: anchor, anchorOffset: 2, focusOffset: 2})};
global.getComputedStyle = () => ({display: 'block', visibility: 'visible'});
(async () => {
  const value = await (new Function('return (async () => {' + BODY + '})()'))();
  console.log(JSON.stringify(value));
})().catch(error => { console.error(error); process.exitCode = 1; });
""".replace("BODY", json.dumps(body))
        result = subprocess.run(["node", "-e", script], capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        value = json.loads(result.stdout)
        self.assertTrue(value["ok"])
        selection = value["diagnostics"]["selection"]
        self.assertEqual(selection["ranges"], 1)
        self.assertEqual(selection["anchor"]["boxes"], 0)
        self.assertTrue(selection["anchor"]["hiddenAncestor"])
