"""A halt restores each affected Linux channel to the exact retained generation it replaced."""

import hashlib
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

from scripts.linux_release import (native_repositories, pacman_r2, pacman_repository, release_repositories,
                                   release_signing, repository_r2, repository_withdrawal)
from scripts.linux_release.openpgp import ephemeral
from scripts.linux_release.repository_objects import encoded
from scripts.linux_release.repository_withdrawal import apply, download, prepare
from scripts.linux_release.signing import CATALOG
from scripts.verification_tests.distribution.repository_fixture_support import native_tools

ROOT = Path(__file__).resolve().parents[3]


class QuietHandler(SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass


SOURCE = "a" * 40


def receipt(version: str, family="deb", architecture=None, channel="stable", packages=True) -> dict:
    value = {"family": family, "channel": channel, "version": version, "source_commit": SOURCE,
             "packages": [{"file": "package", "sha256": "0" * 64}] if packages else [], "objects": [
                 {"path": "pool/a/package", "sha256": hashlib.sha256(b"package").hexdigest(), "size": 7}]}
    return {**value, "architecture": architecture} if architecture else value


def identity(value: dict) -> str:
    return hashlib.sha256(encoded(value)).hexdigest()


class LinuxWithdrawalPlanTests(unittest.TestCase):
    """Every channel publication writes withdraws on halt, with or without an earlier Linux offer."""

    def setUp(self):
        self.store = {}

    def read(self, key, directory):
        return self.store.get(key)

    def offer(self, family, channel, architecture, previous=None, version="1.0.2"):
        """Model what publication writes: the active offer and its retained empty withdrawal generation."""
        empty = receipt(version, family, architecture, channel, packages=False)
        self.store[release_repositories.withdrawal_key(empty)] = empty
        active = {"version": version, "generation": identity(receipt(version, family, architecture, channel)),
                  "previous": previous, "source_commit": SOURCE, "packages": [{"file": "package"}]}
        self.store[repository_withdrawal.state_key(family, channel, architecture)] = active
        return empty, active

    def earlier(self, family, channel, architecture, version="1.0.1"):
        value = receipt(version, family, architecture, channel)
        self.store[f"linux/generations/{identity(value)}/generation.json"] = value
        return value

    def test_first_linux_release_withdraws_to_its_empty_generation_on_every_channel(self):
        empties = {row: self.offer(*row)[0] for row in repository_withdrawal.CHANNELS}
        for last_good in [None, "1.0.1"]:
            with self.subTest(last_good=last_good):
                rows = prepare("1.0.2", last_good, read=self.read)
                self.assertEqual([(r["family"], r["channel"], r["architecture"]) for r in rows], repository_withdrawal.CHANNELS)
                for row in rows:
                    empty = empties[(row["family"], row["channel"], row["architecture"])]
                    self.assertEqual((row["receipt"], row["replacement"]), (empty, identity(empty)))
                    self.assertEqual(row["receipt"]["packages"], [])

    def test_replaced_release_restores_the_named_predecessor_on_every_channel(self):
        earlier = {}
        for row in repository_withdrawal.CHANNELS:
            earlier[row] = self.earlier(*row)
            self.offer(*row, previous=identity(earlier[row]))
        rows = prepare("1.0.2", "1.0.1", read=self.read)
        self.assertEqual(len(rows), len(repository_withdrawal.CHANNELS))
        for row in rows:
            self.assertEqual(row["receipt"], earlier[(row["family"], row["channel"], row["architecture"])])
        with self.assertRaisesRegex(ValueError, "predecessor is not the retained 1.0.0 generation"):
            prepare("1.0.2", "1.0.0", read=self.read)
        # Naming no replacement stops discovery with the empty generation instead.
        self.assertTrue(all(row["receipt"]["packages"] == [] for row in prepare("1.0.2", None, read=self.read)))

    def test_withdrawn_unoffered_and_unretained_channels(self):
        empty, active = self.offer("deb", "stable", None)
        self.store[repository_withdrawal.state_key("deb", "stable", None)] = {**active, "packages": []}
        self.assertEqual(prepare("1.0.2", None, read=self.read), [])
        self.assertEqual(prepare("1.0.3", None, read=self.read), [])
        self.store[repository_withdrawal.state_key("deb", "stable", None)] = active
        del self.store[release_repositories.withdrawal_key(empty)]
        with self.assertRaisesRegex(ValueError, "no retained withdrawal generation"):
            prepare("1.0.2", None, read=self.read)
        self.store[release_repositories.withdrawal_key(empty)] = {**empty, "source_commit": "b" * 40}
        with self.assertRaisesRegex(ValueError, "no retained withdrawal generation"):
            prepare("1.0.2", None, read=self.read)
        self.store[repository_withdrawal.state_key("deb", "stable", None)] = {**active, "previous": "f" * 64}
        with self.assertRaisesRegex(ValueError, "predecessor is not retained"):
            prepare("1.0.2", "1.0.1", read=self.read)


class LinuxWithdrawalTests(unittest.TestCase):
    def setUp(self):
        self.good = receipt("1.0.1")
        self.pacman = receipt("1.0.1", "pacman", "x86_64")
        self.store = {
            "linux/channels/deb/stable/current.json": {"version": "1.0.2", "generation": "b" * 64,
                                                      "previous": identity(self.good)},
            "linux/channels/pacman/stable/x86_64/current.json": {"version": "1.0.2", "generation": "c" * 64,
                                                                "previous": identity(self.pacman)},
            "linux/channels/rpm/stable/current.json": {"version": "1.0.1", "generation": "d" * 64, "previous": None},
            f"linux/generations/{identity(self.good)}/generation.json": self.good,
            f"linux/generations/{identity(self.pacman)}/generation.json": self.pacman,
        }

    def read(self, key, directory):
        return self.store.get(key)

    def test_apply_withdraws_with_the_prepared_plan_and_reports_every_failure(self):
        rows = prepare("1.0.2", "1.0.1", read=self.read)
        calls = []

        def publisher(manifest, directory, catalog, prefix, base_url, expected, rehearsal, operation, phase, *, authorize):
            request = {"manifest": manifest, "catalog": catalog, "storage_prefix": prefix, "expected": expected,
                       "operation": operation}
            self.assertTrue(authorize(request))
            with self.assertRaisesRegex(ValueError, "prepared plan"):
                authorize({**request, "expected": "e" * 64})
            calls.append((manifest["family"], expected, rehearsal, operation, phase))
            return {"generation": identity(manifest)}
        fetched = []
        results = apply(rows, "https://downloads.example.invalid", fetch=lambda base, row, directory: fetched.append(row),
                        publishers={"deb": publisher, "rpm": publisher, "pacman": publisher})
        self.assertEqual(len(results), 2)
        self.assertEqual(calls, [("deb", "b" * 64, False, "withdraw", "activate"),
                                 ("pacman", "c" * 64, False, "withdraw", "activate")])

        def broken(*args, **kwargs):
            raise ValueError("activation interrupted")
        with self.assertRaisesRegex(ValueError, "deb stable: activation interrupted; pacman stable"):
            apply(rows, "https://downloads.example.invalid", fetch=lambda *args: None,
                  publishers={"deb": broken, "pacman": broken})

    def test_download_reconstructs_the_retained_generation_from_public_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            row = {"replacement": identity(self.good), "receipt": self.good}
            origin = work / "origin" / "linux/generations" / row["replacement"] / "pool/a"
            origin.mkdir(parents=True)
            (origin / "package").write_bytes(b"package")
            server = ThreadingHTTPServer(("127.0.0.1", 0), partial(QuietHandler, directory=str(work / "origin")))
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                base = f"http://127.0.0.1:{server.server_port}"
                download(base, row, work / "retained")
                self.assertEqual((work / "retained/pool/a/package").read_bytes(), b"package")
                self.assertEqual(json.loads((work / "retained/generation.json").read_text()), self.good)
                (origin / "package").write_bytes(b"changed")
                with self.assertRaisesRegex(ValueError, "differs from its inventory"):
                    download(base, row, work / "changed")
            finally:
                server.shutdown()
                server.server_close()

    def test_download_refuses_truncated_oversized_and_escaping_objects(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            row = {"replacement": identity(self.good), "receipt": self.good}
            origin = work / "origin" / "linux/generations" / row["replacement"] / "pool/a"
            origin.mkdir(parents=True)
            server = ThreadingHTTPServer(("127.0.0.1", 0), partial(QuietHandler, directory=str(work / "origin")))
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                base = f"http://127.0.0.1:{server.server_port}"
                for name, content in [("truncated", b"pack"), ("oversized", b"package and more")]:
                    (origin / "package").write_bytes(content)
                    with self.subTest(name=name), self.assertRaisesRegex(ValueError, "differs from its inventory"):
                        download(base, row, work / name)
                    self.assertFalse((work / name / "pool/a/package").exists())
                (origin / "package").write_bytes(b"package")
                for escaping in ["../package", "/pool/a/package", "pool/../a/package", "pool//a/package",
                                 "./package", "generation.json", ""]:
                    receipt = {**self.good, "objects": [{**self.good["objects"][0], "path": escaping}]}
                    with self.subTest(path=escaping), self.assertRaisesRegex(ValueError, "unsupported object path"):
                        download(base, {"replacement": identity(receipt), "receipt": receipt}, work / "escaping")
                    self.assertFalse((work / "escaping").exists())
                download(base, row, work / "retained")
                self.assertEqual((work / "retained/pool/a/package").read_bytes(), b"package")
            finally:
                server.shutdown()
                server.server_close()


class LinuxReleaseCommandTests(unittest.TestCase):
    """The workflow entry points run the same functions with the protected key and qualification."""

    def run_main(self, module, arguments):
        with patch.object(sys, "argv", ["linux-release", *arguments]):
            try:
                module.main()
            except SystemExit as exit:
                return exit.code
        return 0

    def test_signing_command_consumes_the_protected_key(self):
        with tempfile.TemporaryDirectory() as temporary, \
                patch.dict(os.environ, {"LINUX_PACKAGE_SIGNING_KEY": "private"}), \
                patch.object(release_signing, "sign_staged", return_value={"signed": True}) as sign:
            arguments = ["--directory", temporary, "--platform", "linux-x86_64", "--architecture", "x86_64",
                         "--version", "1.2.3", "--source-commit", "a" * 40]
            self.assertEqual(self.run_main(release_signing, arguments), 0)
            self.assertEqual(sign.call_args.kwargs["private"].name, "private.asc")
            sign.side_effect = ValueError("refused")
            with patch.dict(os.environ, {"LINUX_PACKAGE_SIGNING_KEY": "private"}):
                self.assertEqual(self.run_main(release_signing, arguments), 1)

    def test_repository_commands_select_families_and_qualification(self):
        common = ["--artifacts", "public", "--version", "1.2.3", "--source-commit", "a" * 40, "--channel", "stable"]
        with tempfile.TemporaryDirectory() as temporary, \
                patch.dict(os.environ, {"LINUX_PACKAGE_SIGNING_KEY": "private"}), \
                patch.object(release_repositories, "public_linux", return_value=[{"goos": "linux"}]), \
                patch.object(release_repositories, "assemble") as assemble, \
                patch.object(release_repositories, "publish", return_value=[]) as publish:
            self.assertEqual(self.run_main(release_repositories, ["assemble", *common, "--family", "pacman",
                                                                  "--output", temporary + "/out"]), 0)
            self.assertEqual(assemble.call_args.args[0], {"pacman"})
            self.assertEqual(self.run_main(release_repositories, ["publish", *common, "--generations", "a",
                                                                  "--generations", "b", "--base-url",
                                                                  "https://downloads.example.invalid/"]), 0)
            self.assertEqual([path.name for path in publish.call_args.args[0]], ["a", "b"])
            self.assertEqual(publish.call_args.args[5], "https://downloads.example.invalid")
            self.assertIs(publish.call_args.args[6], release_repositories.release_qualification.require)
            publish.side_effect = ValueError("refused")
            self.assertEqual(self.run_main(release_repositories, ["publish", *common, "--generations", "a",
                                                                  "--base-url", "https://x.invalid"]), 1)
            release_repositories.public_linux.return_value = []
            self.assertEqual(self.run_main(release_repositories, ["assemble", *common, "--family", "native",
                                                                  "--output", temporary + "/empty"]), 0)
            self.assertEqual(json.loads(Path(temporary, "empty/generations.json").read_text()), [])

    def test_withdrawal_commands_prepare_and_apply_the_plan_file(self):
        with tempfile.TemporaryDirectory() as temporary, \
                patch.object(repository_withdrawal, "prepare", return_value=[{"planned": True}]) as planned, \
                patch.object(repository_withdrawal, "apply", return_value=[]) as applied:
            plan = Path(temporary) / "plan.json"
            base = ["--file", str(plan), "--base-url", "https://downloads.example.invalid/"]
            self.assertEqual(self.run_main(repository_withdrawal, ["prepare", *base, "--bad", "1.0.2", "--last-good", ""]), 0)
            self.assertEqual(planned.call_args.args, ("1.0.2", None))
            self.assertEqual(self.run_main(repository_withdrawal, ["apply", *base]), 0)
            self.assertEqual(applied.call_args.args, ([{"planned": True}], "https://downloads.example.invalid"))
            self.assertEqual(self.run_main(repository_withdrawal, ["prepare", *base]), 2)
            applied.side_effect = ValueError("retry")
            self.assertEqual(self.run_main(repository_withdrawal, ["apply", *base]), 1)

    def test_admission_reads_the_shared_exact_source_qualification(self):
        from ci_policy import verification_verdict
        from scripts.linux_release import release_qualification
        with patch.dict(os.environ, {"GITHUB_REPOSITORY": "owner/repository"}), \
                patch.object(verification_verdict, "require_qualification", return_value={"id": 1}) as required:
            self.assertEqual(release_qualification.require("a" * 40), {"id": 1})
        required.assert_called_once_with("owner/repository", "a" * 40)

    def test_native_versions_come_from_each_package_format(self):
        with patch.object(subprocess, "check_output", return_value="1.2.3-1\n") as output:
            self.assertEqual(release_repositories.native_version("deb", Path("p.deb")), "1.2.3-1")
            self.assertEqual(output.call_args.args[0][:2], ["dpkg-deb", "-f"])
            release_repositories.native_version("rpm", Path("p.rpm"))
            self.assertEqual(output.call_args.args[0][:2], ["rpm", "-qp"])
        with patch.object(release_repositories.pacman_repository, "package_info", return_value={"pkgver": "1.2.3-1"}):
            self.assertEqual(release_repositories.native_version("pacman", Path("p.pkg.tar.zst")), "1.2.3-1")
        with patch.object(subprocess, "check_output", return_value="painted-wolf-code 1.2.3-1 x86_64\n"), \
                patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 0, stdout=b"cpio")):
            self.assertEqual(release_signing.rpm_payload(Path("p.rpm")),
                             ("painted-wolf-code 1.2.3-1 x86_64", hashlib.sha256(b"cpio").hexdigest()))
        self.assertEqual(CATALOG.name, "keys.json")


