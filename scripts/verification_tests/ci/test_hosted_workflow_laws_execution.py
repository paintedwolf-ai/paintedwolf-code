"""Hosted CI structure: required aggregates, draft admission, certification, caches and pinned actions."""
import json
from pathlib import Path
import unittest

import yaml

from verification_tests.ci.workflows import (PINNED, ROOT, action, actions, evaluate, invocation, render,
                                             taskfile, truthy, workflow, workflows)


def event(name, **values):
    """A run context for `github.event_name == name`; dotted keys build nested values."""
    context = {"github": {"event_name": name, "event": {}}, "inputs": {}}
    for key, value in values.items():
        node = context
        *parents, leaf = key.split(".")
        for part in parents:
            node = node.setdefault(part, {})
        node[leaf] = value
    return context


DRAFT = event("pull_request", **{"github.event.action": "synchronize", "github.event.pull_request.draft": True})
UNDRAFTED = event("pull_request", **{"github.event.action": "ready_for_review",
                                     "github.event.pull_request.draft": True})
READY = event("pull_request", **{"github.event.action": "synchronize", "github.event.pull_request.draft": False})


# Qualification jobs that check the candidate source itself rather than its verification receipts.
CANDIDATE_CHECKS = {"static", "custody"}


def is_verdict(step):
    return any(argv[1:3] == ["scripts/ci_verification.py", "gate"] for argv in step.commands())


def gate_receives_needs(job):
    """The aggregate hands its dependency results to its verdict step as structured data."""
    needs = {name: {"result": "failure"} for name in job.needs}
    rendered = [render(value, {"needs": needs}) for step in job.steps for value in step.env.values()]
    return any(isinstance(value, str) and value.startswith("{") and json.loads(value) == needs for value in rendered)


class RequiredAggregateLaws(unittest.TestCase):
    def test_each_aggregate_judges_every_blocking_job_even_after_failures(self):
        for name, gate in (("ci", "check"), ("nightly-verification", "nightly"), ("qualification", "qualification")):
            with self.subTest(workflow=name):
                jobs = workflow(name).jobs
                # A job that cannot fail the run, or that reports the aggregate's own result, is not a dependency.
                blocking = {job for job, spec in jobs.items()
                            if job != gate and not spec.continues and gate not in spec.ancestors()}
                self.assertEqual(set(jobs[gate].needs), blocking)
                self.assertTrue(truthy(evaluate(jobs[gate].condition or "success()", event("schedule"), "failure")))
                self.assertFalse(truthy(evaluate(jobs[gate].condition or "success()", event("schedule"), "cancelled")))
                self.assertTrue(gate_receives_needs(jobs[gate]))

    def test_reusable_verification_fails_with_its_plan_or_any_matrix_job(self):
        jobs = workflow("verification").jobs
        matrix = [job for job in jobs.values() if "matrix" in (job.raw.get("strategy") or {})]
        self.assertEqual(len(matrix), 1)
        execution = matrix[0]
        self.assertTrue(execution.needs)
        self.assertEqual(set(jobs), {execution.name, *execution.needs})
        self.assertFalse(any(job.continues for job in jobs.values()))
        # Only an empty selection skips execution; anything selected runs.
        outputs = lambda matrix: {"needs": {plan: {"outputs": {"matrix": matrix}} for plan in execution.needs}}
        self.assertFalse(truthy(evaluate(execution.condition or "true", outputs('{"include":[]}'))))
        self.assertTrue(truthy(evaluate(execution.condition or "true", outputs('{"include":[{"lane":"x"}]}'))))
        self.assertIs(execution.raw["strategy"]["fail-fast"], False)
        self.assertFalse(any(step.continues for job in jobs.values() for step in job.steps))


class DraftAdmissionLaws(unittest.TestCase):
    def test_a_draft_spends_no_runner_and_cannot_satisfy_the_required_check(self):
        ci = workflow("ci")
        self.assertIn("ready_for_review", ci.trigger("pull_request")["types"])
        for name in ("static", "fast"):
            condition = ci.jobs[name].condition
            self.assertFalse(truthy(evaluate(condition, DRAFT)), name)
            self.assertTrue(truthy(evaluate(condition, UNDRAFTED)), name)
            self.assertTrue(truthy(evaluate(condition, READY)), name)
        group = ci.raw["concurrency"]["group"]
        self.assertNotEqual(render(group, DRAFT), render(group, READY))
        check = ci.jobs["check"]
        self.assertEqual(render(check.raw["name"], READY), "check")
        self.assertNotEqual(render(check.raw["name"], DRAFT), "check")
        diagnostic = event("workflow_dispatch", **{"inputs.lane": "behavior"})
        self.assertNotEqual(render(check.raw["name"], diagnostic), "check")
        verdicts = [step for step in check.steps if is_verdict(step)]
        self.assertTrue(verdicts)
        self.assertTrue(all(any(render(value, DRAFT) is True for value in step.env.values()) for step in verdicts))

    def test_the_required_check_cannot_fail_on_cleanup_and_only_cleanup_holds_its_token(self):
        check = workflow("ci").jobs["check"]
        self.assertEqual(check.permissions.get("actions"), "write")
        for step in check.checkouts():
            self.assertEqual(step.inputs.get("persist-credentials"), "false")
        holders = [step for step in check.steps if any("github.token" in value for value in step.env.values())]
        self.assertTrue(holders)
        for step in holders:
            self.assertFalse(is_verdict(step))
            self.assertTrue(step.continues)
            self.assertLess(step.raw["timeout-minutes"], check.raw["timeout-minutes"])


