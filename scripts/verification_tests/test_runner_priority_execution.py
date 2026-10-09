import copy
from datetime import datetime, timedelta, timezone
import subprocess
import unittest
from unittest.mock import patch

import runner_priority as rp
import verification_plan as planning


NOW = datetime(2026, 10, 8, 12, 0, tzinfo=timezone.utc)
LABELS = {"linux": "ubuntu-latest", "macos": "macos-15"}
GROUP = "gh-readonly-queue/main/pr-2-b"


def stamp(minutes):
    return (NOW - timedelta(minutes=minutes)).isoformat().replace("+00:00", "Z")


def run(number, workflow, event, *, branch="main", sha=None, status="in_progress", conclusion=None, attempt=1, age=10):
    return {"id": number, "path": f".github/workflows/{workflow}", "event": event, "head_branch": branch,
            "head_sha": sha or f"sha-{number}", "status": status, "conclusion": conclusion,
            "run_attempt": attempt, "created_at": stamp(age)}


def jobs(running=(), queued=(), age=0):
    return ([{"status": "in_progress", "labels": [LABELS[name]], "created_at": stamp(age)} for name in running]
            + [{"status": "queued", "labels": [LABELS[name]], "created_at": stamp(age)} for name in queued])


def cancelled(number, workflow, event, **fields):
    return run(number, workflow, event, status="completed", conclusion="cancelled", **fields)


class Repository:
    """GitHub's REST answers for one repository, recording every call the scheduler makes."""

    def __init__(self, runs=(), jobs=None, groups=(), pulls=(), history=None, refused=()):
        self.runs, self.jobs, self.groups, self.pulls = list(runs), jobs or {}, groups, pulls
        self.history, self.refused, self.calls = history or {}, set(refused), []

    def __call__(self, path, method="GET", **query):
        self.calls.append((method, path, query))
        if method == "POST":
            if int(path.split("/")[-2]) in self.refused:
                raise subprocess.CalledProcessError(1, ["gh"], stderr="HTTP 403")
            return None
        if path == "repos/owner/repo/actions/runs":
            return {"workflow_runs": [item for item in self.runs if item["status"] == query["status"]]}
        if "matching-refs" in path:
            return [{"ref": f"refs/heads/{group}"} for group in self.groups]
        if path.endswith("/pulls"):
            return [{"head": {"sha": sha}, "draft": draft} for sha, draft in self.pulls]
        if path.endswith("/jobs"):
            return {"jobs": self.jobs.get(int(path.split("/")[-2]), [])}
        workflow = path.split("/")[-2]
        return {"workflow_runs": self.history.get((workflow, query["event"]), [])[:query["per_page"]]}

    def posted(self, action):
        return [int(path.split("/")[-2]) for method, path, _ in self.calls if method == "POST" and path.endswith(action)]

    def schedule(self):
        with patch("builtins.print"):
            return rp.schedule("owner/repo", self, NOW)


