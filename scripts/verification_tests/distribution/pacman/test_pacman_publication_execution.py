"""Local and R2 pacman origins bind signatures before databases and own recovery and withdrawal."""

import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release.pacman_publication import publish
from scripts.linux_release.pacman_r2 import publish_r2
from scripts.linux_release.repository_objects import encoded
from scripts.linux_release.repository_operations import identity
from scripts.verification_tests.distribution.pacman_repository_fixtures import generation, trust

PACKAGE_FILE = "paintedwolf-repository-fixture-0.0.1-1-x86_64.pkg.tar.zst"
ORIGIN = "https://repository.example.invalid"
DATABASE = "painted-wolf-code-preview"


class PacmanPublicationTests(unittest.TestCase):
    """The real local origin assembles each upload; only the object store is in memory."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        work = Path(temporary.name)
        self.catalog = trust(work / "trust")
        self.production_catalog = trust(work / "production", rehearsal=False)
        self.generations = {version: generation(f"pacman-{version}-x86_64", work) for version in ["0.0.1", "0.0.2"]}
        self.version, self.production = "0.0.1", False
        self.objects, self.events = {}, []
        self.crash_after = None
        self.fail_suffix, self.race, self.reads = None, False, 0

    @property
    def receipt(self):
        receipt = self.generations[self.version][0]
        return {**receipt, "rehearsal": False} if self.production else receipt

    def current(self, key, directory):
        if key.endswith("current.json"):
            self.reads += 1
        if self.race and self.reads == 2:
            return {"generation": "other"}
        return json.loads(self.objects[key]) if key in self.objects else None

    def put(self, key, source, mutable=False):
        self.events.append((key, mutable))
        if self.fail_suffix and "/channels/" in key and key.endswith(self.fail_suffix):
            raise OSError("interrupted activation")
        if not mutable and key in self.objects and self.objects[key] != source.read_bytes():
            raise ValueError("immutable collision")
        self.objects[key] = source.read_bytes()
        if self.crash_after and "/channels/" in key and key.endswith(self.crash_after):
            raise OSError("crash after committed authority bytes")

    def public(self, url, source, mutable=False):
        key = url.removeprefix("https://repository.example.invalid/")
        if self.objects.get(key) != source.read_bytes():
            raise ValueError("public bytes differ")

    def publish(self, expected=None, rehearsal=True, authorize=None):
        catalog = self.production_catalog if self.production else self.catalog
        with patch("scripts.linux_release.pacman_r2.current", side_effect=self.current), \
                patch("scripts.linux_release.pacman_r2.put", side_effect=self.put), \
                patch("scripts.linux_release.pacman_r2.public_bytes", side_effect=self.public):
            return publish_r2(self.receipt, self.generations[self.version][1], catalog,
                              "release-system-tests/pacman-contract", "https://repository.example.invalid",
                              expected, rehearsal, authorize=authorize)

    def test_packages_and_signatures_precede_database_alias_and_state_is_last(self):
        result = self.publish()
        writes = [key for key, _ in self.events if "/channels/" in key]
        db = next(index for index, key in enumerate(writes) if key.endswith("/painted-wolf-code-preview.db"))
        signature = next(index for index, key in enumerate(writes) if key.endswith("/painted-wolf-code-preview.db.sig"))
        self.assertLess(signature, db)
        self.assertTrue(writes[-2].endswith("/current.json"))
        self.assertTrue(writes[-1].endswith("/pending.json"))
        self.assertTrue(all(not mutable for key, mutable in self.events if "/generations/" in key))
        self.assertTrue(result["public_verified"])
        self.assertEqual(self.publish()["generation"], result["generation"])

    def test_interrupted_pair_upload_preserves_objects_and_retry_finishes(self):
        self.fail_suffix = "/painted-wolf-code-preview.db.sig"
        with self.assertRaises(OSError):
            self.publish()
        self.assertTrue(any(key.endswith(PACKAGE_FILE) for key in self.objects if "/channels/" in key))
        self.assertFalse(any(key.endswith(("/painted-wolf-code-preview.db", "/current.json")) for key in self.objects if "/channels/" in key))
        self.fail_suffix = None
        self.assertTrue(self.publish()["public_verified"])

    def test_stale_or_concurrently_changed_plan_never_selects_database(self):
        with self.assertRaises(ValueError):
            self.publish("0" * 64)
        self.assertFalse(self.events)
        self.race, self.reads = True, 0
        with self.assertRaises(ValueError):
            self.publish()
        self.assertFalse(any(key.endswith(("/painted-wolf-code-preview.db", "/current.json")) for key in self.objects if "/channels/" in key))

    def test_production_path_requires_controller_admission(self):
        with self.assertRaises(ValueError):
            self.publish(rehearsal=False)
        self.assertFalse(self.events)

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
        self.assertFalse(any(key.endswith(("/current.json", "/painted-wolf-code-preview.db")) for key in self.objects if "/channels/" in key))

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
        for crash in ["/painted-wolf-code-preview.db", "/current.json"]:
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


class PacmanOriginTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.catalog = trust(self.work / "trust")
        self.store = self.work / "origin"
        self.first = generation("pacman-0.0.1-x86_64", self.work)
        self.second = generation("pacman-0.0.2-x86_64", self.work)
        self.channel = self.store / "channels/pacman/preview/x86_64"

    def publish(self, selected, expected=None, operation="publish", base_url=ORIGIN, rehearsal=True):
        receipt, directory = selected
        return publish(receipt, directory, self.store, base_url, self.catalog, expected, rehearsal, operation)

    def state(self):
        return json.loads((self.channel / "current.json").read_text())

    def test_database_pairs_activate_and_old_packages_remain_available(self):
        first = self.publish(self.first)
        self.assertEqual((first["generation"], self.state()), (identity(self.first[0]), first))
        retained = self.store / "generations" / first["generation"]
        self.assertEqual((retained / "generation.json").read_bytes(), encoded(self.first[0]))
        configuration = (retained / "client.conf").read_text()
        self.assertIn("SigLevel = Required DatabaseRequired", configuration)
        self.assertIn(f"Server = {ORIGIN}/channels/pacman/preview/x86_64/", configuration)
        for suffix in [".db", ".db.sig", ".files", ".files.sig"]:
            self.assertEqual((self.channel / (DATABASE + suffix)).read_bytes(),
                             (self.first[1] / (DATABASE + suffix)).read_bytes())
        self.assertEqual(self.publish(self.first), first)
        second = self.publish(self.second, first["generation"])
        self.assertEqual((second["previous"], second["version"]), (first["generation"], "0.0.2"))
        for receipt, _ in [self.first, self.second]:
            for row in receipt["packages"]:
                self.assertTrue((self.channel / row["file"]).is_file())
                self.assertTrue((self.channel / (row["file"] + ".sig")).is_file())
        self.assertEqual((self.channel / (DATABASE + ".db")).read_bytes(),
                         (self.second[1] / (DATABASE + ".db")).read_bytes())

    def test_stale_regressing_and_reoffered_plans_leave_the_active_state_unchanged(self):
        first = self.publish(self.first)
        second = self.publish(self.second, first["generation"])
        before = self.state()
        for selected, expected, operation, reason in [
                (self.second, "0" * 64, "publish", "generation operation must select"),
                (self.first, second["generation"], "publish", "must advance"),
                (self.first, None, "recover", "changed since the reviewed"),
                (self.first, second["generation"], "rotate", "newer signing generation")]:
            with self.subTest(reason=reason), self.assertRaisesRegex(ValueError, reason):
                self.publish(selected, expected, operation)
            self.assertEqual(self.state(), before)

    def test_withdrawal_restores_last_good_and_permanently_refuses_the_bad_generation(self):
        good = self.publish(self.first)
        bad = self.publish(self.second, good["generation"])
        withdrawn = self.publish(self.first, bad["generation"], "withdraw")
        self.assertEqual((withdrawn["operation"], withdrawn["withdrawn_version"]), ("withdraw", "0.0.2"))
        self.assertEqual((self.channel / (DATABASE + ".db")).read_bytes(),
                         (self.first[1] / (DATABASE + ".db")).read_bytes())
        self.assertTrue((self.store / "withdrawn" / (bad["generation"] + ".json")).is_file())
        self.assertTrue((self.store / "withdrawn-versions/pacman/preview/x86_64/0.0.2.json").is_file())
        for operation in ["publish", "recover"]:
            with self.subTest(operation=operation), self.assertRaisesRegex(ValueError, "cannot be offered again"):
                self.publish(self.second, withdrawn["generation"], operation)
        self.assertTrue((self.channel / self.second[0]["packages"][0]["file"]).is_file())
        self.assertEqual(self.state(), withdrawn)

    def test_recovery_requires_the_exact_retained_generation(self):
        good = self.publish(self.first)
        self.publish(self.second, good["generation"])
        (self.store / "generations" / good["generation"] / "generation.json").unlink()
        with self.assertRaisesRegex(ValueError, "exact retained healthy generation"):
            self.publish(self.first, self.state()["generation"], "recover")

    def test_admission_and_origin_refuse_before_activation(self):
        receipt, directory = self.first
        package = directory / receipt["packages"][0]["file"]
        original = package.read_bytes()
        package.write_bytes(original + b"changed")
        with self.assertRaisesRegex(ValueError, "selected final bytes"):
            self.publish(self.first)
        package.write_bytes(original)
        with self.assertRaisesRegex(ValueError, "cross-trust"):
            self.publish(self.first, rehearsal=False)
        self.assertFalse(self.store.exists())
        for url in ["http://repository.example.invalid", "https://user@repository.example.invalid",
                    "https://repository.example.invalid/?q=1"]:
            with self.subTest(url=url), self.assertRaisesRegex(ValueError, "HTTPS URL"):
                self.publish(self.first, base_url=url)
            self.assertFalse((self.channel / "current.json").exists())


if __name__ == "__main__":
    unittest.main()
