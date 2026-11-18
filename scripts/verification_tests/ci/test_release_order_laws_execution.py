"""Release order: gates before publication, the selected source in every checkout, and halts that
prepare every withdrawal before changing anything."""
import unittest

from verification_tests.ci.workflows import evaluate, render, taskfile, truthy, workflow

SOURCE, CONTROLLER = "s" * 40, "c" * 40
CONTEXT = {"needs": {"classify": {"outputs": {"source_sha": SOURCE}}},
           "steps": {"source": {"outputs": {"source_sha": SOURCE}}},
           "github": {"workflow_sha": CONTROLLER, "sha": "t" * 40}}
# Rehearsal jobs run the controller's harness against fixtures prepared from the selected source.
CONTROLLER_JOBS = {"bundle-upgrade", "replay-bundle-upgrade"}
HALT_MUTATIONS = (("release:halt", "--apply-prepared"), ("scripts.linux_release.repository_withdrawal", "apply"),
                  ("scripts.nix_cache.release_lane", "withdraw"), ("scripts/release_withdrawal.py", "apply"))
HALT_PREPARATIONS = ("linux_plan", "ledger_plan", "feed_plan", "nix_plan", "distribution_plan")


def classified(publish):
    return {"needs": {"classify": {"outputs": {"publish": publish}}}, "inputs": {"rehearsal_run_id": ""}}


def halted(dry_run=False, barrier="success", signed="success"):
    return {"inputs": {"dry_run": dry_run},
            "needs": {"halt": {"outputs": {"barrier": barrier}}, "sign-native-restore": {"result": signed}}}


def mutates(step):
    return any(step.invokes(program, argument) for program, argument in HALT_MUTATIONS)


class ReleaseGateLaws(unittest.TestCase):
    def setUp(self):
        self.jobs = workflow("release").jobs

    def test_source_validation_and_compatibility_gate_every_publication(self):
        gates = self.jobs["ship-gates"]
        self.assertLessEqual({"classify", "validate-source", "compatibility"}, gates.ancestors())
        self.assertTrue(truthy(evaluate(gates.condition, classified("true"), "failure")))
        self.assertFalse(truthy(evaluate(gates.condition, classified("true"), "cancelled")))
        for name in ("validate-source", "build-release"):
            self.assertLessEqual({"classify", "compatibility"}, set(self.jobs[name].needs), name)
            self.assertEqual(self.jobs[name].condition, "", name)
        for name, job in self.jobs.items():
            if job.environment == "release-publication":
                self.assertIn("ship-gates", job.ancestors(), name)

    def test_a_dry_run_publishes_nothing(self):
        for name in ("publish-artifacts", "publish-immutable"):
            self.assertFalse(render(self.jobs[name].condition, classified("false")), name)
            self.assertTrue(render(self.jobs[name].condition, classified("true")), name)
        for name in ("update-cask", "notify-website", "activate-updater", "publish-github-release"):
            self.assertIn("publish-immutable", self.jobs[name].ancestors(), name)

    def test_the_mutable_pointer_moves_last_and_the_mirror_never_gates_it(self):
        pointer = {name for name, job in self.jobs.items()
                   if any(step.invokes("scripts/release-r2-publish-pointer.sh") or
                          step.invokes("scripts.release_distribution_contract.channels", "activate")
                          for step in job.steps)}
        self.assertEqual(pointer, {"activate-updater"})
        activation = self.jobs["activate-updater"].ancestors()
        self.assertLessEqual({"publish-immutable", "update-cask", "notify-website"}, activation)
        self.assertNotIn("publish-github-release", activation)
        deposit = self.jobs["publish-artifacts"].steps
        advance = next(index for index, step in enumerate(deposit)
                       if step.invokes("scripts/release_distribution.py", "advance"))
        puts = [index for index, step in enumerate(deposit) if step.invokes("scripts/release-r2-immutable-put.sh")]
        self.assertTrue(puts)
        self.assertLess(advance, min(puts))

    def test_the_release_token_is_read_only_and_the_signed_build_writes_nothing(self):
        self.assertEqual(workflow("release").permissions, {"contents": "read"})
        build = self.jobs["build-release"]
        self.assertFalse({level for level in (build.permissions or {}).values()} & {"write"})
        for step in build.steps:
            for argv in step.commands():
                self.assertNotIn(argv[:2], (["git", "push"], ["gh", "pr"]))

    def test_published_compatibility_checks_run_unconditionally_before_any_build(self):
        classify = self.jobs["classify"]
        baseline = [step for step in classify.steps if step.invokes("scripts/release-compatibility-baseline.py")]
        self.assertTrue(baseline)
        compatibility = self.jobs["compatibility"]
        guards = [step for step in compatibility.steps if step.tasks() & {"release:preflight", "openapi:diff"}]
        self.assertEqual({task for step in guards for task in step.tasks()}, {"release:preflight", "openapi:diff"})
        for step in baseline + guards:
            self.assertEqual((step.condition, step.continues), ("", False))
        provisioning = {"./.github/actions/setup-verification", "./.github/actions/setup-bialy"}
        self.assertFalse({step.action() for step in compatibility.steps} & provisioning)
        self.assertFalse({task for step in compatibility.steps for task in step.tasks()} & {"setup-dev", "build:lycaon-dev"})
        validation = self.jobs["validate-source"].steps
        preflight = next(index for index, step in enumerate(validation) if "release:preflight" in step.tasks())
        rehearsal = next(index for index, step in enumerate(validation) if "upgrade:rehearse" in step.tasks())
        self.assertLess(preflight, rehearsal)
        self.assertEqual((validation[rehearsal].condition, validation[rehearsal].continues), ("", False))
        # A dry run has no frozen corpus; a publishing release cannot skip it.
        self.assertTrue(truthy(evaluate(validation[preflight].condition or "true", classified("true"))))
        self.assertFalse(validation[preflight].continues)


