"""Advisory queue observations: backlog, runtime history, and superseded source."""

import contextlib
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import verification_advisories as advisories
import verification_history as history
from verification_state import read_state


NOW = 1_000_000.0
EXCLUSIVE = {"kind": "invocation", "admission": {"locks": ["*"], "shared_locks": [], "workers": 6}}


def run(ticket, name, state, **fields):
    entry = {"ticket": ticket, "name": name, "state": state, "kind": "request"}
    entry.update(fields)
    return entry


def batch(ticket, members, directory=None, **fields):
    entry = {"ticket": ticket, "name": "verification batch", "state": "running", "kind": "batch",
             "members": members, "source": "/checkout", "started_at": NOW - 60}
    if directory:
        entry["directory"] = str(directory)
    entry.update(fields)
    return entry


class AdvisoryTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.queue = SimpleNamespace(root=self.root, locked=contextlib.nullcontext)
        advisories._announced.clear()

    def status(self, runs=(), operations=(), **fields):
        value = {"runs": list(runs), "operations": list(operations), "paused": None,
                 "capacity": {"workers": 6, "reserved": 6, "available": 0}}
        value.update(fields)
        return value

    def observe(self, status):
        with patch.object(advisories, "source_stamp", return_value=None):
            return advisories.observe(self.queue, status, now=NOW)

    def codes(self, entry):
        return [item["code"] for item in entry.get("advisories", [])]

    def test_ages_are_rendered_so_reading_the_queue_is_not_arithmetic(self):
        active = run("a", "test:integration", "running", started_at=NOW - 3600, queued_at=NOW - 7200)
        waiting = run("b", "check-fast", "queued", queued_at=NOW - 1800)
        status = self.observe(self.status([active, waiting]))
        self.assertEqual(active["elapsed_seconds"], 3600.0)
        self.assertNotIn("waited_seconds", active)
        self.assertEqual(waiting["waited_seconds"], 1800.0)
        self.assertIn("22 min", advisories.duration_text(1300))
        self.assertEqual(status["summary"],
                         "6 of 6 workers reserved; 1 running; 1 waiting; oldest queued 30 min ago")

    def test_requests_a_running_batch_serves_are_not_counted_as_waiting(self):
        served = run("member", "test:digest", "queued", queued_at=NOW - 7200)
        waiting = run("other", "check-fast", "queued", queued_at=NOW - 60)
        status = self.observe(self.status([batch("b-batch", ["member"]), served, waiting]))
        self.assertEqual(status["advisories"], [])
        self.assertNotIn("waited_seconds", served)
        self.assertEqual(waiting["waited_seconds"], 60.0)

    def test_sustained_backlog_is_reported_with_its_oldest_wait(self):
        waiting = [run(str(index), "check-fast", "queued", queued_at=NOW - 600 * (index + 1))
                   for index in range(3)]
        status = self.observe(self.status(waiting))
        advisory = status["advisories"][0]
        self.assertEqual(advisory["code"], "queue_backlog")
        self.assertEqual(advisory["waiting"], 3)
        self.assertEqual(advisory["oldest_wait_seconds"], 1800.0)
        self.assertIn("the oldest for 30 min", advisory["summary"])
        self.assertEqual(advisory["action"], "none")

    def test_a_short_queue_is_not_a_backlog(self):
        fresh = run("a", "check-fast", "queued", queued_at=NOW - advisories.BLOCKED_SECONDS + 1)
        self.assertEqual(self.observe(self.status([fresh]))["advisories"], [])

    def test_a_pause_is_named_as_the_reason_nothing_starts(self):
        waiting = run("a", "check-fast", "queued", queued_at=NOW - 3600)
        status = self.observe(self.status([waiting], paused={"reason": "Host in use"}))
        self.assertEqual(status["advisories"][0]["code"], "paused_backlog")
        self.assertIn("Host in use", status["advisories"][0]["summary"])

    def test_only_an_exclusive_run_is_named_as_holding_the_queue(self):
        exclusive = run("a", "test:integration", "running", started_at=NOW - 4000, workers=6, **EXCLUSIVE)
        waiting = run("b", "check-fast", "queued", queued_at=NOW - 3600)
        self.observe(self.status([exclusive, waiting]))
        self.assertEqual(self.codes(exclusive), ["holding_queue"])
        summary = exclusive["advisories"][0]["summary"]
        self.assertIn("holding exclusive admission for 67 min", summary)
        self.assertIn("1 request cannot start", summary)

        shared = batch("c-batch", [], started_at=NOW - 4000)
        other = run("d", "check-fast", "queued", queued_at=NOW - 3600)
        self.observe(self.status([shared, other]))
        self.assertEqual(self.codes(shared), [])

        declared = run("e", "build:lycaon-dev", "running", started_at=NOW - 4000, kind="invocation",
                       admission={"locks": ["dev-engine"], "shared_locks": [], "workers": 3})
        self.observe(self.status([declared, run("f", "check-fast", "queued", queued_at=NOW - 3600)]))
        self.assertEqual(self.codes(declared), [])

    def test_holding_is_not_claimed_while_the_queue_moves(self):
        exclusive = run("a", "test:integration", "running", started_at=NOW - 4000, workers=6, **EXCLUSIVE)
        waiting = run("b", "check-fast", "queued", queued_at=NOW - 60)
        self.observe(self.status([exclusive, waiting]))
        self.assertEqual(self.codes(exclusive), [])

    def test_a_fresh_holder_is_not_blamed_for_waits_earlier_runs_caused(self):
        fresh = run("a", "upgrade:corpus:boot", "running", started_at=NOW - 27, workers=6, **EXCLUSIVE)
        waiting = run("b", "check-fast", "queued", queued_at=NOW - 1620)
        status = self.observe(self.status([fresh, waiting]))
        self.assertEqual(self.codes(fresh), [])
        self.assertEqual(status["advisories"][0]["code"], "queue_backlog")

    def test_runtime_history_needs_samples_a_floor_and_a_multiple(self):
        for _ in range(history.MIN_SAMPLES - 1):
            history.record_duration(self.queue, ["test:short"], 120)
        entry = run("a", "test:short", "running", started_at=NOW - 3600)
        self.observe(self.status([entry]))
        self.assertEqual(self.codes(entry), [], "a baseline needs enough completed runs")

        history.record_duration(self.queue, ["test:short"], 120)
        brief = run("b", "test:short", "running", started_at=NOW - advisories.RUNTIME_FLOOR_SECONDS + 1)
        self.observe(self.status([brief]))
        self.assertEqual(self.codes(brief), [], "a short absolute runtime is not remarkable")

        slow = run("c", "test:short", "running", started_at=NOW - 3600)
        self.observe(self.status([slow]))
        advisory = slow["advisories"][0]
        self.assertEqual(advisory["code"], "runtime_exceeds_history")
        self.assertEqual(advisory["typical_seconds"], 120.0)
        self.assertEqual(advisory["samples"], history.MIN_SAMPLES)
        self.assertIn("running 60 min", advisory["summary"])

    def test_a_run_within_its_usual_range_is_quiet(self):
        for seconds in (1800, 2000, 2200):
            history.record_duration(self.queue, ["check"], seconds)
        entry = run("a", "check", "running", started_at=NOW - 3000)
        self.observe(self.status([entry]))
        self.assertEqual(self.codes(entry), [])

    def test_history_is_bounded_in_samples_and_in_names(self):
        for index in range(history.HISTORY_LIMIT + 5):
            history.record_duration(self.queue, ["test:short"], index + 1)
        recorded = history.read_history(self.queue)
        self.assertEqual(len(history.samples(recorded, "test:short")), history.HISTORY_LIMIT)
        self.assertEqual(history.samples(recorded, "test:short")[-1], history.HISTORY_LIMIT + 5)
        for index in range(history.HISTORY_NAMES + 4):
            history.record_duration(self.queue, [f"target-{index}"], 10)
        self.assertLessEqual(len(history.read_history(self.queue)), history.HISTORY_NAMES)

    def test_implausible_durations_never_become_a_baseline(self):
        for seconds in (-1, 86400, "slow", None):
            history.record_duration(self.queue, ["test:short"], seconds)
        history.record_duration(self.queue, [], 10)
        self.assertEqual(history.read_history(self.queue), {})

    def test_a_batch_reports_when_the_checkout_moved_past_its_capture(self):
        directory = self.root / "batch"
        directory.mkdir()
        (directory / "source.json").write_text(json.dumps({"source_stamp": "captured"}))
        entry = batch("b-batch", ["member"], directory)
        with patch.object(advisories, "source_stamp", return_value="live"):
            advisories.observe(self.queue, self.status([entry]), now=NOW)
        self.assertEqual(self.codes(entry), ["source_superseded"])
        self.assertIn("will not cover the current tree", entry["advisories"][0]["summary"])

    def test_an_unchanged_or_unreadable_checkout_makes_no_claim(self):
        directory = self.root / "batch"
        directory.mkdir()
        entry = batch("b-batch", ["member"], directory)
        for captured, live in (("captured", "captured"), (None, "live"), ("captured", None)):
            with self.subTest(captured=captured, live=live):
                entry.pop("advisories", None)
                (directory / "source.json").write_text(json.dumps({"source_stamp": captured}))
                with patch.object(advisories, "source_stamp", return_value=live):
                    advisories.observe(self.queue, self.status([entry]), now=NOW)
                self.assertEqual(self.codes(entry), [])

    def test_one_host_wide_source_sample_serves_every_observer(self):
        with patch.object(advisories, "source_stamp", return_value="live") as stamp:
            for _ in range(4):
                advisories.observed_stamps(self.queue, ["/checkout"], NOW)
            self.assertEqual(stamp.call_count, 1)
            advisories.observed_stamps(self.queue, ["/checkout"], NOW + advisories.STAMP_SECONDS)
            self.assertEqual(stamp.call_count, 2)

    def test_stale_checkout_samples_are_dropped(self):
        with patch.object(advisories, "source_stamp", return_value="live"):
            advisories.observed_stamps(self.queue, ["/gone"], NOW)
            advisories.observed_stamps(self.queue, ["/checkout"], NOW + advisories.STAMP_RETAIN_SECONDS + 1)
        self.assertEqual(list(read_state(self.root / "source-stamps.json")), ["/checkout"])

    def test_a_standing_condition_is_said_once_until_its_interval_passes(self):
        with patch("builtins.print"):
            self.assertTrue(advisories.announce("a", "queue_backlog", "first", now=0))
            self.assertFalse(advisories.announce("a", "queue_backlog", "second", now=60))
            self.assertTrue(advisories.announce("a", "holding_queue", "other", now=60))
            self.assertTrue(advisories.announce("a", "queue_backlog", "later",
                                                now=advisories.NOTICE_REPEAT_SECONDS + 1))

    def test_notices_name_the_subject_and_stay_advisory(self):
        exclusive = run("a", "test:integration", "running", started_at=NOW - 4000, workers=6, **EXCLUSIVE)
        waiting = run("b", "check-fast", "queued", queued_at=NOW - 3600)
        status = self.observe(self.status([exclusive, waiting]))
        lines = list(advisories.notices(status))
        self.assertEqual([subject for subject, _, _ in lines], ["queue", "a"])
        self.assertTrue(all(text.endswith(advisories.TRAILER) for _, _, text in lines))
        self.assertIn("test:integration (a): holding_queue", lines[1][2])

    def test_a_superseded_capture_is_told_only_to_the_request_waiting_on_it(self):
        directory = self.root / "batch"
        directory.mkdir()
        (directory / "source.json").write_text(json.dumps({"source_stamp": "captured"}))
        status = self.status([batch("b-batch", ["member"], directory)])
        with patch.object(advisories, "source_stamp", return_value="live"):
            advisories.observe(self.queue, status, now=NOW)
        codes = lambda ticket: [code for _, code, _ in advisories.notices(status, ticket)]
        self.assertEqual(codes("member"), ["source_superseded"])
        self.assertEqual(codes(None), [], "every checkout supersedes captures; peers are not told")
        self.assertEqual(codes("stranger"), [])

    def test_admission_notice_states_exclusivity_backlog_and_usual_runtime(self):
        for seconds in (2400, 2500, 2600):
            history.record_duration(self.queue, ["test:integration"], seconds)
        waiting = run("b", "check-fast", "queued", queued_at=NOW - 60)
        notice = advisories.admission_notice(self.queue, self.status([waiting]), "test:integration",
                                             {"locks": ["*"], "workers": 6})
        self.assertIn("no other verification runs while it holds its 6-worker admission", notice)
        self.assertIn("recent runs took about 42 min", notice)
        self.assertIn("1 request already queued", notice)

    def test_admission_notice_states_only_host_facts_when_nothing_is_known(self):
        notice = advisories.admission_notice(self.queue, self.status(), "test:integration",
                                             {"locks": ["*"], "workers": 6})
        self.assertEqual(notice, "test execution: test:integration takes exclusive admission: "
                                 "no other verification runs while it holds its 6-worker admission")

    def test_admission_notice_names_what_a_declared_invocation_holds(self):
        notice = advisories.admission_notice(self.queue, self.status(), "build:lycaon-dev",
                                             {"locks": ["dev-engine", "scanners"], "workers": 3})
        self.assertEqual(notice, "test execution: build:lycaon-dev runs outside a shared batch: it holds "
                                 "dev-engine, scanners and 3 workers for its whole run; other work continues")

if __name__ == "__main__":
    unittest.main()
