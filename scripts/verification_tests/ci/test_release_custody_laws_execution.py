"""Release credentials stay in their custody, release jobs read only bytes their own run produced or that
preparation produced and they verify against the source, and nothing public happens before every build and check."""
import unittest

from verification_tests.ci.workflows import invocation, render, workflow

SIGNING, PUBLICATION = "release-signing", "release-publication"
STORAGE = {"CLOUDFLARE_API_TOKEN", "R2_BUCKET"}
PUBLISHING = STORAGE | {"HOMEBREW_TAP_TOKEN", "WWW_DISPATCH_TOKEN"}
CODE_SIGNING = {"APPLE_CERTIFICATE", "AZURE_CLIENT_SECRET", "TAURI_SIGNING_PRIVATE_KEY"}
FEED_KEY = "FEED_SIGNING_KEYS_JSON"
PACKAGE_KEY = "LINUX_PACKAGE_SIGNING_KEY"
NIX_KEY = "NIX_CACHE_SIGNING_KEY"
# Reads retained channel controls after admission and before any deposit.
CHANNEL_SELECTION = "select-release-channels"
# The diagnostic replay of a retained rehearsal reads another run's fixtures and holds nothing.
REPLAY = "replay-bundle-upgrade"
RELEASE_WORKFLOWS = ("release", "release-halt", "release-linux-delivery")
PREPARED = {"needs": {"prepare": {"outputs": {"run_id": "prepared"}}}}
PREPARATION = "scripts.release_preparation"
# Builds, the closure check and the installed upgrade cases: every public side effect waits for all of them.
RELEASE_GATES = {"prepare", "build-release", "verify-nix-closures", "bundle-upgrade"}
COMPILERS = {("nix", "build"), ("cargo", "build"), ("go", "build")}


def holders(spec, secret):
    return sorted(name for name, job in spec.jobs.items() if secret in job.secrets())


def downloads(job):
    return [step for step in job.steps if step.action() == "actions/download-artifact"]


class CredentialCustodyLaws(unittest.TestCase):
    def test_code_signing_keys_live_only_in_signing_custody_and_never_beside_publication_keys(self):
        for name in RELEASE_WORKFLOWS:
            for job in workflow(name).jobs.values():
                secrets = job.secrets()
                if secrets & CODE_SIGNING:
                    self.assertEqual(job.environment, SIGNING, f"{name} {job.name}")
                    self.assertFalse(secrets & (PUBLISHING | {FEED_KEY}), f"{name} {job.name}")
        self.assertTrue(holders(workflow("release"), "TAURI_SIGNING_PRIVATE_KEY"))

    def test_the_feed_key_signs_pointers_at_publication_and_never_meets_the_artifact_key(self):
        for name in ("release", "release-halt"):
            spec = workflow(name)
            feed = holders(spec, FEED_KEY)
            self.assertTrue(feed, name)
            for job in feed:
                self.assertEqual(spec.jobs[job].environment, PUBLICATION, f"{name} {job}")
                self.assertNotIn("TAURI_SIGNING_PRIVATE_KEY", spec.jobs[job].secrets())

    def test_the_linux_package_key_stays_in_signing_custody(self):
        for name in ("release", "release-halt"):
            spec = workflow(name)
            found = holders(spec, PACKAGE_KEY)
            self.assertTrue(found, name)
            for job in found:
                self.assertEqual(spec.jobs[job].environment, SIGNING, f"{name} {job}")
                self.assertFalse(spec.jobs[job].secrets() & PUBLISHING, f"{name} {job}")
        (signer,) = holders(workflow("release-halt"), PACKAGE_KEY)
        for step in workflow("release-halt").jobs[signer].steps:
            if PACKAGE_KEY in step.secrets():
                self.assertIn(["-m", "scripts.linux_release.repository_withdrawal", "resign"],
                              [argv[1:4] for argv in step.commands()])

    def test_storage_writes_follow_the_shared_publication_gate(self):
        release = workflow("release")
        gate = release.jobs["publish-artifacts"]
        self.assertIn("ship-gates", gate.ancestors())
        self.assertFalse(render(gate.condition, {"needs": {"classify": {"outputs": {"publish": "false"}}}}))
        writers = [name for name in holders_of(release, STORAGE) if name not in {gate.name, CHANNEL_SELECTION}]
        self.assertIn("publish-linux-repositories", writers)
        for name in writers:
            ancestors = release.jobs[name].ancestors()
            self.assertLessEqual({"ship-gates", gate.name}, ancestors, name)
            self.assertEqual(release.jobs[name].environment, PUBLICATION, name)
        selection = release.jobs[CHANNEL_SELECTION]
        self.assertIn("ship-gates", selection.ancestors())
        self.assertEqual(selection.environment, PUBLICATION)
        self.assertNotIn(FEED_KEY, selection.secrets())
        self.assertIn("publish-linux-repositories", release.jobs["notify-website"].ancestors())
        delivery = workflow("release-linux-delivery")
        for name in holders_of(delivery, STORAGE):
            self.assertEqual(delivery.jobs[name].environment, PUBLICATION, name)

    def test_the_cache_key_reaches_only_its_gated_publisher(self):
        self.assertEqual(holders(workflow("release"), NIX_KEY), [])
        delivery = workflow("release-linux-delivery")
        (publisher,) = holders(delivery, NIX_KEY)
        job = delivery.jobs[publisher]
        self.assertEqual(job.environment, PUBLICATION)
        rechecks = [step for step in job.steps
                    if ["-m", "scripts.nix_cache.release_lane", "publish"] in [argv[1:4] for argv in step.commands()]]
        self.assertTrue(rechecks)
        self.assertTrue(all("github.token" in step.env.get("GH_TOKEN", "") for step in rechecks))
        self.assertIn(NIX_KEY, set().union(*(job.secrets() for job in workflow("release-secrets-check").jobs.values())))
        caller = workflow("release").jobs["linux-delivery"]
        self.assertLessEqual({"ship-gates", "publish-artifacts", "publish-linux-repositories"}, caller.ancestors())
        admitted = {"classify": {"outputs": {"publish": "true", "linux_public": "true"}},
                    "publish-linux-repositories": {"result": "success"}}
        self.assertTrue(render(caller.condition, {"needs": admitted}))
        for change in ({"classify": {"outputs": {"publish": "false", "linux_public": "true"}}},
                       {"classify": {"outputs": {"publish": "true", "linux_public": "false"}}},
                       {"publish-linux-repositories": {"result": "failure"}}):
            self.assertFalse(render(caller.condition, {"needs": {**admitted, **change}}), change)

    def test_release_jobs_read_only_their_own_run_or_verified_preparation_and_run_on_hosted_runners(self):
        for name in ("release", "release-halt"):
            for job in workflow(name).jobs.values():
                runner = job.raw.get("runs-on", "")
                self.assertNotIn("self-hosted", [runner] if isinstance(runner, str) else runner, job.name)
                for index, step in enumerate(job.steps):
                    self.assertFalse(any(argv[:3] == ["gh", "run", "download"] for argv in step.commands()), job.name)
                    if step.inputs.get("run-id") and (name, job.name) != ("release", REPLAY):
                        # Another run may supply only prepared artifacts, verified against the source before use.
                        self.assertEqual(render(step.inputs["run-id"], PREPARED), "prepared", job.name)
                        checked = [arguments for program, arguments in map(invocation, job.steps[index + 1].commands())
                                   if program == PREPARATION]
                        self.assertIn("verify", sum(checked, []), job.name)
        replay = workflow("release").jobs[REPLAY]
        self.assertIsNone(replay.environment)
        self.assertEqual(replay.secrets(), set())


