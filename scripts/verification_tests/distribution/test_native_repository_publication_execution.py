"""Local and R2 repository origins activate admitted generations and own recovery and withdrawal."""

import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release.repository_objects import encoded, immutable
from scripts.linux_release.repository_operations import identity
from scripts.linux_release.repository_publication import publish
from scripts.linux_release.repository_r2 import publish_r2
from scripts.verification_tests.distribution.native_repository_fixtures import generation, native_tools, trust

ORIGIN = "https://repository.example.invalid"


class RepositoryR2Tests(unittest.TestCase):
    """The real local origin assembles each upload; only the object store is in memory."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        work = Path(temporary.name)
        self.catalog = trust(work / "trust")
        self.generations = {version: generation("deb-" + version, work) for version in ["0.0.1", "0.0.2"]}
        self.production_catalog = trust(work / "production", rehearsal=False)
        self.version = "0.0.1"
        self.production = False
        self.objects = {}
        self.crash_after = None
        self.events = []
        self.fail = None
        self.reads = 0
        self.race = False

    @property
    def receipt(self):
        receipt = self.generations[self.version][0]
        return {**receipt, "rehearsal": False} if self.production else receipt

    def current(self, key, directory):
        if key.endswith("current.json"):
            self.reads += 1
        if self.race and self.reads == 2:
            return {"generation": "other publisher", "version": "0.0.2", "rehearsal": True}
        return json.loads(self.objects[key]) if key in self.objects else None

    def put(self, key, source, mutable=False):
        self.events.append(("put", key, mutable))
        if self.fail and self.fail in key:
            raise OSError("interrupted upload")
        if not mutable and key in self.objects and self.objects[key] != source.read_bytes():
            raise ValueError("immutable collision")
        self.objects[key] = source.read_bytes()
        if self.crash_after and "/channels/" in key and key.endswith(self.crash_after):
            raise OSError("crash after committed authority bytes")

    def public(self, url, source, mutable=False):
        key = url.removeprefix("https://repository.example.invalid/")
        self.events.append(("readback", key, mutable))
        if self.objects.get(key) != source.read_bytes():
            raise ValueError("public bytes differ")

    def activated(self):
        return [key for key in self.objects if "/channels/" in key and key.endswith(("/InRelease", "/current.json"))]

    def publish(self, expected=None, rehearsal=True, prefix="release-system-tests/native-contract", authorize=None):
        catalog = self.production_catalog if self.production else self.catalog
        with patch("scripts.linux_release.repository_r2.current", side_effect=self.current), \
                patch("scripts.linux_release.repository_r2.put", side_effect=self.put), \
                patch("scripts.linux_release.repository_r2.public_bytes", side_effect=self.public):
            return publish_r2(self.receipt, self.generations[self.version][1], catalog, prefix,
                              "https://repository.example.invalid", expected, rehearsal, authorize=authorize)

    def test_content_and_readback_precede_the_single_apt_authority(self):
        result = self.publish()
        writes = [key for event, key, _ in self.events if event == "put"]
        self.assertTrue(writes[-3].endswith("/InRelease"))
        self.assertTrue(writes[-2].endswith("/current.json"))
        self.assertTrue(writes[-1].endswith("/pending.json"))
        authority = next(index for index, (_, key, _) in enumerate(self.events)
                         if "/channels/" in key and key.endswith("/InRelease"))
        self.assertTrue(any(event == "readback" and "/generations/" in key
                            for event, key, _ in self.events[:authority]))
        self.assertTrue(result["public_verified"])
        self.assertTrue(all(not mutable for _, key, mutable in self.events if "/generations/" in key))
        # A retry after successful activation is safe with the originally reviewed predecessor.
        self.assertEqual(self.publish()["generation"], result["generation"])

    def test_interrupted_upload_never_selects_partial_content_and_retry_recovers(self):
        self.fail = "Packages"
        with self.assertRaises(OSError):
            self.publish()
        self.assertFalse(self.activated())
        self.fail = None
        self.assertTrue(self.publish()["public_verified"])

    def test_stale_and_concurrently_changed_plans_never_activate(self):
        with self.assertRaises(ValueError):
            self.publish("0" * 64)
        self.assertFalse(self.events)
        self.race = True
        self.reads = 0
        with self.assertRaises(ValueError):
            self.publish()
        self.assertFalse(self.activated())

    def test_fixture_namespace_and_production_admission_are_checked_before_storage(self):
        for prefix in ["linux/native", "release-system-tests/../linux", "/release-system-tests/run"]:
            with self.subTest(prefix=prefix), self.assertRaises(ValueError):
                self.publish(prefix=prefix)
        with self.assertRaises(ValueError):
            self.publish(rehearsal=False)
        self.assertFalse(self.events)

    def test_immutable_local_origin_preserves_original_bytes_on_collision(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "selected"
            immutable(path, b"selected")
            immutable(path, b"selected")
            with self.assertRaises(ValueError):
                immutable(path, b"changed")
            self.assertEqual(path.read_bytes(), b"selected")

    def test_production_denial_precedes_every_transport_read_or_write(self):
        self.production = True
        for value in [False, None, 1, {"accepted": True}]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.publish(rehearsal=False, authorize=lambda request: value)
            self.assertFalse(self.events)
            self.assertEqual(self.reads, 0)

    def test_production_authority_is_freshly_checked_before_activation(self):
        self.production = True
        requests = []

        def authorize(request):
            self.assertEqual(set(request), {"manifest", "directory", "catalog", "storage_prefix",
                                           "base_url", "expected", "operation", "phase"})
            self.assertEqual(request["manifest"], self.receipt)
            self.assertIsNot(request["manifest"], self.receipt)
            self.assertEqual(request["base_url"], "https://repository.example.invalid")
            self.assertEqual(request["operation"], "publish")
            self.assertEqual(request["phase"], "activate")
            self.assertIsNone(request["expected"])
            self.assertEqual(request["directory"], self.generations["0.0.1"][1])
            self.assertEqual(request["catalog"], self.production_catalog)
            requests.append(self.reads)
            return len(requests) == 1

        with self.assertRaisesRegex(ValueError, "authorization was refused"):
            self.publish(rehearsal=False, authorize=authorize)
        self.assertEqual(requests, [0, 2])
        self.assertTrue(self.events)
        self.assertFalse(any(key.endswith(("/current.json", "/InRelease")) for key in self.objects if "/channels/" in key))

    def test_production_authority_can_admit_both_bound_checkpoints(self):
        self.production = True
        requests = []

        def authorize(request):
            requests.append(request)
            return True

        result = self.publish(rehearsal=False, authorize=authorize)
        self.assertEqual(len(requests), 2)
        self.assertEqual(requests[0], requests[1])
        self.assertEqual(result["publication_phase"], "activated")

    def test_changed_predecessor_refuses_before_activation_authorization(self):
        self.production = True
        self.race = True
        calls = []

        def authorize(request):
            calls.append(request)
            return True

        with self.assertRaisesRegex(ValueError, "changed while"):
            self.publish(rehearsal=False, authorize=authorize)
        self.assertEqual(len(calls), 1)
        self.assertFalse(any(key.endswith("/current.json") for key in self.objects))

    def test_crashes_require_exact_operation_recovery_and_preserve_old_objects(self):
        for crash in ["/InRelease", "/current.json"]:
            with self.subTest(crash=crash):
                self.objects, self.events, self.reads = {}, [], 0
                self.version = "0.0.1"
                original = identity(self.receipt)
                self.crash_after = crash
                with self.assertRaisesRegex(OSError, "crash after"):
                    self.publish()
                pending_key = next(key for key in self.objects if key.endswith("/pending.json"))
                self.assertEqual(json.loads(self.objects[pending_key])["state"], "pending")
                retained = {key: value for key, value in self.objects.items() if "/generations/" in key}
                before = dict(self.objects)
                self.crash_after = None
                self.version = "0.0.2"
                predecessor = original if crash == "/current.json" else None
                with self.assertRaisesRegex(ValueError, "pending activation"):
                    self.publish(expected=predecessor)
                self.assertEqual(self.objects, before)
                self.version = "0.0.1"
                self.assertEqual(self.publish()["generation"], original)
                self.assertEqual(json.loads(self.objects[pending_key])["state"], "completed")
                self.version = "0.0.2"
                self.assertEqual(self.publish(expected=original)["version"], "0.0.2")
                self.assertTrue(all(self.objects[key] == value for key, value in retained.items()))


class RepositoryPublicationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.catalog = trust(self.work / "trust")
        self.store = self.work / "origin"
        self.first = generation("deb-0.0.1", self.work)
        self.second = generation("deb-0.0.2", self.work)

    def publish(self, selected, expected=None, operation="publish", **options):
        receipt, directory = selected
        arguments = {"base_url": ORIGIN, "catalog": self.catalog, "rehearsal": True, **options}
        return publish(receipt, directory, self.store, arguments["base_url"], arguments["catalog"], expected,
                       arguments["rehearsal"], operation)

    def state(self, family="deb"):
        return json.loads((self.store / f"channels/{family}/preview/current.json").read_text())

    def test_apt_activation_retains_content_and_replaces_the_signed_authority_last(self):
        record = self.publish(self.first)
        channel = self.store / "channels/deb/preview"
        receipt = self.first[0]
        self.assertEqual(record["generation"], identity(receipt))
        self.assertEqual(self.state(), record)
        self.assertEqual((channel / "dists/preview/InRelease").read_bytes(),
                         (self.first[1] / "dists/preview/InRelease").read_bytes())
        retained = self.store / "generations" / record["generation"]
        self.assertEqual((retained / "generation.json").read_bytes(), encoded(receipt))
        configuration = (retained / "client.conf").read_text()
        self.assertIn(f"URIs: {ORIGIN}/channels/deb/preview/", configuration)
        self.assertIn("Signed-By: /etc/apt/keyrings/painted-wolf-code.asc", configuration)
        self.assertNotIn("secret", " ".join(path.name for path in retained.rglob("*")))
        self.assertEqual(self.publish(self.first), record)
        advanced = self.publish(self.second, record["generation"])
        self.assertEqual((advanced["previous"], advanced["version"]), (record["generation"], "0.0.2"))
        for row in receipt["objects"]:
            if row["path"].startswith("pool/") or "/by-hash/" in row["path"]:
                self.assertTrue((channel / row["path"]).is_file(), row["path"])
        self.assertEqual(self.publish(self.second, record["generation"]), advanced)

    def test_stale_regressing_or_unretained_plans_leave_the_active_state_unchanged(self):
        first = self.publish(self.first)
        second = self.publish(self.second, first["generation"])
        before = self.state()
        for selected, expected, operation, reason in [
                (self.second, "0" * 64, "publish", "generation operation must select"),
                (self.first, second["generation"], "publish", "must advance"),
                (self.first, "0" * 64, "recover", "changed since the reviewed"),
                (self.first, None, "publish", "changed since the reviewed")]:
            with self.subTest(reason=reason), self.assertRaisesRegex(ValueError, reason):
                self.publish(selected, expected, operation)
            self.assertEqual(self.state(), before)
        fresh = self.work / "fresh"
        self.store = fresh
        for operation in ["recover", "withdraw", "rotate"]:
            with self.subTest(operation=operation), self.assertRaisesRegex(ValueError, "initial publish"):
                self.publish(self.first, None, operation)
        with self.assertRaisesRegex(ValueError, "initial publish"):
            self.publish(self.first, "0" * 64)

    def test_withdrawal_restores_retained_last_good_and_permanently_refuses_the_bad_generation(self):
        good = self.publish(self.first)
        bad = self.publish(self.second, good["generation"])
        withdrawn = self.publish(self.first, bad["generation"], "withdraw")
        self.assertEqual((withdrawn["operation"], withdrawn["withdrawn_version"]), ("withdraw", "0.0.2"))
        self.assertEqual((self.store / "channels/deb/preview/dists/preview/InRelease").read_bytes(),
                         (self.first[1] / "dists/preview/InRelease").read_bytes())
        marker = json.loads((self.store / "withdrawn" / (bad["generation"] + ".json")).read_text())
        self.assertEqual((marker["version"], marker["replacement_generation"]), ("0.0.2", good["generation"]))
        self.assertTrue((self.store / "withdrawn-versions/deb/preview/all/0.0.2.json").is_file())
        self.assertEqual(self.publish(self.first, bad["generation"], "withdraw"), withdrawn)
        for operation in ["publish", "recover"]:
            with self.subTest(operation=operation), self.assertRaisesRegex(ValueError, "cannot be offered again"):
                self.publish(self.second, withdrawn["generation"], operation)
        self.assertTrue((self.store / "generations" / bad["generation"] / "generation.json").is_file())
        self.assertEqual(self.state(), withdrawn)

    def test_recovery_requires_the_exact_retained_generation(self):
        good = self.publish(self.first)
        self.publish(self.second, good["generation"])
        retained = self.store / "generations" / good["generation"] / "generation.json"
        retained.unlink()
        with self.assertRaisesRegex(ValueError, "exact retained healthy generation"):
            self.publish(self.first, self.state()["generation"], "recover")

    def test_admission_and_origin_refuse_before_any_store_write(self):
        receipt, directory = self.first
        package = directory / receipt["packages"][0]["file"]
        package.write_bytes(package.read_bytes() + b"changed")
        with self.assertRaisesRegex(ValueError, "selected size"):
            self.publish(self.first)
        package.write_bytes(package.read_bytes()[:-len(b"changed")])
        for url in ["http://repository.example.invalid", "https://user@repository.example.invalid",
                    "https://repository.example.invalid/?q=1", "file:///srv/repository"]:
            with self.subTest(url=url), self.assertRaisesRegex(ValueError, "HTTPS URL"):
                self.publish(self.first, base_url=url)
        with self.assertRaisesRegex(ValueError, "cross-trust"):
            self.publish(self.first, rehearsal=False)
        self.assertFalse(self.store.exists())
        self.store.parent.joinpath("real-origin").mkdir()
        self.store.symlink_to(self.store.parent / "real-origin")
        with self.assertRaisesRegex(ValueError, "cannot be a symlink"):
            self.publish(self.first)

    def test_dnf_mirrorlist_selects_one_immutable_generation_base_url(self):
        native_tools("rpm", "rpmkeys", "gpg")
        rpm = generation("rpm-0.0.1", self.work)
        record = self.publish(rpm)
        mirrorlist = (self.store / "channels/rpm/preview/mirrorlist").read_text()
        self.assertEqual(mirrorlist, f"{ORIGIN}/generations/{record['generation']}/\n")
        retained = self.store / "generations" / record["generation"]
        self.assertTrue((retained / "repodata/repomd.xml.asc").is_file())
        configuration = (retained / "client.conf").read_text()
        self.assertIn("repo_gpgcheck=1", configuration)
        self.assertIn(f"mirrorlist={ORIGIN}/channels/rpm/preview/mirrorlist", configuration)
        self.assertEqual(self.state("rpm"), record)


if __name__ == "__main__":
    unittest.main()