class ReleaseCheckoutLaws(unittest.TestCase):
    def test_every_checkout_binds_the_selected_source_or_its_trusted_controller(self):
        for name, job in workflow("release").jobs.items():
            for index, step in enumerate(job.checkouts()):
                if step.inputs.get("repository"):
                    continue
                with self.subTest(job=name, checkout=index):
                    ref = render(step.inputs.get("ref", ""), CONTEXT)
                    controller = step.inputs.get("path") == ".release-controller" or name == "classify" and index == 0
                    if controller:
                        self.assertIn(ref, {"refs/heads/main", CONTROLLER})
                    elif name in CONTROLLER_JOBS:
                        self.assertEqual(ref, CONTROLLER)
                    else:
                        self.assertEqual(ref, SOURCE)

    def test_rehearsal_checkouts_are_single_immutable_and_credential_free(self):
        jobs = workflow("release").jobs
        for name in ("prepare-bundle-upgrade", *CONTROLLER_JOBS):
            paths = [step.inputs.get("path", "") for step in jobs[name].checkouts()]
            self.assertTrue(paths, name)
            self.assertEqual(len(paths), len(set(paths)), name)
            for step in jobs[name].checkouts():
                self.assertEqual(step.inputs.get("persist-credentials"), "false", name)
                self.assertFalse(step.inputs.get("repository"), name)


