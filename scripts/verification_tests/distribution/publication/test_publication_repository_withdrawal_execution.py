"""Release halt withdraws halted versions from apt, dnf and pacman channels through their owners."""

import contextlib
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release.publication.delivery import repository_withdrawal as withdrawal
from scripts.linux_release.publication.trust.authority import PREFIX
from scripts.linux_release.repository_operations import identity, version_marker
from scripts.verification_tests.distribution import native_repository_fixtures as native
from scripts.verification_tests.distribution import pacman_repository_fixtures as pacman

ORIGIN = "https://repository.example.invalid"


class Store:
    """The only fake is the object store; publication, tombstones and admission are real."""

    def __init__(self):
        self.objects, self.writes = {}, []

    def current(self, key, directory):
        return json.loads(self.objects[key]) if key in self.objects else None

    def put(self, key, source, mutable=False):
        self.writes.append(key)
        if not mutable and key in self.objects and self.objects[key] != source.read_bytes():
            raise ValueError("immutable collision")
        self.objects[key] = source.read_bytes()

    def public(self, url, source, mutable=False, **_):
        if self.objects.get(url.removeprefix(ORIGIN + "/")) != source.read_bytes():
            raise ValueError("public bytes differ")

    def read(self, name):
        return self.current(PREFIX + "/" + name, None)

    def fetch(self, name, target, expected):
        withdrawal.verified_copy(io.BytesIO(self.objects[PREFIX + "/" + name]), target, name, expected)

    @contextlib.contextmanager
    def transport(self):
        with contextlib.ExitStack() as stack:
            for module in ["repository_r2", "pacman_r2"]:
                for name, fake in [("current", self.current), ("put", self.put), ("public_bytes", self.public)]:
                    stack.enter_context(patch(f"scripts.linux_release.{module}.{name}", side_effect=fake))
            yield


class Halt(unittest.TestCase):
    fixtures, families = None, None

    def setUp(self):
        native.native_tools("gpg")
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.catalog = self.fixtures.trust(self.work / "trust", rehearsal=False)
        self.store = Store()
        self.generations = {}
        for name in self.families:
            for version in ["0.0.1", "0.0.2"]:
                receipt, directory = self.fixtures.generation(name.format(version=version), self.work)
                self.generations[(name, version)] = ({**receipt, "rehearsal": False}, directory)

    def publish(self, name, version, expected=None):
        receipt, directory = self.generations[(name, version)]
        with self.store.transport():
            return withdrawal.transport(receipt["family"])(
                receipt, directory, self.catalog, PREFIX, ORIGIN, expected, False, authorize=lambda request: True)

    def release(self):
        """Every channel offers 0.0.2 after an ordinary 0.0.1 → 0.0.2 publication."""
        for name in self.families:
            first = self.publish(name, "0.0.1")
            self.publish(name, "0.0.2", first["generation"])

    def distribution(self, bad="0.0.2", last_good="0.0.1"):
        return {"channels": [{"channel": "preview", "bad": bad, "last_good": last_good}]}

    def prepare(self, **changes):
        work = self.work / "halt"
        if work.exists():
            work = Path(tempfile.mkdtemp(dir=self.work))
        return withdrawal.prepare(changes.pop("distribution", self.distribution()), read=self.store.read,
                                  fetch=self.store.fetch, work=work, catalog=self.catalog,
                                  storage_prefix=PREFIX, origin=ORIGIN)

    def apply(self, plan):
        with self.store.transport():
            return withdrawal.apply(plan, read=self.store.read)

    def replaced(self, plan):
        return [entry for entry in plan["channels"] if entry["state"] == "replace"]


