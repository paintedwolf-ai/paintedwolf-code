import unittest
from unittest.mock import patch

from ci_policy import verification_verdict as ci


class VerificationVerdictTests(unittest.TestCase):
    def test_aggregate_rejects_failure_cancellation_skip_and_missing_results(self):
        ci.require_success({"a": {"result": "success"}, "b": {"result": "success"}})
        for status in ["failure", "cancelled", "skipped", "", None]:
            with self.subTest(status=status), self.assertRaises(ValueError):
                ci.require_success({"a": {"result": "success"}, "b": {"result": status}})
        for results in [{}, [], {"a": {}}]:
            with self.assertRaises(ValueError):
                ci.require_success(results)

    def test_aggregate_accepts_only_the_skips_a_tier_declares(self):
        ci.require_success({"verification": {"result": "success"}, "platform": {"result": "skipped"}}, ["platform"])
        for platform, skipped in [("success", ["platform"]), ("failure", ["platform"]), ("skipped", [])]:
            with self.subTest(platform=platform, skipped=skipped), self.assertRaises(ValueError):
                ci.require_success({"verification": {"result": "success"}, "platform": {"result": platform}}, skipped)
        # Declaring a skip never excuses the jobs the tier does run.
        with self.assertRaises(ValueError):
            ci.require_success({"verification": {"result": "skipped"}, "platform": {"result": "skipped"}}, ["platform"])
        with self.assertRaises(ValueError):
            ci.require_success({"verification": {"result": "success"}}, ["platform"])

    def test_draft_pull_requests_never_pass_the_required_check(self):
        # Drafts skip verification, and a skipped required check would otherwise read as passing.
        for results in [{"verification": {"result": "skipped"}, "platform": {"result": "skipped"}},
                        {"verification": {"result": "success"}, "platform": {"result": "skipped"}}]:
            with self.subTest(results=results), self.assertRaisesRegex(ValueError, "ready for review"):
                ci.require_success(results, ["platform"], draft=True)

    def test_release_requires_a_complete_exact_source_dispatch_receipt(self):
        from ci_policy import qualification_receipt
        sha, controller = "a" * 40, "b" * 40
        run = dict(id=42, run_attempt=1, head_sha=controller, head_branch="main", conclusion="success",
                   event="workflow_dispatch", display_title=f"Qualification {sha}")
        proof = dict(version=1, repository="owner/repo", run_id=42, run_attempt=1, event="workflow_dispatch",
                     source_sha=sha, source_tree="c" * 40, controller_sha=controller,
                     checks={name: "success" for name in qualification_receipt.CHECKS})
        for changed in ({}, {"run_attempt": 2}, {"source_sha": controller}, {"controller_sha": sha},
                        {"repository": "foreign/repo"}, {"event": "push"}):
            with self.subTest(changed=changed), patch.object(ci, "pages", return_value=[run]), \
                    patch.object(qualification_receipt, "aggregate_passed", return_value=True), \
                    patch.object(qualification_receipt, "from_run", return_value={**proof, **changed}):
                if not changed:
                    self.assertEqual(ci.require_qualification("owner/repo", sha), run)
                else:
                    with self.assertRaises(ValueError):
                        ci.require_qualification("owner/repo", sha)
        for changed in ({"head_branch": "feature"}, {"conclusion": "failure"}, {"event": "push"},
                        {"event": "merge_group"}, {"display_title": "Qualification " + controller}):
            with self.subTest(changed=changed), patch.object(ci, "pages", return_value=[{**run, **changed}]), \
                    patch.object(qualification_receipt, "from_run") as read, self.assertRaises(ValueError):
                ci.require_qualification("owner/repo", sha)
            read.assert_not_called()
        for aggregate, receipt in [(False, proof), (True, None)]:
            with self.subTest(aggregate=aggregate), patch.object(ci, "pages", return_value=[run]), \
                    patch.object(qualification_receipt, "aggregate_passed", return_value=aggregate), \
                    patch.object(qualification_receipt, "from_run", return_value=receipt), self.assertRaises(ValueError):
                ci.require_qualification("owner/repo", sha)

    def test_dispatch_lookup_is_bounded_and_skips_unrelated_sources(self):
        from ci_policy import qualification_receipt
        from urllib.parse import parse_qs, urlparse
        sha = "a" * 40
        unrelated = dict(id=7, head_branch="main", conclusion="success", event="workflow_dispatch",
                         display_title="Qualification " + "b" * 40)
        with patch.object(ci, "pages", return_value=[unrelated]) as pages, \
                patch.object(qualification_receipt, "from_run") as read, \
                patch.object(qualification_receipt, "aggregate_passed") as jobs:
            self.assertIsNone(ci.qualified_run("owner/repo", sha))
            read.assert_not_called()
            jobs.assert_not_called()
            query = parse_qs(urlparse(pages.call_args.args[0]).query)
            self.assertEqual(query["event"], ["workflow_dispatch"])
            self.assertTrue(query["created"][0].startswith(">="))
            self.assertTrue(query["created"][0].endswith("Z"))
        for value in ("main", sha[:12], None):
            with self.subTest(value=value), self.assertRaises(ValueError):
                ci.qualified_run("owner/repo", value)