class RunnerPriorityTests(unittest.TestCase):
    def test_prune_cancels_only_runs_whose_merge_group_is_gone(self):
        gone = "gh-readonly-queue/main/pr-1-a"
        repository = Repository(runs=[run(1, "ci.yml", "merge_group", branch=gone, status="queued"),
                                      run(2, "ci.yml", "merge_group", branch=GROUP)],
                                jobs={2: jobs(running=["linux"])}, groups=[GROUP])
        self.assertEqual(repository.schedule(), ([1], []))
        self.assertEqual(repository.posted("/force-cancel"), [1])
        # Runs are listed before branches, so a group created in between is treated as live.
        listed = [index for index, call in enumerate(repository.calls) if call[1] == "repos/owner/repo/actions/runs"]
        branches = next(index for index, call in enumerate(repository.calls) if "matching-refs" in call[1])
        self.assertLess(max(listed), branches)
        self.assertEqual({repository.calls[index][2]["status"] for index in listed}, set(rp.UNFINISHED_RUNS))

    def test_waiting_macos_merge_job_takes_only_macos_runners_from_the_lowest_priority(self):
        repository = Repository(
            runs=[run(1, "ci.yml", "merge_group", branch=GROUP), run(3, "build-caches.yml", "push"),
                  run(4, "ci.yml", "workflow_dispatch"), run(5, "ci.yml", "pull_request", sha="ready")],
            jobs={1: jobs(running=["linux"] * 8, queued=["macos"]), 3: jobs(running=["macos", "macos", "linux"]),
                  4: jobs(running=["macos"] * 3), 5: jobs(running=["linux"] * 4)},
            groups=[GROUP], pulls=[("ready", False)])
        self.assertEqual(repository.schedule(), ([3], []))
        self.assertEqual(repository.posted("/rerun-failed-jobs"), [])

    def test_waiting_linux_merge_jobs_preempt_in_reverse_priority_and_newest_first(self):
        repository = Repository(
            runs=[run(1, "ci.yml", "merge_group", branch=GROUP), run(2, "release.yml", "push", branch="v1.2.0"),
                  run(3, "build-caches.yml", "push"), run(4, "ci.yml", "pull_request", sha="draft"),
                  run(5, "ci.yml", "pull_request", sha="old", age=30), run(6, "ci.yml", "pull_request", sha="new", age=5),
                  run(7, "ci.yml", "pull_request", sha="newest", age=1), run(8, "ci.yml", "workflow_dispatch")],
            jobs={1: jobs(running=["linux"] * 2, queued=["linux"] * 6), 2: jobs(running=["macos", "linux"]),
                  3: jobs(running=["linux"]), 4: jobs(running=["linux"]), 5: jobs(running=["linux"] * 4),
                  6: jobs(running=["linux"] * 4), 7: jobs(queued=["linux"] * 3), 8: jobs(running=["linux"] * 6)},
            groups=[GROUP], pulls=[("draft", True), ("old", False), ("new", False), ("newest", False)])
        # Warming and the draft free one runner each; the newest ready run only waits, but would take the next
        # free runner; the next newest frees the rest. Releases, live groups, and dispatched CI are never cancelled.
        self.assertEqual(repository.schedule(), ([3, 4, 7, 6], []))

    def test_waiting_jobs_preempt_nothing_while_runners_are_free_until_they_starve(self):
        for age, expected in [(1, []), (rp.STARVED_AFTER.total_seconds() // 60 + 1, [2])]:
            with self.subTest(age=age):
                repository = Repository(
                    runs=[run(1, "ci.yml", "merge_group", branch=GROUP), run(2, "ci.yml", "pull_request", sha="ready")],
                    jobs={1: jobs(queued=["linux"] * 2, age=age), 2: jobs(running=["linux"] * 4)},
                    groups=[GROUP], pulls=[("ready", False)])
                # Other repositories share the plan, so a long wait counts even when this repository leaves room.
                self.assertEqual(repository.schedule(), (expected, []))

    def test_background_work_yields_while_the_merge_queue_has_groups(self):
        runs = [run(1, "ci.yml", "merge_group", branch=GROUP), run(2, "nightly.yml", "schedule", age=20),
                run(3, "dependency-inventory.yml", "workflow_dispatch", age=5), run(4, "issue-intake.yml", "issues"),
                run(5, "ci.yml", "pull_request", sha="ready")]
        for groups, expected in [([GROUP], [3, 2]), ([], [])]:
            with self.subTest(groups=groups):
                repository = Repository(runs=runs if groups else runs[1:], jobs={1: jobs(running=["linux"])},
                                        groups=groups, pulls=[("ready", False)])
                cancelled_runs, _ = repository.schedule()
                self.assertEqual(cancelled_runs, expected)

    def test_preempted_work_resumes_once_nothing_outranks_it(self):
        history = {
            ("ci.yml", "pull_request"): [
                cancelled(12, "ci.yml", "pull_request", sha="ready", attempt=2, age=5),
                cancelled(10, "ci.yml", "pull_request", sha="ready", age=50),
                cancelled(13, "ci.yml", "pull_request", sha="draft"),
                cancelled(14, "ci.yml", "pull_request", sha="exhausted", attempt=rp.ATTEMPTS),
                cancelled(15, "ci.yml", "pull_request", sha="closed"),
                run(16, "ci.yml", "pull_request", sha="superseded", status="completed", conclusion="success", age=1),
                cancelled(17, "ci.yml", "pull_request", sha="superseded", age=9)],
            ("build-caches.yml", "push"): [cancelled(20, "build-caches.yml", "push")],
            ("nightly.yml", "schedule"): [cancelled(21, "nightly.yml", "schedule")],
            ("dependency-inventory.yml", "schedule"): [
                run(22, "dependency-inventory.yml", "schedule", status="completed", conclusion="success")],
        }
        pulls = [("ready", False), ("draft", True), ("exhausted", False), ("superseded", False)]
        cases = [([], [], {12, 20, 21}),
                 # Background work waits for an empty queue; the rest only for no waiting jobs.
                 ([GROUP], jobs(running=["linux"]), {12, 20}),
                 ([GROUP], jobs(queued=["linux"]), set())]
        for groups, merge_jobs, expected in cases:
            with self.subTest(groups=groups, merge_jobs=merge_jobs):
                runs = [run(1, "ci.yml", "merge_group", branch=GROUP)] if groups else []
                repository = Repository(runs=runs, jobs={1: merge_jobs}, groups=groups, pulls=pulls, history=history)
                _, resumed = repository.schedule()
                self.assertEqual(set(resumed), expected)
                self.assertEqual(set(repository.posted("/rerun-failed-jobs")), expected)
                if not expected:
                    self.assertFalse([call for call in repository.calls if "/workflows/" in call[1]])

    def test_a_refused_action_is_reported_and_the_sweep_continues(self):
        history = {("build-caches.yml", "push"): [cancelled(20, "build-caches.yml", "push")],
                   ("nightly.yml", "schedule"): [cancelled(21, "nightly.yml", "schedule")]}
        repository = Repository(history=history, refused=[21])
        with patch("builtins.print") as printed:
            self.assertEqual(rp.schedule("owner/repo", repository, NOW), ([], [20]))
        self.assertEqual(sorted(repository.posted("/rerun-failed-jobs")), [20, 21])
        self.assertTrue(any(call.args[0].startswith("::warning::") for call in printed.call_args_list))

    def test_classes_follow_the_catalog_and_ci_events(self):
        classes = rp.workflow_classes()
        ready = {"head"}
        cases = [(run(1, "ci.yml", "merge_group"), rp.QUEUE), (run(2, "ci.yml", "pull_request", sha="head"), rp.READY),
                 (run(3, "ci.yml", "pull_request"), rp.DRAFT), (run(4, "ci.yml", "workflow_dispatch"), None),
                 (run(5, "release-halt.yml", "workflow_dispatch"), rp.RELEASE),
                 (run(6, "build-caches.yml", "push"), rp.WARMING), (run(7, "issue-sweep.yml", "schedule"), rp.BACKGROUND),
                 (run(8, "issue-sweep.yml", "issues"), None), (run(9, "runner-priority.yml", "schedule"), None)]
        for item, expected in cases:
            with self.subTest(run=item["id"]):
                self.assertEqual(rp.run_class(item, classes, ready), expected)
        for mutation in ("duplicate", "ci", "unknown"):
            with self.subTest(mutation=mutation):
                data = copy.deepcopy(planning.catalog())
                declared = data["runner_priority"]
                if mutation == "duplicate":
                    declared["warming"].append("nightly.yml")
                elif mutation == "ci":
                    declared["background"].append("ci.yml")
                else:
                    declared["urgent"] = ["build-caches.yml"]
                with patch.object(rp, "catalog", return_value=data), self.assertRaises(ValueError):
                    rp.workflow_classes()
