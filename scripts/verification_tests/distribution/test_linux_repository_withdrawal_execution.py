"""A halt restores each affected Linux channel to the exact retained generation it replaced."""

import hashlib
import copy
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json
import io
from contextlib import redirect_stderr
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

from scripts.linux_release import release_repositories, release_signing, repository_withdrawal
from scripts.linux_release.repository_objects import encoded
from scripts.linux_release.repository_withdrawal import apply, download, prepare
from scripts.linux_release.signing import CATALOG
from scripts.verification_tests.distribution.repository_fixture_support import halt_distribution, generation_prefixes

ROOT = Path(__file__).resolve().parents[3]


class QuietHandler(SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass


SOURCE = "a" * 40


def receipt(version: str, family="deb", architecture=None, channel="stable", packages=True) -> dict:
    value = {"format_version": 1, "rehearsal": False, "family": family, "channel": channel, "version": version, "source_commit": SOURCE,
             "packages": [{"file": "package", "sha256": "0" * 64}] if packages else [], "objects": [
                 {"path": "pool/a/package", "sha256": hashlib.sha256(b"package").hexdigest(), "size": 7}]}
    return {**value, "architecture": architecture} if architecture else value


def identity(value: dict) -> str:
    return hashlib.sha256(encoded(value)).hexdigest()


class LinuxWithdrawalPlanTests(unittest.TestCase):
    """Every channel publication writes withdraws on halt, with or without an earlier Linux offer."""

    def setUp(self):
        self.store = {}
        self.selection_only_admission()

    def selection_only_admission(self):
        for owner in (repository_withdrawal.repository_validation, repository_withdrawal.pacman_validation):
            patched = patch.object(owner, "validate_manifest", return_value=[])
            patched.start()
            self.addCleanup(patched.stop)

    def read(self, key, directory):
        return self.store.get(key)

    def offer(self, family, channel, architecture, previous=None, version="2.0.2"):
        """Model what publication writes: the active offer and its retained empty withdrawal generation."""
        empty = receipt(version, family, architecture, channel, packages=False)
        self.store[release_repositories.withdrawal_key(empty)] = empty
        active = {"version": version, "generation": identity(receipt(version, family, architecture, channel)),
                  "previous": previous, "source_commit": SOURCE, "packages": [{"file": "package"}]}
        self.store[repository_withdrawal.state_key(family, channel, architecture)] = active
        return empty, active

    def earlier(self, family, channel, architecture, version="2.0.1"):
        value = receipt(version, family, architecture, channel)
        self.store[f"linux/generations/{identity(value)}/generation.json"] = value
        return value

    def test_first_linux_release_withdraws_to_its_empty_generation_on_every_channel(self):
        empties = {row: self.offer(*row)[0] for row in repository_withdrawal.CHANNELS}
        for last_good in [None]:
            with self.subTest(last_good=last_good):
                rows = prepare(halt_distribution("2.0.2", last_good), read=self.read, enumerate_prefixes=lambda prefix: generation_prefixes(self.store))
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
        rows = prepare(halt_distribution("2.0.2", "2.0.1"), read=self.read, enumerate_prefixes=lambda prefix: generation_prefixes(self.store))
        self.assertEqual(len(rows), len(repository_withdrawal.CHANNELS))
        for row in rows:
            self.assertEqual(row["receipt"], earlier[(row["family"], row["channel"], row["architecture"])])
        with self.assertRaises(repository_withdrawal.MissingRetainedGeneration) as failure:
            prepare(halt_distribution("2.0.2", "2.0.0"), read=self.read, enumerate_prefixes=lambda prefix: generation_prefixes(self.store))
        self.assertEqual(failure.exception.reason["selected_version"], "2.0.0")
        self.assertEqual(failure.exception.reason["retained_versions"], ["2.0.1"])
        # Naming no replacement stops discovery with the empty generation instead.
        self.assertTrue(all(row["receipt"]["packages"] == [] for row in prepare(halt_distribution("2.0.2", None), read=self.read, enumerate_prefixes=lambda prefix: generation_prefixes(self.store))))

    def test_withdrawn_unoffered_and_unretained_channels(self):
        empty, active = self.offer("deb", "stable", None)
        self.store[repository_withdrawal.state_key("deb", "stable", None)] = {**active, "packages": []}
        self.assertTrue(all(row["action"] == "keep" for row in prepare(halt_distribution("2.0.2", None), read=self.read, enumerate_prefixes=lambda prefix: generation_prefixes(self.store))))
        self.assertTrue(all(row["action"] == "keep" for row in prepare(halt_distribution("2.0.3", None), read=self.read, enumerate_prefixes=lambda prefix: generation_prefixes(self.store))))
        self.store[repository_withdrawal.state_key("deb", "stable", None)] = active
        del self.store[release_repositories.withdrawal_key(empty)]
        with self.assertRaisesRegex(ValueError, "no retained withdrawal generation"):
            prepare(halt_distribution("2.0.2", None), read=self.read, enumerate_prefixes=lambda prefix: generation_prefixes(self.store))
        self.store[release_repositories.withdrawal_key(empty)] = {**empty, "source_commit": "b" * 40}
        with self.assertRaisesRegex(ValueError, "no retained withdrawal generation"):
            prepare(halt_distribution("2.0.2", None), read=self.read, enumerate_prefixes=lambda prefix: generation_prefixes(self.store))
        self.store[repository_withdrawal.state_key("deb", "stable", None)] = {**active, "previous": "f" * 64}
        with self.assertRaises(repository_withdrawal.MissingRetainedGeneration):
            prepare(halt_distribution("2.0.2", "2.0.1"), read=self.read, enumerate_prefixes=lambda prefix: generation_prefixes(self.store))

    def test_default_and_reviewed_per_channel_replacements_ignore_immediate_predecessors(self):
        for family, channel, architecture in repository_withdrawal.CHANNELS:
            self.earlier(family, channel, architecture, version="2.0.1")
            previous = self.earlier(family, channel, architecture, version="2.0.2-rc.1" if channel == "preview" else "2.0.1")
            self.offer(family, channel, architecture, previous=identity(previous))
        calls = []
        def enumerate_once(prefix):
            calls.append(prefix)
            return generation_prefixes(self.store)
        selected = halt_distribution("2.0.2", "2.0.1")
        rows = prepare(selected, read=self.read, enumerate_prefixes=enumerate_once)
        self.assertEqual({row["receipt"]["version"] for row in rows}, {"2.0.1"})
        self.assertEqual(calls, ["linux/generations/"])
        selected["channels"][1]["last_good"] = "2.0.2-rc.1"
        rows = prepare(selected, read=self.read, enumerate_prefixes=enumerate_once)
        self.assertEqual({(row["channel"], row["receipt"]["version"]) for row in rows},
                         {("stable", "2.0.1"), ("preview", "2.0.2-rc.1")})
        snapshot = copy.deepcopy(self.store)
        rows = prepare(halt_distribution("2.0.2", "2.0.1", channels=("stable",)), read=self.read,
                       enumerate_prefixes=enumerate_once)
        self.assertEqual({row["channel"] for row in rows}, {"stable"})
        self.assertEqual(self.store, snapshot)

    def test_missing_selected_preview_version_refuses_the_whole_cli_dry_run(self):
        for family, channel, architecture in repository_withdrawal.CHANNELS:
            self.earlier(family, channel, architecture, version="2.0.1" if channel == "stable" else "2.0.2-rc.1")
            self.offer(family, channel, architecture)
        selected = halt_distribution("2.0.2", "2.0.1")
        snapshot = copy.deepcopy(self.store)
        with self.assertRaises(repository_withdrawal.MissingRetainedGeneration) as failure:
            prepare(selected, read=self.read, enumerate_prefixes=lambda prefix: generation_prefixes(self.store))
        self.assertEqual(failure.exception.reason["channel"], "preview")
        self.assertEqual(failure.exception.reason["retained_versions"], ["2.0.2-rc.1"])
        with tempfile.TemporaryDirectory() as temporary:
            source, output = Path(temporary) / "shared.json", Path(temporary) / "native.json"
            source.write_text(json.dumps(selected))
            arguments = ["native-halt", "prepare", "--distribution-plan", str(source), "--file", str(output), "--base-url", "https://downloads.paintedwolf.dev"]
            def actual(plan, **configuration):
                return prepare(plan, read=self.read, enumerate_prefixes=lambda prefix: generation_prefixes(self.store))
            with patch.object(sys, "argv", arguments), patch.object(repository_withdrawal, "prepare", side_effect=actual), \
                 redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as failed:
                repository_withdrawal.main()
            self.assertEqual(failed.exception.code, 1)
            self.assertFalse(output.exists())
        self.assertEqual(self.store, snapshot)

    def test_retained_generation_tamper_and_unknown_plan_refuse_before_mutation(self):
        earlier = self.earlier("deb", "stable", None)
        self.offer("deb", "stable", None)
        self.store[f"linux/generations/{identity(earlier)}/generation.json"] = {**earlier, "version": "2.0.0"}
        with self.assertRaisesRegex(ValueError, "identity"):
            prepare(halt_distribution("2.0.2", "2.0.1"), read=self.read,
                    enumerate_prefixes=lambda prefix: generation_prefixes(self.store))
        with patch.object(repository_withdrawal, "current") as read, self.assertRaises(ValueError):
            prepare({"bad_versions": ["2.0.2"], "channels": [{"channel": "stable", "bad": "2.0.2", "last_good": "2.0.3"}]}, read=read)
        read.assert_not_called()


class LinuxWithdrawalTests(unittest.TestCase):
    def setUp(self):
        for owner in (repository_withdrawal.repository_validation, repository_withdrawal.pacman_validation):
            patched = patch.object(owner, "validate_manifest", return_value=[])
            patched.start()
            self.addCleanup(patched.stop)
        self.good = receipt("2.0.1")
        self.pacman = receipt("2.0.1", "pacman", "x86_64")
        self.store = {
            "linux/channels/deb/stable/current.json": {"version": "2.0.2", "generation": "b" * 64,
                                                      "previous": identity(self.good)},
            "linux/channels/pacman/stable/x86_64/current.json": {"version": "2.0.2", "generation": "c" * 64,
                                                                "previous": identity(self.pacman)},
            "linux/channels/rpm/stable/current.json": {"version": "2.0.1", "generation": "d" * 64, "previous": None},
            f"linux/generations/{identity(self.good)}/generation.json": self.good,
            f"linux/generations/{identity(self.pacman)}/generation.json": self.pacman,
        }

    def read(self, key, directory):
        return self.store.get(key)

    def test_apply_withdraws_with_the_prepared_plan_and_reports_every_failure(self):
        rows = prepare(halt_distribution("2.0.2", "2.0.1"), read=self.read, enumerate_prefixes=lambda prefix: generation_prefixes(self.store))
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
        # An apt restore needs its re-signed generation; nothing applies without it.
        self.assertEqual([row["family"] for row in rows if row.get("resign")], ["deb"])
        with self.assertRaisesRegex(ValueError, "re-signed generation"):
            apply(rows, "https://downloads.paintedwolf.dev", fetch=lambda base, row, directory: fetched.append(row),
                  publishers={"deb": publisher, "rpm": publisher, "pacman": publisher})
        self.assertEqual((calls, fetched), ([], []))
        rows = [row for row in rows if not row.get("resign")]
        results = apply(rows, "https://downloads.paintedwolf.dev", fetch=lambda base, row, directory: fetched.append(row),
                        publishers={"deb": publisher, "rpm": publisher, "pacman": publisher})
        self.assertEqual(len(results), 1)
        self.assertEqual(calls, [("pacman", "c" * 64, False, "withdraw", "activate")])

        def broken(*args, **kwargs):
            raise ValueError("activation interrupted")
        with self.assertRaisesRegex(ValueError, "pacman stable: activation interrupted"):
            apply(rows, "https://downloads.paintedwolf.dev", fetch=lambda *args: None,
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
                # The halted generation's public Release dates an apt restore's fresh one.
                self.assertEqual(repository_withdrawal.public_object(base, row, row["replacement"], "pool/a/package"), b"package")
                with self.assertRaisesRegex(ValueError, "exceeds its bound"):
                    repository_withdrawal.public_object(base, row, row["replacement"], "pool/a/package", limit=3)
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
            from scripts.release_distribution_contract.channels import select
            from update_keys import load_registry
            plan = Path(temporary) / 'channel-plan.json'
            plan.write_text(json.dumps(select('stable', '1.2.3', load_registry()['signing_generation'], 'a' * 40, read=lambda key: None)))
            common += ['--plan', str(plan)]
            self.assertEqual(self.run_main(release_repositories, ["assemble", *common, "--family", "pacman",
                                                                  "--output", temporary + "/out"]), 0)
            self.assertEqual(assemble.call_args.args[0], {"pacman"})
            self.assertEqual(self.run_main(release_repositories, ["publish", *common, "--generations", "a",
                                                                  "--generations", "b", "--base-url",
                                                                  "https://downloads.example.invalid/", "--output", temporary + "/publication.json"]), 0)
            self.assertEqual([path.name for path in publish.call_args.args[0]], ["a", "b"])
            self.assertEqual(publish.call_args.args[5], "https://downloads.example.invalid")
            self.assertEqual(publish.call_args.args[4], ['stable', 'preview'])
            with patch('release_distribution.read_storage', return_value=None), \
                 patch.object(release_repositories.release_qualification, 'require') as qualify:
                publish.call_args.args[6]('a' * 40)
            qualify.assert_called_once_with('a' * 40)
            publish.side_effect = ValueError("refused")
            self.assertEqual(self.run_main(release_repositories, ["publish", *common, "--generations", "a",
                                                                  "--base-url", "https://x.invalid", "--output", temporary + "/failed.json"]), 1)
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
            shared = Path(temporary) / "shared.json"
            selected = halt_distribution("2.0.2", None)
            shared.write_text(json.dumps(selected))
            self.assertEqual(self.run_main(repository_withdrawal, ["prepare", *base, "--distribution-plan", str(shared)]), 0)
            self.assertEqual(planned.call_args.args, (selected,))
            self.assertEqual(planned.call_args.kwargs, {"base_url": "https://downloads.example.invalid"})
            self.assertEqual(self.run_main(repository_withdrawal, ["apply", *base]), 0)
            self.assertEqual(applied.call_args.args, ([{"planned": True}], "https://downloads.example.invalid"))
            signed = Path(temporary) / "signed"
            with patch.object(repository_withdrawal, "resign", return_value=[{"signed": True}]) as resigned, \
                    patch.dict(os.environ, {"LINUX_PACKAGE_SIGNING_KEY": "private material"}):
                self.assertEqual(self.run_main(repository_withdrawal, ["resign", *base, "--signed", str(signed)]), 0)
                self.assertEqual(self.run_main(repository_withdrawal, ["resign", *base]), 2)
            self.assertEqual(resigned.call_args.args[:3], ([{"planned": True}], "https://downloads.example.invalid", signed))
            self.assertEqual(json.loads((signed / "linux-withdrawal.json").read_text()), [{"signed": True}])
            self.assertEqual(self.run_main(repository_withdrawal, ["apply", *base, "--signed", str(signed)]), 0)
            self.assertEqual(applied.call_args.kwargs, {"signed": signed})
            self.assertEqual(self.run_main(repository_withdrawal, ["prepare", *base]), 2)
            applied.side_effect = ValueError("retry")
            self.assertEqual(self.run_main(repository_withdrawal, ["apply", *base]), 1)

    def test_native_cli_rerun_refuses_negative_ledger_when_controls_keep_and_no_owner_tombstone(self):
        import release_distribution
        with tempfile.TemporaryDirectory() as temporary:
            from scripts.release_distribution_contract.channels import select
            from update_keys import load_registry
            selected = select('stable', '1.2.3', load_registry()['signing_generation'], SOURCE, read=lambda key: None)
            plan = Path(temporary) / 'plan.json'
            plan.write_text(json.dumps(selected))
            mutations = []
            def publish(*args):
                args[6](SOURCE)
                mutations.append(True)
            def read(key):
                return {'version': '1.2.3', 'withdrawn': True} if key.endswith('/withdrawn.json') else None
            arguments = ['publish', '--artifacts', temporary, '--version', '1.2.3', '--source-commit', SOURCE,
                         '--channel', 'stable', '--plan', str(plan), '--generations', temporary,
                         '--base-url', 'https://downloads.paintedwolf.dev', '--output', str(Path(temporary) / 'forbidden.json')]
            with patch.object(release_repositories, 'publish', side_effect=publish), \
                 patch.object(release_distribution, 'read_storage', side_effect=read):
                self.assertEqual(self.run_main(release_repositories, arguments), 1)
            self.assertEqual(mutations, [])
            self.assertFalse((Path(temporary) / 'forbidden.json').exists())

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