class CertificationLaws(unittest.TestCase):
    def setUp(self):
        self.dco = workflow("dco")
        (self.job,) = self.dco.jobs.values()

    def test_certification_is_one_serialized_publisher_with_least_privilege(self):
        writes = {scope for scope, level in self.dco.permissions.items() if level == "write"}
        self.assertEqual(writes, {"checks", "statuses"})
        concurrency = self.dco.raw["concurrency"]
        group = lambda **values: render(concurrency["group"], event("pull_request_target", **values))
        first, second = group(**{"github.event.pull_request.number": 1}), group(**{"github.event.pull_request.number": 2})
        queue = group(**{"github.event.merge_group.head_ref": "refs/heads/gh-readonly-queue/main/pr-1"})
        self.assertEqual(len({first, second, queue}), 3)
        self.assertEqual(first, group(**{"github.event.pull_request.number": 1, "github.run_id": 9}))
        self.assertIs(concurrency["cancel-in-progress"], False)
        self.assertFalse(self.job.permissions)
        self.assertFalse(any(step.continues for step in self.job.steps))
        for step in self.job.checkouts():
            self.assertEqual(step.inputs.get("persist-credentials"), "false")

    def test_only_ready_pull_requests_and_their_completed_runs_reach_certification(self):
        target = {"github.actor": "someone", "github.event.action": "synchronize"}
        cases = [
            (event("pull_request_target", **target, **{"github.event.pull_request.draft": True}), False),
            (event("pull_request_target", **{**target, "github.event.action": "ready_for_review"},
                   **{"github.event.pull_request.draft": True}), True),
            (event("pull_request_target", **target, **{"github.event.pull_request.draft": False}), True),
            (event("pull_request_target", **{**target, "github.actor": "dependabot[bot]"},
                   **{"github.event.pull_request.draft": False}), False),
            (event("workflow_run", **{"github.event.workflow_run.event": "push"}), False),
            (event("workflow_run", **{"github.event.workflow_run.event": "pull_request"}), True),
            (event("merge_group"), True),
        ]
        for context, admitted in cases:
            with self.subTest(context=context):
                self.assertEqual(truthy(evaluate(self.job.condition, context)), admitted)

    def test_queue_recovery_and_ordinary_certification_run_the_same_pinned_checker(self):
        queue = event("workflow_dispatch", **{"inputs.queue_ref": "refs/heads/gh-readonly-queue/main/pr-1",
                                              "inputs.queue_sha": "a" * 40, "inputs.pull_request": ""})
        manual = event("workflow_dispatch", **{"inputs.pull_request": "12", "inputs.queue_ref": "",
                                               "inputs.queue_sha": ""})
        runs = lambda context: [step for step in self.job.steps
                                if truthy(evaluate(step.condition or "true", context))]
        (shared,) = [step for step in runs(manual)]
        self.assertRegex(shared.uses, PINNED)
        repository, revision = shared.uses.split("@")
        queued = runs(queue)
        self.assertNotIn(shared, queued)
        checker = [step for step in queued if step.inputs.get("repository") == repository]
        self.assertEqual([step.inputs["ref"] for step in checker], [revision])
        self.assertTrue(any(revision in step.env.values() for step in queued))

    def test_completed_run_fallback_follows_ci_and_manual_routes_stay_distinct(self):
        self.assertEqual(self.dco.trigger("workflow_run")["workflows"], [workflow("ci").name])
        inputs = self.dco.dispatch_inputs()
        self.assertTrue(inputs)
        for name, spec in inputs.items():
            self.assertIs(spec.get("required", False), False, name)


class CacheAndActionLaws(unittest.TestCase):
    def test_cache_writes_happen_only_on_trusted_main(self):
        branch = event("pull_request", **{"github.ref": "refs/heads/feature"})
        writes = 0
        for name in actions():
            for step in action(name):
                guard = None
                if step.action() in {"actions/cache", "actions/cache/save"}:
                    guard = step.condition or "true"
                elif step.action() == "Swatinem/rust-cache":
                    guard = step.inputs.get("save-if", "true")
                if guard is not None:
                    writes += 1
                    self.assertFalse(truthy(evaluate(guard, branch)), f"{name}: {step.uses}")
        self.assertTrue(writes)

    def test_every_external_action_is_pinned_to_a_commit_and_maintained(self):
        steps = [step for spec in workflows() for job in spec.jobs.values() for step in job.steps]
        steps += [step for name in actions() for step in action(name)]
        external = [step.uses for step in steps if step.uses and not step.uses.startswith("./")]
        self.assertTrue(external)
        for uses in external:
            self.assertRegex(uses, PINNED)
        calls = [job.uses for spec in workflows() for job in spec.jobs.values()
                 if job.uses and not job.uses.startswith("./")]
        for uses in calls:
            self.assertRegex(uses, PINNED)
        updates = yaml.safe_load((ROOT / ".github/dependabot.yml").read_text())["updates"]
        self.assertTrue(any(entry["package-ecosystem"] == "github-actions" and entry.get("schedule")
                            for entry in updates))