BASE = "https://downloads.example.invalid"


class FirstReleaseWithdrawalTests(unittest.TestCase):
    """Generations are real signed assembly output; only the object store is in memory."""

    @classmethod
    def setUpClass(cls):
        if shutil.which("gpg") is None:
            if sys.platform == "linux":
                raise AssertionError("Linux distribution tests require gpg")
            raise unittest.SkipTest("signing requires gpg")
        cls.temporary = tempfile.TemporaryDirectory()
        work = Path(cls.temporary.name)
        cls.catalog = ephemeral(work / "key")
        cls.private = cls.catalog.parent / "rehearsal-secret.asc"
        # The empty generation is production trust: the release lane's key, not rehearsal trust.
        cls.catalog.write_text(json.dumps({**json.loads(cls.catalog.read_text()), "rehearsal": False}))

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.objects = {}
        for target in [release_repositories, repository_withdrawal]:
            patcher = patch.object(target, "CATALOG", self.catalog)
            patcher.start()
            self.addCleanup(patcher.stop)
        for module in [repository_r2, pacman_r2]:
            for name, replacement in [("current", self.current), ("put", self.put), ("public_bytes", self.public)]:
                patcher = patch.object(module, name, side_effect=replacement)
                patcher.start()
                self.addCleanup(patcher.stop)

    def current(self, key, directory):
        return json.loads(self.objects[key]) if key in self.objects else None

    def put(self, key, source, mutable=False):
        if not mutable and key in self.objects and self.objects[key] != source.read_bytes():
            raise ValueError("immutable collision")
        self.objects[key] = source.read_bytes()

    def public(self, url, source, mutable=False):
        if self.objects.get(url.removeprefix(BASE + "/")) != source.read_bytes():
            raise ValueError("public bytes differ")

    def fetch(self, base_url, row, directory):
        for item in row["receipt"]["objects"]:
            target = directory / item["path"]
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(self.objects[f"linux/generations/{row['replacement']}/{item['path']}"])
        (directory / "generation.json").write_bytes(encoded(row["receipt"]))

    def empty(self, family: str) -> tuple[dict, Path]:
        destination = self.work / (family + "-withdrawal")
        manifest = {"format_version": 1, "source_commit": SOURCE, "version": "1.2.3", "packages": []}
        if family == "pacman":
            receipt = pacman_repository.assemble({**manifest, "architecture": "x86_64"}, self.work, destination,
                                                 "stable", self.catalog, self.private)
        else:
            receipt = native_repositories.assemble(family, manifest, self.work, destination, "stable", self.catalog,
                                                   self.private, architectures=["amd64", "arm64"])
        return receipt, destination

    def withdraw_first_release(self, family: str):
        empty, directory = self.empty(family)
        self.assertEqual(empty["packages"], [])
        offer = {**empty, "packages": [{"file": "painted-wolf-code.pkg", "sha256": "0" * 64}],
                 "inventory_sha256": "0" * 64}
        qualified = []
        release_repositories.retain_withdrawal(empty, offer, directory, BASE, qualified.append,
                                               upload=self.put, verify=self.public)
        self.assertEqual(qualified, [SOURCE])
        # The halted first release is the active offer, with no earlier Linux generation.
        halted = {"deb": "c", "rpm": "d", "pacman": "e"}[family] * 64
        active = {"format_version": 1, "operation": "publish", "generation": halted, "previous": None,
                  **{key: offer[key] for key in ["family", "channel", "version", "source_commit", "inventory_sha256",
                                                 "packages", "signing_generation", "key_fingerprint", "rehearsal"]},
                  **({"architecture": "x86_64"} if family == "pacman" else {})}
        state = release_repositories.channel_key(offer)
        self.objects[state] = encoded(active)
        rows = repository_withdrawal.prepare("1.2.3", None, read=self.current)
        self.assertEqual([row["replacement"] for row in rows], [identity(empty)])
        for _ in range(2):  # an interrupted halt retries the same plan
            repository_withdrawal.apply(rows, BASE, fetch=self.fetch)
            record = json.loads(self.objects[state])
            self.assertEqual((record["operation"], record["generation"], record["previous"], record["packages"]),
                             ("withdraw", identity(empty), halted, []))
        self.assertIn("linux/withdrawn/" + halted + ".json", self.objects)
        marker = "linux/withdrawn-versions/" + family + "/stable/" + ("x86_64" if family == "pacman" else "all")
        self.assertIn(marker + "/1.2.3.json", self.objects)
        self.assertEqual(repository_withdrawal.prepare("1.2.3", None, read=self.current), [])

    def test_pacman_first_release_withdraws_to_signed_empty_databases(self):
        self.withdraw_first_release("pacman")
        self.assertEqual(json.loads(self.objects["linux/channels/pacman/stable/x86_64/current.json"])["version"], "1.2.3")

    def test_apt_and_dnf_first_release_withdraws_to_signed_empty_indexes(self):
        native_tools("apt-ftparchive", "createrepo_c", "gpg", "gpgv")
        for family in ["deb", "rpm"]:
            with self.subTest(family=family):
                self.withdraw_first_release(family)

    def test_withdrawal_generation_must_match_its_offer(self):
        empty, directory = self.empty("pacman")
        for change in [{"version": "1.2.4"}, {"architecture": "aarch64"}, {"key_fingerprint": "0" * 40}]:
            with self.subTest(change=change), self.assertRaisesRegex(ValueError, "differs from the offer"):
                release_repositories.retain_withdrawal(empty, {**empty, **change}, directory, BASE, lambda _s: None,
                                                       upload=self.put, verify=self.public)
        with self.assertRaisesRegex(ValueError, "differs from the offer"):
            release_repositories.retain_withdrawal({**empty, "packages": [{"file": "x"}]}, empty, directory, BASE,
                                                   lambda _s: None, upload=self.put, verify=self.public)
        self.assertEqual(self.objects, {})


if __name__ == "__main__":
    unittest.main()
