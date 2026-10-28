"""Batch preparation pins every source and leaves admission to the normal CI gate."""
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import batch_prs as batch

BASE = "a" * 40
ROWS = [dict(number=number, url=f"https://github.com/owner/repo/pull/{number}", state="OPEN",
             baseRefName="main", headRefName=f"feature-{number}", headRefOid=str(number) * 40,
             isDraft=False, isCrossRepository=False, id=f"PR_{number}",
             mergeQueueEntry=None, autoMergeRequest=None) for number in (1, 2)]
URL = "https://github.com/owner/repo/pull/3"


class BatchTests(unittest.TestCase):
    def test_sources_need_no_risk_classification_or_review(self):
        for changed in ({}, {"isCrossRepository": True}, {"isDraft": True}, {"baseRefName": "dependency"}):
            row = {**ROWS[0], **changed}
            with patch.object(batch, "api", return_value={"data": {"repository": {"pullRequest": row}}}):
                self.assertEqual(batch.source("owner/repo", 1), row)
        for changed in ({"state": "CLOSED"}, {"number": 2}, {"headRefOid": "main"}):
            with self.subTest(changed=changed), patch.object(batch, "api", return_value={"data": {"repository": {"pullRequest": {**ROWS[0], **changed}}}}), \
                    self.assertRaises(ValueError):
                batch.source("owner/repo", 1)

    def prepare(self, directory, rows=ROWS, changed=None, conflict=False):
        calls = []

        def execute(arguments, cwd=batch.ROOT):
            calls.append((arguments, cwd))
            if arguments[:2] == ["git", "ls-remote"]:
                return BASE + "\trefs/heads/main"
            if arguments[:3] == ["gh", "pr", "create"]:
                return URL
            if conflict and arguments[:2] == ["git", "merge"]:
                raise subprocess.CalledProcessError(1, arguments)
            return ""

        refreshed = rows if changed is None else [changed, rows[1]]
        return (patch.object(batch, "command", side_effect=execute),
                patch.object(batch, "source", side_effect=[*rows, *refreshed]),
                patch.object(batch.tempfile, "mkdtemp", return_value=directory), calls)

    def test_combines_exact_heads_then_disables_individual_auto_merge_and_starts_ci(self):
        with tempfile.TemporaryDirectory() as directory:
            commands, sources, location, calls = self.prepare(directory)
            with commands, sources, location, patch("builtins.print"), patch.object(batch, "withdraw") as withdraw:
                self.assertEqual(batch.create("owner/repo", [1, 2]), URL)
            argv = [call[0] for call in calls]
            merges = [row for row in argv if row[:2] == ["git", "merge"]]
            self.assertEqual([row[-1] for row in merges], [row["headRefOid"] for row in ROWS])
            self.assertTrue(all("--signoff" in row for row in merges))
            create = next(row for row in argv if row[:3] == ["gh", "pr", "create"])
            self.assertIn("--draft", create)
            self.assertIn("--body-file", create)
            self.assertEqual(argv[-1], ["gh", "pr", "ready", URL])
            self.assertEqual(withdraw.call_count, 2)
            description = (Path(directory) / "body.md").read_text()
            for row in ROWS:
                self.assertIn(row["url"], description)
                self.assertIn(row["headRefOid"], description)
                self.assertNotIn(f"Closes #{row['number']}", description)
            self.assertIn(BASE, description)
            self.assertFalse(any("--auto" in row for row in argv))

    def test_conflict_or_changed_source_never_pushes(self):
        for options in ({"conflict": True}, {"changed": {**ROWS[0], "headRefOid": "f" * 40}},
                        {"changed": {**ROWS[0], "isDraft": True}}):
            with self.subTest(options=options), tempfile.TemporaryDirectory() as directory:
                commands, sources, location, calls = self.prepare(directory, **options)
                with commands, sources, location, patch("builtins.print"), \
                        self.assertRaises((ValueError, subprocess.CalledProcessError)):
                    batch.create("owner/repo", [1, 2])
                self.assertFalse(any(argv[:2] == ["git", "push"] for argv, _ in calls))
                self.assertFalse(any(argv[:3] == ["gh", "pr", "merge"] for argv, _ in calls))

    def test_incomplete_sources_keep_the_batch_draft(self):
        with tempfile.TemporaryDirectory() as directory:
            rows = [{**ROWS[0], "isDraft": True}, ROWS[1]]
            commands, sources, location, calls = self.prepare(directory, rows=rows)
            with commands, sources, location, patch("builtins.print"), patch.object(batch, "withdraw") as withdraw:
                batch.create("owner/repo", [1, 2])
            self.assertFalse(any(argv[:3] == ["gh", "pr", "ready"] for argv, _ in calls))

    def test_rejects_invalid_lists_before_any_effect_and_validates_main(self):
        for numbers in ([1], [1, 1], [0, 2]):
            with patch.object(batch, "command") as command, self.assertRaises(ValueError):
                batch.create("owner/repo", numbers)
            command.assert_not_called()
        with patch.object(batch, "source", side_effect=ROWS), \
                patch.object(batch, "command", return_value="invalid\trefs/heads/main"), self.assertRaises(ValueError):
            batch.create("owner/repo", [1, 2])

    def test_entrypoint_and_subprocess_use_structured_arguments(self):
        with patch.object(batch.subprocess, "check_output", return_value=" output\n") as process:
            self.assertEqual(batch.command(["git", "status"]), "output")
            process.assert_called_once_with(["git", "status"], cwd=batch.ROOT, text=True)
        with patch("sys.argv", ["batch_prs", "1", "2"]), \
                patch.object(batch, "command", return_value="owner/repo"), patch.object(batch, "create") as create:
            batch.main()
            create.assert_called_once_with("owner/repo", [1, 2])

    def test_queue_withdrawal_handles_queued_pending_and_unqueued_sources(self):
        queued = {**ROWS[0], "mergeQueueEntry": {"id": "Q_1"}}
        waiting = {**ROWS[0], "autoMergeRequest": {"enabledAt": "now"}}
        with patch.object(batch, "source", side_effect=[queued, waiting]), patch.object(batch, "api") as api:
            batch.withdraw("owner/repo", ROWS[0])
            self.assertEqual(api.call_count, 2)
            dequeue = api.call_args_list[0].args[2]
            self.assertIn("dequeuePullRequest(input: {id: $id})", dequeue["query"])
            self.assertEqual(dequeue["variables"], {"id": queued["id"]})
            self.assertIn("disablePullRequestAutoMerge", api.call_args_list[1].args[2]["query"])
        with patch.object(batch, "source", return_value=ROWS[0]), patch.object(batch, "api") as api:
            batch.withdraw("owner/repo", ROWS[0])
            api.assert_not_called()
        with patch.object(batch, "source", return_value={**ROWS[0], "headRefOid": "f" * 40}), \
                patch.object(batch, "api") as api, self.assertRaises(ValueError):
            batch.withdraw("owner/repo", ROWS[0])
            api.assert_not_called()
