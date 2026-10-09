from pathlib import Path
import unittest


class SharedDCOIntegrationTests(unittest.TestCase):
    def test_consumer_uses_pinned_shared_action_without_pr_code_or_artifacts(self):
        workflow = (Path(__file__).parents[2] / ".github/workflows/dco.yml").read_text()
        self.assertRegex(workflow, r"uses: paintedwolf-ai/dco-checker@[0-9a-f]{40}(?:\s|$)")
        self.assertNotIn("actions/checkout", workflow)
        self.assertNotIn("download-artifact", workflow)
        self.assertNotRegex(workflow, r"(?m)^\s+run:")
        self.assertNotIn("github.event.pull_request.head", workflow)

    def test_consumer_preserves_privileged_event_filters_and_draft_guard(self):
        workflow = (Path(__file__).parents[2] / ".github/workflows/dco.yml").read_text()
        self.assertIn("pull_request_target:", workflow)
        self.assertIn("ready_for_review, edited", workflow)
        self.assertIn("workflow_run:", workflow)
        self.assertIn("workflows: [CI]", workflow)
        self.assertIn("types: [completed]", workflow)
        self.assertIn("merge_group:", workflow)
        self.assertIn("checks: write", workflow)
        self.assertIn("actions: read", workflow)
        self.assertIn("pull-requests: read", workflow)
        self.assertIn("github.event.pull_request.draft == false", workflow)
        self.assertIn("github.event.workflow_run.event == 'pull_request'", workflow)