class NativeChannelHalt(Halt):
    fixtures, families = native, ["deb-{version}", "rpm-{version}"]

    def test_preparation_is_read_only_and_selects_each_retained_predecessor(self):
        self.release()
        before = dict(self.store.objects)
        self.store.writes.clear()
        plan = self.prepare()
        self.assertEqual((self.store.writes, self.store.objects), ([], before))
        states = {(entry["family"], entry["architecture"]): entry["state"] for entry in plan["channels"]}
        self.assertEqual(states, {("deb", None): "replace", ("rpm", None): "replace",
                                  ("pacman", "x86_64"): "absent", ("pacman", "aarch64"): "absent"})
        for entry in self.replaced(plan):
            healthy = self.generations[(entry["family"] + "-{version}", "0.0.1")][0]
            self.assertEqual(entry["replacement"], healthy)
            self.assertEqual(entry["active"]["previous"], identity(healthy))

    def test_withdrawal_restores_the_predecessor_and_tombstones_the_halted_generation(self):
        self.release()
        bad = {entry["family"]: entry["active"]["generation"] for entry in self.replaced(self.prepare())}
        results = self.apply(self.prepare())
        self.assertEqual({row["state"] for row in results}, {"withdrawn", "absent"})
        for family in ["deb", "rpm"]:
            healthy = self.generations[(family + "-{version}", "0.0.1")][0]
            state = self.store.read(f"channels/{family}/preview/current.json")
            self.assertEqual((state["operation"], state["generation"], state["previous"], state["withdrawn_version"]),
                             ("withdraw", identity(healthy), bad[family], "0.0.2"))
            self.assertIsNotNone(self.store.read(f"withdrawn/{bad[family]}.json"))
            marker = version_marker({**healthy, "version": "0.0.2"}).as_posix()
            self.assertIsNotNone(self.store.read(marker))
        retained = self.generations[("deb-{version}", "0.0.1")][1] / "dists/preview/InRelease"
        self.assertEqual(self.store.objects[PREFIX + "/channels/deb/preview/dists/preview/InRelease"],
                         retained.read_bytes())

    def test_an_interrupted_or_repeated_halt_converges_without_new_state(self):
        self.release()
        plan = self.prepare()
        self.apply(plan)
        settled = dict(self.store.objects)
        self.apply(plan)
        self.assertEqual(self.store.objects, settled)
        again = self.prepare()
        self.assertEqual({entry["state"] for entry in again["channels"]}, {"withdrawn", "absent"})
        self.apply(again)
        self.assertEqual(self.store.objects, settled)

    def test_a_halted_version_without_a_named_replacement_is_refused(self):
        self.release()
        with self.assertRaisesRegex(ValueError, "name a last-good release"):
            self.prepare(distribution=self.distribution(last_good=None))

    def test_the_retained_predecessor_must_be_the_selected_last_good(self):
        self.release()
        with self.assertRaisesRegex(ValueError, "not the selected last-good 0.0.0"):
            self.prepare(distribution=self.distribution(last_good="0.0.0"))

    def test_a_first_publication_has_no_predecessor_to_restore(self):
        self.publish("deb-{version}", "0.0.1")
        with self.assertRaisesRegex(ValueError, "no retained predecessor"):
            self.prepare(distribution=self.distribution(bad="0.0.1", last_good="0.0.0"))

    def test_unaffected_and_disabled_channels_are_preserved(self):
        self.release()
        before = dict(self.store.objects)
        plan = self.prepare(distribution=self.distribution(bad="0.0.3", last_good="0.0.2"))
        self.assertEqual({entry["state"] for entry in plan["channels"]}, {"unaffected", "absent"})
        self.apply(plan)
        self.assertEqual(self.store.objects, before)

    def test_tampered_retained_bytes_are_refused_before_any_write(self):
        self.release()
        healthy = identity(self.generations[("deb-{version}", "0.0.1")][0])
        key = next(name for name in self.store.objects
                   if name.startswith(f"{PREFIX}/generations/{healthy}/") and name.endswith("/Packages"))
        self.store.objects[key] += b"Package: injected\n"
        self.store.writes.clear()
        with self.assertRaisesRegex(ValueError, "differs from its generation inventory"):
            self.prepare()
        self.assertEqual(self.store.writes, [])

    def test_a_substituted_predecessor_record_is_refused(self):
        self.release()
        healthy = identity(self.generations[("deb-{version}", "0.0.1")][0])
        key = f"{PREFIX}/generations/{healthy}/generation.json"
        forged = json.loads(self.store.objects[key])
        forged["version"] = "0.0.1-forged"
        self.store.objects[key] = json.dumps(forged).encode()
        with self.assertRaisesRegex(ValueError, "differs from its retained immutable generation"):
            self.prepare()

    def test_a_predecessor_signed_by_another_key_generation_is_refused(self):
        self.release()
        key = f"{PREFIX}/channels/deb/preview/current.json"
        active = json.loads(self.store.objects[key])
        self.store.objects[key] = json.dumps({**active, "signing_generation": active["signing_generation"] + 1}).encode()
        with self.assertRaisesRegex(ValueError, "another key generation"):
            self.prepare()

    def test_a_previously_withdrawn_predecessor_is_never_offered_again(self):
        self.release()
        healthy = identity(self.generations[("rpm-{version}", "0.0.1")][0])
        self.store.objects[f"{PREFIX}/withdrawn/{healthy}.json"] = b"{}"
        with self.assertRaisesRegex(ValueError, "itself withdrawn"):
            self.prepare()

    def test_a_channel_that_changes_after_preparation_is_not_overwritten(self):
        self.release()
        plan = self.prepare()
        key = f"{PREFIX}/channels/rpm/preview/current.json"
        self.store.objects[key] = json.dumps({**json.loads(self.store.objects[key]), "version": "0.0.3"}).encode()
        with self.assertRaisesRegex(ValueError, "rpm/preview changed since the halt was prepared"):
            self.apply(plan)
        deb = self.store.read("channels/deb/preview/current.json")
        self.assertEqual(deb["operation"], "withdraw")

    def test_a_disabled_channel_enabled_after_preparation_is_refused(self):
        self.publish("deb-{version}", "0.0.1")
        plan = self.prepare(distribution=self.distribution(bad="0.0.2"))
        self.publish("rpm-{version}", "0.0.1")
        with self.assertRaisesRegex(ValueError, "rpm/preview changed"):
            self.apply(plan)

    def test_an_unsupported_active_state_is_refused(self):
        self.release()
        key = f"{PREFIX}/channels/deb/preview/current.json"
        self.store.objects[key] = json.dumps({**json.loads(self.store.objects[key]), "rehearsal": True}).encode()
        with self.assertRaisesRegex(ValueError, "unsupported active state"):
            self.prepare()

    def test_authority_admits_only_the_prepared_request(self):
        self.release()
        plan = self.prepare()
        entry = self.replaced(plan)[0]
        authority = withdrawal.WithdrawalAuthority(plan, entry, self.store.read)
        request = {"manifest": entry["replacement"], "directory": Path(entry["directory"]),
                   "catalog": self.catalog, "storage_prefix": PREFIX, "base_url": ORIGIN,
                   "expected": entry["active"]["generation"], "operation": "withdraw", "phase": "activate"}
        self.assertIs(authority(request), True)
        bad = self.generations[(entry["family"] + "-{version}", "0.0.2")]
        for change in [{"operation": "publish"}, {"phase": "stage"}, {"storage_prefix": "linux/other"},
                       {"base_url": "https://elsewhere.example.invalid"}, {"catalog": native.TESTDATA / "other-keys.json"},
                       {"expected": "0" * 64}, {"manifest": bad[0]}, {"directory": bad[1]}]:
            with self.subTest(change=sorted(change)), self.assertRaises(ValueError):
                authority({**request, **change})
        with self.assertRaises(ValueError):
            authority({key: value for key, value in request.items() if key != "phase"})
        (Path(entry["directory"]) / entry["replacement"]["objects"][0]["path"]).write_bytes(b"replaced")
        with self.assertRaises(ValueError):
            authority(request)


