"""Only obsolete background sources can be canceled by the main controller."""
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import qualification_supersession as supersession

TIP = "a" * 40
OLD = "b" * 40
ENV = dict(GITHUB_EVENT_NAME="push", GITHUB_REF="refs/heads/main", GITHUB_REPOSITORY="owner/repo")
RUN = dict(id=42, event="push", head_branch="main", path=supersession.WORKFLOW,
           status="in_progress", head_sha=OLD, head_repository={"full_name": "owner/repo"})


class SupersessionTests(unittest.TestCase):
    def execute(self, run=None, refreshed=None, ancestor=0, listed=None):
        run = RUN if run is None else run
        refreshed = run if refreshed is None else refreshed
        with patch.object(supersession, "api", side_effect=[{"object": {"sha": TIP}}, refreshed, None]) as api, \
                patch.object(supersession, "pages", side_effect=[listed if listed is not None else [run], [], [], [], []]), \
                patch.object(supersession.subprocess, "run", side_effect=[
                    subprocess.CompletedProcess([], 0), subprocess.CompletedProcess([], ancestor)]) as git:
            result = supersession.reconcile(ENV)
            return result, api.call_args_list, git.call_args_list

    def test_cancels_only_the_obsolete_main_snapshot(self):
        result, calls, commands = self.execute()
        self.assertEqual(result["cancelled"], [{"run_id": 42, "source_sha": OLD}])
        self.assertEqual(calls[-1].args, ("repos/owner/repo/actions/runs/42/cancel", "POST"))
        self.assertEqual(commands[-1].args[0], ["git", "merge-base", "--is-ancestor", OLD, TIP])
        for status in supersession.ACTIVE:
            self.assertTrue(supersession.obsolete({**RUN, "status": status}, "owner/repo", TIP))

    def test_preserves_candidates_current_tip_and_foreign_or_finished_runs(self):
        for changed in ({"event": "workflow_dispatch"}, {"head_sha": TIP}, {"status": "completed"},
                        {"path": ".github/workflows/release.yml"}, {"head_branch": "feature"},
                        {"head_repository": {"full_name": "foreign/repo"}}, {"id": True}, {"id": -1},
                        {"head_sha": "invalid"}):
            with self.subTest(changed=changed):
                result, calls, _ = self.execute({**RUN, **changed})
                self.assertEqual(result["cancelled"], [])
                self.assertEqual(len(calls), 1)

    def test_rechecks_run_before_cancel_and_deduplicates_status_transitions(self):
        for changed in ({"status": "completed"}, {"event": "workflow_dispatch"}, {"head_sha": "c" * 40}):
            with self.subTest(changed=changed):
                result, calls, _ = self.execute(refreshed={**RUN, **changed})
                self.assertEqual(result["cancelled"], [])
                self.assertEqual(len(calls), 2)
        result, calls, _ = self.execute(listed=[RUN, RUN])
        self.assertEqual(len(result["cancelled"]), 1)
        self.assertEqual(len(calls), 3)

    def test_unrelated_commits_stay_and_git_errors_fail_closed(self):
        result, calls, _ = self.execute(ancestor=1)
        self.assertEqual(result["cancelled"], [])
        self.assertEqual(len(calls), 1)
        with self.assertRaises(subprocess.CalledProcessError):
            self.execute(ancestor=128)
        for changed in ({"GITHUB_EVENT_NAME": "pull_request"}, {"GITHUB_REF": "refs/heads/feature"}):
            with patch.object(supersession, "api") as api, self.assertRaises(ValueError):
                supersession.reconcile({**ENV, **changed})
            api.assert_not_called()

    def test_main_records_cancellation_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            summary = Path(directory) / "summary"
            record = {"cancelled": [{"run_id": 42}]}
            with patch.dict(supersession.os.environ, {"GITHUB_STEP_SUMMARY": str(summary)}), \
                    patch.object(supersession, "reconcile", return_value=record), patch("builtins.print") as output:
                supersession.main()
            self.assertIn('"run_id": 42', summary.read_text())
            self.assertEqual(json.loads(output.call_args.args[0]), record)