class ReleaseHaltLaws(unittest.TestCase):
    def setUp(self):
        self.halt = workflow("release-halt")
        self.jobs = self.halt.jobs

    def test_a_halt_is_manual_serialized_with_releases_and_publication_protected(self):
        self.assertEqual(set(self.halt.on), {"workflow_dispatch"})
        self.assertIn("dry_run", self.halt.dispatch_inputs())
        # A publishing release and a halt queue behind each other; a rehearsal runs apart.
        publishing = {"github": {"event_name": "push", "run_id": 7}, "inputs": {"rehearsal_run_id": ""}}
        release = workflow("release").raw["concurrency"]
        self.assertEqual(render(self.halt.raw["concurrency"]["group"], publishing), render(release["group"], publishing))
        self.assertIs(self.halt.raw["concurrency"]["cancel-in-progress"], False)
        effects = [job for job in self.jobs.values() if any(mutates(step) for step in job.steps)]
        self.assertTrue(effects)
        for job in effects:
            self.assertEqual(job.environment, "release-publication", job.name)

    def test_every_withdrawal_is_prepared_before_the_barrier_and_nothing_changes_before_it(self):
        halt = self.jobs["halt"]
        position = {step.id: index for index, step in enumerate(halt.steps) if step.id}
        selection = halt.steps[position["halt_selection"]]
        self.assertTrue(selection.invokes("scripts/release-halt-plan.py", "--select-only"))
        self.assertNotIn("FEED_SIGNING_KEYS_JSON", {key for key in halt.raw.get("env") or {}})
        barrier = halt.steps[position["halt_barrier"]]
        self.assertTrue(barrier.invokes("scripts.release_distribution_contract.halt_effects"))
        outcomes = {"steps": {step: {"outcome": f"outcome-{step}"} for step in position}}
        recorded = render(" ".join(barrier.env.values()), outcomes)
        for step in HALT_PREPARATIONS:
            self.assertLess(position["halt_selection"], position[step], step)
            self.assertLess(position[step], position["halt_barrier"], step)
            self.assertIn(f"outcome-{step}", recorded, step)
            # A preparation still runs when an unrelated one refused, so the barrier sees every refusal.
            context = {"steps": {name: {"outcome": "success"} for name in ("halt_selection", *HALT_PREPARATIONS)}}
            self.assertTrue(truthy(evaluate(halt.steps[position[step]].condition, context, "failure")), step)
        self.assertFalse(any(mutates(step) for step in halt.steps))
        self.assertEqual(render(halt.raw["outputs"]["barrier"], {"steps": {"halt_barrier": {"outcome": "x"}}}), "x")

    def test_every_halt_effect_waits_for_a_complete_barrier_and_a_real_run(self):
        signer, apply = self.jobs["sign-native-restore"], self.jobs["apply-halt"]
        self.assertIn(signer.name, apply.ancestors())
        for job in (signer, apply):
            self.assertTrue(truthy(evaluate(job.condition, halted())), job.name)
            for context in (halted(dry_run=True), halted(barrier="failure")):
                self.assertFalse(truthy(evaluate(job.condition, context)), job.name)
            self.assertFalse(truthy(evaluate(job.condition, halted(), "cancelled")), job.name)
        self.assertFalse(truthy(evaluate(apply.condition, halted(signed="failure"))))
        for program, argument in HALT_MUTATIONS:
            self.assertTrue(any(step.invokes(program, argument) for step in apply.steps), program)
        prepare = self.jobs["halt"].steps
        self.assertTrue(any(step.invokes("scripts.linux_release.repository_withdrawal", "prepare") for step in prepare))
        self.assertTrue(any(step.invokes("scripts.linux_release.repository_withdrawal", "apply", "--signed")
                            for step in apply.steps))


class PlatformAndLiveTestLaws(unittest.TestCase):
    def test_platform_verification_runs_seatbelt_on_macos_and_boots_the_upgrade_corpus(self):
        jobs = workflow("platform-verification").jobs.values()
        runners = {task: job.raw["runs-on"] for job in jobs for step in job.steps for task in step.tasks()}
        self.assertTrue(runners["test:seatbelt"].startswith("macos-"))
        self.assertIn("upgrade:corpus:boot", runners)
        for name in ("upgrade:corpus:seed", "upgrade:corpus:boot"):
            self.assertIn(name, taskfile())

    def test_the_credentialed_live_test_runs_only_in_its_own_scheduled_rehearsal(self):
        live = workflow("release-system-live-test")
        self.assertIn("schedule", live.on)
        tasks = {task for job in live.jobs.values() for step in job.steps for task in step.tasks()}
        self.assertIn("release:live-test", tasks)
        for job in live.jobs.values():
            if job.secrets():
                self.assertEqual(job.environment, "release-rehearsal", job.name)
                self.assertTrue(all(secret.startswith("RELEASE_TEST_") for secret in job.secrets()), job.name)
        for other in ("release", "release-halt", "nightly", "ci", "qualification"):
            spec = workflow(other)
            self.assertFalse(any(secret.startswith("RELEASE_TEST_") for job in spec.jobs.values()
                                 for secret in job.secrets()), other)
            self.assertNotIn("release:live-test", {task for job in spec.jobs.values() for step in job.steps
                                                   for task in step.tasks()}, other)
        for name, task in taskfile().items():
            if name != "release:live-test":
                callers = [entry.get("task") for entry in [*task.get("deps", []), *task.get("cmds", [])]
                           if isinstance(entry, dict)]
                self.assertNotIn("release:live-test", callers, name)


if __name__ == "__main__":
    unittest.main()
