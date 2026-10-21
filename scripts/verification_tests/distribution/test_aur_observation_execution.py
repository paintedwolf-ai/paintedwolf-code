"""Availability requires fresh origin content, not a saved publisher receipt."""
import copy
import hashlib
from pathlib import Path
import sys
import tempfile
import unittest

from scripts.linux_release.aur_delivery import FixturePublisher, recipe_objects
from scripts.linux_release.aur_observation import observe
from scripts.linux_release.aur_origin import FixtureOrigin, ReadOnlyOrigin
from scripts.linux_release.repository_objects import encoded


def selection(root):
    recipe = root / "recipe"
    recipe.mkdir()
    (recipe / "PKGBUILD").write_text("pkgname=painted-wolf-code-bin\npkgver=1.0.0\n")
    (recipe / ".SRCINFO").write_text("pkgbase = painted-wolf-code-bin\n\tpkgver = 1.0.0\n")
    objects = recipe_objects(recipe)
    selected = {"format_version": 1, "operation": "publish-aur", "source_commit": "a" * 40,
                "version": "1.0.0", "channel": "stable", "package": "painted-wolf-code-bin",
                "rehearsal": True, "final_artifacts_sha256": "b" * 64, "generations": [],
                "recipe_objects": objects, "recipe_sha256": hashlib.sha256(encoded(objects)).hexdigest()}
    return recipe, selected


class MemoryOrigin:
    production = False
    origin = "/fixture-only"

    def __init__(self, selected, recipe):
        self.commit = "c" * 40
        self.heads = [self.commit, self.commit]
        self.value = {"commit": self.commit, "message": encoded({**selected, "previous": None}),
                      "objects": {row["path"]: (recipe / row["path"]).read_bytes()
                                  for row in selected["recipe_objects"]}}

    def head(self):
        return self.heads.pop(0)

    def read(self, commit):
        return copy.deepcopy(self.value)


class AvailabilityTests(unittest.TestCase):
    def test_fresh_observation_binds_recipe_and_complete_inventory_without_production_claim(self):
        with tempfile.TemporaryDirectory() as temporary:
            recipe, selected = selection(Path(temporary))
            origin = MemoryOrigin(selected, recipe)
            calls = []
            def authorize(plan, actual):
                calls.append((copy.deepcopy(plan), actual))
                return {"accepted": True}
            report = observe(selected, recipe, origin.commit, backend=origin, authorize=authorize)
            self.assertEqual(len(calls), 2)
            self.assertFalse(report["production_available"])
            self.assertEqual(report["facts"]["source_sha256"], selected["recipe_sha256"])
            self.assertEqual(report["facts"]["package_sha256"], selected["final_artifacts_sha256"])
            self.assertEqual(report["identity_semantics"]["package_sha256"], "complete-final-artifact-inventory")

    def test_drift_tampering_forged_plan_and_extra_files_cannot_advertise_availability(self):
        for failure in ("head", "blob", "message", "extra"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary:
                recipe, selected = selection(Path(temporary))
                origin = MemoryOrigin(selected, recipe)
                if failure == "head":
                    origin.heads[-1] = "d" * 40
                elif failure == "blob":
                    origin.value["objects"]["PKGBUILD"] += b"tampered"
                elif failure == "message":
                    origin.value["message"] = encoded({**selected, "version": "9.0.0", "previous": None})
                else:
                    origin.value["objects"]["unexpected.install"] = b"foreign"
                with self.assertRaises(ValueError):
                    observe(selected, recipe, origin.commit, backend=origin,
                            authorize=lambda *_: {"accepted": True})

    def test_second_admission_and_local_recipe_must_remain_fresh(self):
        with tempfile.TemporaryDirectory() as temporary:
            recipe, selected = selection(Path(temporary))
            origin = MemoryOrigin(selected, recipe)
            calls = []
            def authorize(*_):
                calls.append(1)
                return {"accepted": len(calls) == 1}
            with self.assertRaisesRegex(ValueError, "admission changed"):
                observe(selected, recipe, origin.commit, backend=origin, authorize=authorize)

    def test_fixture_origin_cannot_supply_production_authority(self):
        with tempfile.TemporaryDirectory() as temporary:
            recipe, selected = selection(Path(temporary))
            origin = MemoryOrigin(selected, recipe)
            selected["rehearsal"] = False
            with self.assertRaisesRegex(ValueError, "authority cannot be exchanged"):
                observe(selected, recipe, origin.commit, backend=origin, authorize=lambda *_: {"accepted": True})

    def test_protected_origin_is_fixed_and_never_accepts_an_operator_url(self):
        with self.assertRaisesRegex(ValueError, "maintained"):
            ReadOnlyOrigin(Path("/unused"), "foreign-package")


@unittest.skipUnless(sys.platform.startswith("linux"), "requires the protected Linux Git process lifecycle")
class RealLocalGitTests(unittest.TestCase):
    def test_independent_readback_of_real_local_publisher_commit_and_cleanup(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            recipe, selected = selection(root)
            publishing = root / "publisher"
            publishing.mkdir(mode=0o700)
            publisher = FixturePublisher(publishing)
            Path(publisher.origin).chmod(0o700)
            delivered = publisher.publish(selected, recipe, None)
            reader_root = root / "reader"
            with FixtureOrigin(reader_root, Path(publisher.origin)) as backend:
                report = observe(selected, recipe, delivered["commit"], backend=backend,
                                 authorize=lambda *_: {"accepted": True})
            self.assertFalse(reader_root.exists())
            self.assertFalse(report["production_available"])
            self.assertEqual(report["facts"]["commit"], delivered["commit"])
            self.assertEqual(publisher.head(), delivered["commit"])

    def test_moved_real_origin_never_reuses_a_retained_old_availability_receipt(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            recipe, selected = selection(root)
            publishing = root / "publisher"
            publishing.mkdir(mode=0o700)
            publisher = FixturePublisher(publishing)
            Path(publisher.origin).chmod(0o700)
            first = publisher.publish(selected, recipe, None)
            later = {**selected, "version": "1.0.1"}
            publisher.publish(later, recipe, first["commit"])
            with FixtureOrigin(root / "reader", Path(publisher.origin)) as backend:
                with self.assertRaisesRegex(ValueError, "no longer offers"):
                    observe(selected, recipe, first["commit"], backend=backend,
                            authorize=lambda *_: {"accepted": True})

    def test_committed_symlink_is_refused_without_checking_out_or_executing_recipe(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            recipe, selected = selection(root)
            publishing = root / "publisher"
            publishing.mkdir(mode=0o700)
            publisher = FixturePublisher(publishing)
            Path(publisher.origin).chmod(0o700)
            first = publisher.publish(selected, recipe, None)
            checkout = publishing / "checkout-1"
            (checkout / "PKGBUILD").unlink()
            (checkout / "PKGBUILD").symlink_to(".SRCINFO")
            publisher.git("-C", checkout, "add", "--", "PKGBUILD")
            publisher.git("-C", checkout, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                          "commit", "-m", encoded({**selected, "previous": first["commit"]}).decode().strip())
            publisher.git("-C", checkout, "push", "origin", "HEAD:refs/heads/master")
            changed = publisher.head()
            reader = root / "reader"
            with FixtureOrigin(reader, Path(publisher.origin)) as backend:
                with self.assertRaisesRegex(ValueError, "nonregular"):
                    observe(selected, recipe, changed, backend=backend,
                            authorize=lambda *_: {"accepted": True})
            self.assertFalse(reader.exists())