class ArtifactFlowLaws(unittest.TestCase):
    def test_every_consumed_artifact_is_produced_by_a_job_the_consumer_waits_for(self):
        release = workflow("release")
        producers = {}
        for name, job in release.jobs.items():
            outputs = job.artifacts("upload-artifact")
            called = job.called()
            if called:
                outputs += [artifact for child in called.jobs.values() for artifact in child.artifacts("upload-artifact")]
            for artifact in outputs:
                producers.setdefault(artifact, set()).add(name)
        self.assertEqual(producers["release-public-artifacts"], {"aggregate-release"})
        consumed = 0
        for name, job in release.jobs.items():
            for step in downloads(job):
                if step.inputs.get("run-id"):
                    continue
                consumed += 1
                artifact = step.inputs.get("name") or step.inputs.get("pattern", "").removesuffix("*")
                ancestors = job.ancestors()
                if "needs." in artifact:
                    producer = artifact.split("needs.", 1)[1].split(".outputs.", 1)[0]
                    found = producer in ancestors and release.jobs[producer].artifacts("upload-artifact")
                else:
                    found = any(ancestors & jobs for produced, jobs in producers.items()
                                if produced == artifact or step.inputs.get("pattern") and produced.startswith(artifact))
                self.assertTrue(found, f"{name} downloads {artifact}")
        self.assertTrue(consumed)
        for name in ("linux-repositories", "linux-pacman-repositories", "publish-linux-repositories"):
            self.assertIn("release-public-artifacts", release.jobs[name].artifacts("download-artifact"), name)

    def test_repository_setup_assets_follow_signing_custody_and_shared_admission(self):
        release = workflow("release")
        native = release.jobs["linux-repositories"]
        self.assertEqual(native.environment, SIGNING)
        assembly = next(index for index, step in enumerate(native.steps)
                        if "scripts.linux_release.release_repository_setup" in step.modules())
        self.assertIn(PACKAGE_KEY, native.steps[assembly].secrets())
        uploads = [index for index, step in enumerate(native.steps) if step.action() == "actions/upload-artifact"
                   and step.inputs.get("name") == "linux-repository-setup"]
        self.assertTrue(uploads)
        self.assertTrue(all(index > assembly for index in uploads))
        for name in ("publish-artifacts", "publish-github-release"):
            job = release.jobs[name]
            self.assertLessEqual({native.name, "ship-gates"}, job.ancestors(), name)
            self.assertIn("linux-repository-setup", job.artifacts("download-artifact"), name)

    def test_one_retained_channel_plan_feeds_every_publication_surface(self):
        release = workflow("release")
        consumers = [name for name, job in release.jobs.items()
                     if "release-channel-plan" in job.artifacts("download-artifact")]
        surfaces = {"linux-repositories", "linux-pacman-repositories", "publish-artifacts",
                    "publish-linux-repositories", "publish-immutable", "update-cask", "notify-website",
                    "activate-updater"}
        self.assertLessEqual(surfaces, set(consumers))
        for name in consumers:
            self.assertIn(CHANNEL_SELECTION, release.jobs[name].ancestors(), name)
        self.assertIn("release-channel-plan", release.jobs[CHANNEL_SELECTION].artifacts("upload-artifact"))

    def test_the_distribution_document_records_observed_delivery_before_any_announcement(self):
        release = workflow("release")
        document = release.jobs["publish-immutable"]
        self.assertLessEqual({"ship-gates", "publish-artifacts", "publish-linux-repositories", "linux-delivery"},
                             document.ancestors())
        self.assertEqual(document.environment, PUBLICATION)
        self.assertFalse(render(document.condition, {"needs": {"classify": {"outputs": {"publish": "false"}}}}))
        receipts = {"release-public-artifacts", "linux-repository-setup", "release-metadata",
                    "linux-repository-publication", "nix-cache-publication", "release-channel-plan"}
        self.assertLessEqual(receipts, set(document.artifacts("download-artifact")))
        delivery = workflow("release-linux-delivery")
        self.assertIn("nix-cache-publication",
                      [artifact for job in delivery.jobs.values() for artifact in job.artifacts("upload-artifact")])
        self.assertIn("linux-repository-publication",
                      release.jobs["publish-linux-repositories"].artifacts("upload-artifact"))
        for name in ("update-cask", "notify-website", "activate-updater", "publish-github-release"):
            self.assertIn(document.name, release.jobs[name].ancestors(), name)
        verifier = [step for step in release.jobs["notify-website"].steps
                    if step.action() == "./.github/actions/setup-apt"]
        self.assertTrue(any("minisign" in step.inputs.get("packages", "").split() for step in verifier))