class ReferenceLaws(unittest.TestCase):
    def test_every_action_workflow_script_module_and_task_a_workflow_names_exists(self):
        steps = [(spec.path, step) for spec in workflows() for job in spec.jobs.values() for step in job.steps]
        steps += [(f"action {name}", step) for name in actions() for step in action(name)]
        calls = [job.uses for spec in workflows() for job in spec.jobs.values() if job.uses.startswith("./")]
        self.assertTrue(steps)
        # A job may run an action from a second checkout of this repository.
        checkouts = {step.inputs["path"] for _, step in steps
                     if step.action() == "actions/checkout" and step.inputs.get("path") and not step.inputs.get("repository")}
        missing = [uses for uses in calls if not (ROOT / uses).is_file()]
        for source, step in steps:
            if step.uses.startswith("./"):
                local = step.uses
                for path in checkouts:
                    local = local.replace(f"./{path}/", "./", 1)
                if not (ROOT / local / "action.yml").is_file():
                    missing.append(f"{source}: {step.uses}")
            missing += [f"{source}: ./task {task}" for task in step.tasks() if task not in taskfile()]
            for program in step.modules() | {name for name, _ in map(invocation, step.commands())}:
                if program.startswith("scripts/") and "$" not in program and not (ROOT / program).exists():
                    missing.append(f"{source}: {program}")
                elif "." in program and "/" not in program and not program.endswith(".py"):
                    module = program.split(".")
                    candidates = [ROOT / Path(*module), ROOT / "scripts" / Path(*module)]
                    if module[0] in {"scripts", "ci_policy"} and not any(
                            path.with_suffix(".py").is_file() or (path / "__init__.py").is_file() for path in candidates):
                        missing.append(f"{source}: python -m {program}")
        self.assertEqual(missing, [])


class QualificationSourceLaws(unittest.TestCase):
    def test_lanes_check_out_the_selected_source_and_qualification_its_controller(self):
        selected = {"inputs": {"source-sha": "b" * 40}, "github": {"sha": "c" * 40, "workflow_sha": "d" * 40}}
        default = {"inputs": {"source-sha": ""}, "github": selected["github"]}
        for name in ("verification", "platform-verification", "e2e-verification"):
            checkouts = [step for job in workflow(name).jobs.values() for step in job.checkouts()]
            self.assertTrue(checkouts, name)
            for step in checkouts:
                self.assertEqual(render(step.inputs["ref"], selected), "b" * 40, name)
                self.assertEqual(render(step.inputs["ref"], default), "c" * 40, name)
        # Only the candidate checks run the selected source; every other qualification job runs its controller.
        candidate = {**selected, "needs": {"source": {"outputs": {"source-sha": "e" * 40}}}}
        refs = {job.name: {render(step.inputs.get("ref", ""), candidate) for step in job.checkouts()}
                for job in workflow("qualification").jobs.values() if job.checkouts()}
        self.assertEqual({name for name, found in refs.items() if found == {"e" * 40}}, CANDIDATE_CHECKS)
        self.assertEqual({name for name, found in refs.items() if found == {"d" * 40}}, set(refs) - CANDIDATE_CHECKS)

    def test_qualification_publishes_only_its_completed_attempt_receipt(self):
        job = workflow("qualification").jobs["qualification"]
        self.assertTrue(truthy(evaluate(job.condition, event("workflow_dispatch"), "failure")))
        self.assertFalse(truthy(evaluate(job.condition, event("workflow_dispatch"), "cancelled")))
        receipt = next(index for index, step in enumerate(job.steps)
                       if any("needs" in value for value in step.env.values()))
        self.assertTrue(gate_receives_needs(job))
        uploads = [(index, step) for index, step in enumerate(job.steps)
                   if step.action() == "actions/upload-artifact"]
        self.assertTrue(uploads)
        for index, step in uploads:
            self.assertGreater(index, receipt)
            self.assertEqual((step.condition, step.continues), ("", False))
            self.assertEqual(step.inputs["if-no-files-found"], "error")
            first = render(step.inputs["name"], {"github": {"run_attempt": 1}})
            second = render(step.inputs["name"], {"github": {"run_attempt": 2}})
            self.assertNotEqual(first, second)


if __name__ == "__main__":
    unittest.main()