class PacmanChannelHalt(Halt):
    fixtures, families = pacman, ["pacman-{version}-x86_64"]

    def test_each_cpu_channel_is_withdrawn_independently(self):
        self.release()
        self.publish_aarch64()
        plan = self.prepare()
        states = {(entry["family"], entry["architecture"]): entry["state"] for entry in plan["channels"]}
        self.assertEqual(states, {("deb", None): "absent", ("rpm", None): "absent",
                                  ("pacman", "x86_64"): "replace", ("pacman", "aarch64"): "unaffected"})
        self.apply(plan)
        state = self.store.read("channels/pacman/preview/x86_64/current.json")
        healthy = self.generations[("pacman-{version}-x86_64", "0.0.1")][0]
        self.assertEqual((state["operation"], state["generation"]), ("withdraw", identity(healthy)))
        self.assertEqual(self.store.read("channels/pacman/preview/aarch64/current.json")["version"], "0.0.1")

    def publish_aarch64(self):
        receipt, directory = pacman.generation("pacman-0.0.1-aarch64", self.work)
        with self.store.transport():
            withdrawal.transport("pacman")({**receipt, "rehearsal": False}, directory, self.catalog, PREFIX,
                                           ORIGIN, None, False, authorize=lambda request: True)


class ProtectedEntry(unittest.TestCase):
    def run_main(self, *argv, environment=None):
        output = io.StringIO()
        with patch.dict(os.environ, environment or {}, clear=False), contextlib.redirect_stdout(output), \
                contextlib.redirect_stderr(output):
            try:
                code = withdrawal.main(list(argv))
            except SystemExit as exit:
                code = exit.code
        return code, output.getvalue()

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.distribution = self.work / "distribution.json"
        self.distribution.write_text(json.dumps({"channels": [{"channel": "stable", "bad": "1.0.3", "last_good": "1.0.2"}]}))

    def test_an_unconfigured_origin_records_that_no_channel_exists(self):
        plan = self.work / "plan.json"
        with patch.dict(os.environ, {"LINUX_REPOSITORY_ORIGIN": ""}):
            code, output = self.run_main("prepare", "--distribution", str(self.distribution),
                                         "--work", str(self.work / "w"), "--output", str(plan))
            self.assertEqual(code, 0, output)
            self.assertEqual(json.loads(plan.read_text()), {"format_version": 1, "configured": False})
            self.assertEqual(self.run_main("apply", "--plan", str(plan))[0], 0)

    def test_a_configured_plan_never_applies_without_its_origin(self):
        plan = self.work / "plan.json"
        plan.write_text(json.dumps({"format_version": 1, "channels": []}))
        with patch.dict(os.environ, {"LINUX_REPOSITORY_ORIGIN": ""}):
            code, output = self.run_main("apply", "--plan", str(plan))
        self.assertEqual(code, 1)
        self.assertIn("prepared for a configured repository origin", output)

    def test_only_the_protected_production_origin_and_policy_apply(self):
        code, output = self.run_main("prepare", "--distribution", str(self.distribution), "--work",
                                     str(self.work / "w"), "--output", str(self.work / "plan.json"),
                                     environment={"LINUX_REPOSITORY_ORIGIN": ORIGIN})
        self.assertEqual(code, 1)
        self.assertIn("protected production repository origin", output)
        plan = self.work / "rehearsal-plan.json"
        plan.write_text(json.dumps({"format_version": 1, "origin": withdrawal.PRODUCTION_ORIGIN,
                                    "storage_prefix": "release-system-tests/halt", "catalog": "/elsewhere",
                                    "rehearsal": True, "channels": []}))
        code, output = self.run_main("apply", "--plan", str(plan),
                                     environment={"LINUX_REPOSITORY_ORIGIN": withdrawal.PRODUCTION_ORIGIN})
        self.assertEqual(code, 1)
        self.assertIn("differs from protected production policy", output)


if __name__ == "__main__":
    unittest.main()