def holders_of(spec, secrets):
    return sorted(name for name, job in spec.jobs.items() if job.secrets() & secrets)


def compiles(step):
    """Whether a step builds the application or a closure; packaging a prepared archive builds nothing."""
    for argv in step.commands():
        if tuple(argv[:2]) in COMPILERS:
            return True
        if argv[0] in {"./task", "task"} and argv[1:2] == ["den:bundle"]:
            if (argv[argv.index("--phase") + 1] if "--phase" in argv else "all") != "package":
                return True
    return False


class ReleaseOrderLaws(unittest.TestCase):
    def setUp(self):
        self.release = workflow("release")
        self.publishing = {name for name, job in self.release.jobs.items()
                           if (job.secrets() & PUBLISHING and name != CHANNEL_SELECTION)
                           or job.uses == "./.github/workflows/release-linux-delivery.yml"
                           or (job.permissions or {}).get("contents") == "write"}

    def test_every_public_side_effect_waits_for_every_build_check_and_upgrade_case(self):
        self.assertLessEqual({"publish-artifacts", "publish-linux-repositories", "linux-delivery",
                              "publish-immutable", "update-cask", "notify-website", "activate-updater",
                              "publish-github-release"}, self.publishing)
        for name in sorted(self.publishing):
            self.assertLessEqual(RELEASE_GATES, self.release.jobs[name].ancestors(), name)

    def test_no_build_check_or_upgrade_case_waits_for_a_publisher(self):
        for name in sorted(RELEASE_GATES):
            self.assertFalse(self.release.jobs[name].ancestors() & self.publishing, name)

    def test_no_release_matrix_runs_one_job_at_a_time(self):
        for name in ("release", "release-prepare", "release-linux-delivery"):
            for job in workflow(name).jobs.values():
                self.assertNotEqual((job.raw.get("strategy") or {}).get("max-parallel"), 1, f"{name} {job.name}")

    def test_the_release_compiles_nothing_and_packages_the_prepared_application(self):
        self.assertEqual(self.release.jobs["prepare"].uses, "./.github/workflows/release-prepare.yml")
        for name in ("release", "release-linux-delivery"):
            for job in workflow(name).jobs.values():
                self.assertFalse(any(compiles(step) for step in job.steps), f"{name} {job.name}")
        self.assertTrue(any("den:bundle" in step.tasks() for step in self.release.jobs["build-release"].steps))


if __name__ == "__main__":
    unittest.main()
