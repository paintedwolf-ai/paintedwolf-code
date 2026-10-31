"""Repository rehearsal controls are checked without privileged host operations."""

from pathlib import Path
from io import StringIO
import json
import subprocess
import sys
import tempfile
import unittest
from contextlib import ExitStack
from unittest.mock import Mock, patch

from scripts.linux_release import (repository_installation, repository_installation as repository_origin,
                                   repository_rehearsal, repository_r2, repository_setup_rehearsal)


class RepositoryInstallationContracts(unittest.TestCase):
    def test_apt_uses_private_root_and_verifies_installed_identity(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "apt"
            catalog = Path(temporary) / "catalog.json"
            public = Path(temporary) / "public.asc"
            payload = root / "usr/share/paintedwolf-repository-fixture/value"

            def run(command, **_kwargs):
                if command[-2:] == ["install", "paintedwolf-repository-fixture"]:
                    payload.parent.mkdir(parents=True)
                    payload.write_text("0.0.1\n")
                return Mock(returncode=0)

            with patch.object(repository_installation, "selection", return_value=({}, public)), \
                 patch.object(repository_installation.subprocess, "run", side_effect=run) as calls, \
                 patch.object(repository_installation.subprocess, "check_output", side_effect=["amd64\n", "paintedwolf-repository-fixture 0.0.1-1"]), \
                 patch.object(repository_installation, "restore_owner") as restore:
                repository_installation.apt(root, "https://example.invalid/repo", public, catalog,
                                            "paintedwolf-repository-fixture", "0.0.1")
            self.assertIn('Acquire::https::CaInfo', (root / "apt.conf").read_text())
            self.assertIn("Signed-By: " + str(public), (root / "etc/apt/sources.list.d/fixture.sources").read_text())
            self.assertEqual([call.args[0][-2:] for call in calls.call_args_list],
                             [["--no-install-recommends", "update"], ["install", "paintedwolf-repository-fixture"]])
            restore.assert_called_once_with(root)

    def test_apt_rejects_unsupported_architecture_before_install(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "apt"
            with patch.object(repository_installation, "selection", return_value=({}, Path(temporary) / "public.asc")), \
                 patch.object(repository_installation.subprocess, "check_output", return_value="s390x\n"), \
                 patch.object(repository_installation.subprocess, "run") as run:
                with self.assertRaisesRegex(ValueError, "architecture"):
                    repository_installation.apt(root, "https://example.invalid", None, Path(temporary) / "catalog",
                                                "paintedwolf-repository-fixture", "0.0.1")
            run.assert_not_called()

    def test_apt_cleanup_runs_after_package_manager_failure(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "apt"
            with patch.object(repository_installation, "selection", return_value=({}, Path(temporary) / "public.asc")), \
                 patch.object(repository_installation.subprocess, "check_output", return_value="arm64\n"), \
                 patch.object(repository_installation.subprocess, "run", side_effect=OSError("apt failed")), \
                 patch.object(repository_installation, "restore_owner") as restore:
                with self.assertRaisesRegex(OSError, "apt failed"):
                    repository_installation.apt(root, "https://example.invalid", None, Path(temporary) / "catalog",
                                                "paintedwolf-repository-fixture", "0.0.1")
            restore.assert_called_once_with(root)

    def test_dnf_uses_signed_repositories_and_verifies_payload(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "dnf"
            payload = root / "usr/share/paintedwolf-repository-fixture/value"

            def run(command, **_kwargs):
                payload.parent.mkdir(parents=True)
                payload.write_text("0.0.2\n")
                return Mock(returncode=0)

            with patch.object(repository_installation, "selection", return_value=({}, Path(temporary) / "public.asc")), \
                 patch.object(repository_installation.subprocess, "run", side_effect=run) as calls, \
                 patch.object(repository_installation.subprocess, "check_output", return_value="paintedwolf-repository-fixture 0.0.2-1"), \
                 patch.object(repository_installation, "restore_owner") as restore:
                repository_installation.dnf(root, "https://example.invalid/mirrorlist", None,
                                            Path(temporary) / "catalog", "paintedwolf-repository-fixture", "0.0.2")
            config = (root / "dnf.conf").read_text()
            self.assertIn("repo_gpgcheck=1", config)
            self.assertIn("mirrorlist=https://example.invalid/mirrorlist", config)
            self.assertEqual(calls.call_args.args[0][-2:], ["install", "paintedwolf-repository-fixture"])
            restore.assert_called_once_with(root)

    def test_restore_owner_reclaims_files_and_stops_fixture_agents(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            home = root / "key"
            home.mkdir()
            (home / "pubring.kbx").touch()
            with patch.object(repository_installation.subprocess, "run") as run:
                repository_installation.restore_owner(root)
            self.assertEqual(run.call_count, 2)
            self.assertEqual(run.call_args.args[0], ["sudo", "-n", "gpgconf", "--homedir", str(home), "--kill", "gpg-agent"])


class RepositoryOriginContracts(unittest.TestCase):
    def test_tls_origin_starts_and_releases_owned_server(self):
        with tempfile.TemporaryDirectory() as temporary:
            server = Mock(server_port=38671)
            server.socket = Mock()
            server.serve_forever = Mock()
            context = Mock()
            context.wrap_socket.return_value = Mock()
            with patch.object(repository_origin.subprocess, "run") as openssl, \
                 patch.object(repository_origin.ssl, "SSLContext", return_value=context), \
                 patch.object(repository_origin, "ThreadingHTTPServer", return_value=server), \
                 patch.object(repository_origin.threading, "Thread") as thread:
                with repository_origin.serve(Path(temporary)) as (url, certificate):
                    self.assertEqual(url, "https://127.0.0.1:38671")
                    self.assertEqual(certificate, Path(temporary) / "fixture-tls.crt")
                self.assertEqual(openssl.call_args.args[0][0], "openssl")
                self.assertTrue((Path(temporary) / "origin").is_dir())
                server.shutdown.assert_called_once()
                server.server_close.assert_called_once()
                thread.return_value.join.assert_called_once()


class RepositoryRehearsalContracts(unittest.TestCase):
    def test_fixture_rejects_unsupported_architecture_before_building_packages(self):
        with tempfile.TemporaryDirectory() as temporary, \
             patch.object(repository_rehearsal.platform, "machine", return_value="s390x"), \
             patch.object(repository_rehearsal.subprocess, "run") as run:
            with self.assertRaisesRegex(ValueError, "Linux amd64 or arm64"):
                repository_rehearsal.fixtures(Path(temporary), "0.0.1")
        run.assert_not_called()

    def test_candidate_receipt_cannot_be_written_into_an_input_or_retained_root(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            candidate = root / "candidates"
            retained = root / "retained"
            for receipt in (candidate / "receipt.json", retained / "receipt.json"):
                arguments = ["repository_rehearsal", "candidates", "--receipt", str(receipt),
                             "--candidate-dir", str(candidate), "--retain-repositories", str(retained),
                             "--source-commit", "f" * 40, "--version", "0.0.1",
                             "--channel", "preview", "--release-build", "1"]
                with self.subTest(receipt=receipt), patch.object(sys, "argv", arguments), \
                     patch.object(repository_rehearsal, "retain_candidates") as retain:
                    with self.assertRaises(SystemExit) as error:
                        repository_rehearsal.main()
                    self.assertEqual(error.exception.code, 2)
                    retain.assert_not_called()

    def test_generation_recovery_rotation_and_remote_refusal_use_disposable_roots(self):
        with tempfile.TemporaryDirectory() as temporary, ExitStack() as patches:
            work = Path(temporary)
            observed = {"install": [], "publish": [], "remote": [], "setup": [], "upgrade": [], "tampered": False}
            local_active, remote_active = {}, {}
            local_withdrawn, remote_withdrawn = set(), set()
            serial = 0

            def fixture(directory, version):
                paths = {}
                for family in ["deb", "rpm"]:
                    path = directory / (family + "-" + version)
                    path.write_bytes(family.encode() + version.encode())
                    paths[family] = path
                return {**paths, "architecture": "amd64"}

            def catalog(path):
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(json.dumps({"signing_generation": 1, "generations": [{"generation": 1}]}))
                return path

            def assemble(family, manifest, source_parent, destination, *_args):
                destination.mkdir()
                source = manifest["packages"][0]
                (destination / source["file"]).write_bytes(b"package")
                return {**manifest, "family": family, "channel": "preview", "objects": ["object"],
                        "signer": str(_args[1])}

            def admit(receipt, destination, catalog, rehearsal=False):
                selected = destination / receipt["packages"][0]["file"]
                if (not rehearsal or str(catalog) != receipt["signer"] or not selected.is_file()
                        or selected.read_bytes() != b"package"):
                    raise ValueError("repository receipt did not match selected bytes and trust")

            def publish(receipt, _directory, _store, _origin, catalog, previous, _rehearsal, operation="publish"):
                nonlocal serial
                family = receipt["family"]
                if previous != local_active.get(family, {}).get("generation"):
                    raise ValueError("stale repository activation")
                if str(catalog) != receipt["signer"]:
                    raise ValueError("wrong signing catalog")
                if operation == "publish" and ((family, receipt["version"]) in local_withdrawn or
                                               (family in local_active and local_active[family]["signer"] != receipt["signer"])):
                    raise ValueError("withdrawn version or changed signer")
                if operation == "withdraw":
                    local_withdrawn.add((family, local_active[family]["version"]))
                serial += 1
                observed["publish"].append((family, receipt["version"], operation))
                local_active[family] = {"generation": str(serial), "version": receipt["version"],
                                        "signer": receipt["signer"]}
                return {"generation": str(serial)}

            def publish_remote(receipt, _directory, catalog, _prefix, _origin, previous, _rehearsal,
                               operation="publish", phase="activate"):
                if observed["tampered"]:
                    raise subprocess.CalledProcessError(1, "put")
                nonlocal serial
                family = receipt["family"]
                if previous != remote_active.get(family, {}).get("generation"):
                    raise ValueError("stale remote activation")
                if str(catalog) != receipt["signer"]:
                    raise ValueError("wrong remote signing catalog")
                if operation == "publish" and ((family, receipt["version"]) in remote_withdrawn or
                                               (family in remote_active and remote_active[family]["signer"] != receipt["signer"])):
                    raise ValueError("withdrawn remote version or changed signer")
                if operation == "withdraw":
                    remote_withdrawn.add((family, remote_active[family]["version"]))
                serial += 1
                observed["remote"].append((family, receipt["version"], operation, phase))
                if phase == "activate":
                    remote_active[family] = {"generation": str(serial), "version": receipt["version"],
                                             "signer": receipt["signer"]}
                return {"generation": str(serial)}

            def tamper(*_args):
                observed["tampered"] = True

            def delete(*_args):
                observed["tampered"] = False

            def install(root, uri, certificate, _catalog, package, version):
                observed["install"].append((root.name, uri, certificate, package, version))
                if root.name.endswith("wrong-key-root"):
                    raise subprocess.CalledProcessError(1, "native package manager")

            for name, replacement in {
                    "fixtures": fixture, "ephemeral": catalog, "assemble": assemble,
                    "admit": admit, "publish": publish, "publish_r2": publish_remote,
                    "install_apt": install, "install_dnf": install, "warm_absence": Mock(),
                    "tamper_fixture_object": tamper, "delete_fixture_object": delete,
                    "rehearse_setup": lambda *args: observed["setup"].append(args),
                    "rehearse_setup_upgrade": lambda *args: observed["upgrade"].append(args),
                    "digest": lambda path: "f" * 64,
            }.items():
                patches.enter_context(patch.object(repository_rehearsal, name, replacement))
            with patch.dict(repository_rehearsal.os.environ, {"LINUX_PACKAGE_SIGNING_KEY": "must be removed"}):
                result = repository_rehearsal.rehearse(work, "https://127.0.0.1:1234", work / "cert", "https://r2.invalid")
                self.assertNotIn("LINUX_PACKAGE_SIGNING_KEY", repository_rehearsal.os.environ)
            self.assertEqual(len(result["generations"]), 6)
            self.assertTrue(result["isolated_r2_published"])
            self.assertFalse(result["application_qualified"])
            self.assertEqual(len(observed["setup"]), 2)
            self.assertEqual(len(observed["upgrade"]), 2)
            self.assertIn(("deb", "0.0.1", "withdraw"), observed["publish"])
            self.assertIn(("rpm", "0.0.3", "rotate"), observed["publish"])
            self.assertTrue(any(row[-1] == "stage" for row in observed["remote"]))
            self.assertTrue(any(row[0].endswith("wrong-key-root") for row in observed["install"]))


class RepositorySetupRehearsalContracts(unittest.TestCase):
    def receipt(self, family):
        return {"family": family, "source_commit": "f" * 40, "version": "0.0.1", "channel": "preview",
                "repository_generation": "a" * 64, "inventory_sha256": "b" * 64,
                "signing_generation": 1, "key_fingerprint": "C" * 40,
                "package": {"file": "setup." + family},
                "installed_files": [{"path": "etc/repository-fixture", "sha256": "d" * 64}]}

    def test_setup_rejects_unsafe_origins_and_preserves_user_data_on_remove(self):
        for family in ["deb", "rpm"]:
            with self.subTest(family=family), tempfile.TemporaryDirectory() as temporary:
                work = Path(temporary)
                directory = work / "generation"
                directory.mkdir()
                catalog = work / "catalog.json"
                receipt = self.receipt(family)
                origin = "https://repository.example.invalid"
                config = work / (family + "-setup-root/etc/repository-fixture")
                actions = []

                def build(_manifest, _directory, _catalog, output, url, _private, rehearsal=False):
                    if url != origin or output == directory / "setup" or not rehearsal:
                        raise ValueError("unsafe setup origin or destination")
                    output.mkdir()
                    (output / receipt["package"]["file"]).write_bytes(b"setup")
                    return receipt

                def admit(artifact, _sums, _signature, _catalog, **authority):
                    if artifact.is_symlink() or any(authority.get(key) != receipt[key] for key in receipt if key not in {"package", "installed_files"}) or authority["base_url"] != origin:
                        raise ValueError("setup authority mismatch")
                    return receipt

                def run(command, **_kwargs):
                    actions.append(command)
                    if "--install" in command:
                        config.parent.mkdir(parents=True, exist_ok=True)
                        config.write_bytes(b"configuration")
                    if "--erase" in command or "--purge" in command:
                        config.unlink()
                    return Mock(returncode=0)

                with ExitStack() as patches:
                    for name, replacement in {"build": build, "admit_setup": admit, "verify": Mock(),
                                               "digest": lambda _path: "d" * 64,
                                               "restore_owner": Mock()}.items():
                        patches.enter_context(patch.object(repository_setup_rehearsal, name, replacement))
                    patches.enter_context(patch.object(repository_setup_rehearsal.subprocess, "run", side_effect=run))
                    patches.enter_context(patch.object(repository_setup_rehearsal.subprocess, "check_output", return_value="/etc/repository-fixture\n"))
                    repository_setup_rehearsal.rehearse(work, receipt, directory, catalog, origin)
                self.assertTrue(any("--install" in action for action in actions))
                self.assertTrue(any("--purge" in action or "--erase" in action for action in actions))
                self.assertEqual((work / (family + "-setup-root/home/fixture/.local/share/paintedwolf/preserve")).read_text(),
                                 "mutable user data\n")
                self.assertFalse(config.exists())

    def test_same_version_upgrade_rejects_mismatch_before_install(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            old = self.receipt("deb")
            successor = {**old, "version": "0.0.2"}
            with patch.object(repository_setup_rehearsal.subprocess, "run") as run:
                with self.assertRaisesRegex(ValueError, "preserve the application version"):
                    repository_setup_rehearsal.rehearse_upgrade(work, (old, work), (successor, work), work / "old",
                                                                work / "new", "https://repository.example.invalid")
            run.assert_not_called()

    def test_same_version_setup_rotation_installs_then_removes_only_owned_files(self):
        for family in ["deb", "rpm"]:
            with self.subTest(family=family), tempfile.TemporaryDirectory() as temporary:
                work = Path(temporary)
                old = self.receipt(family)
                successor = {**old, "signing_generation": 2, "key_fingerprint": "D" * 40}
                root = work / (family + "-setup-upgrade-root")
                installed = root / "etc/repository-fixture"
                actions = []

                def build(manifest, _directory, _catalog, output, _origin, _private, _rehearsal):
                    output.mkdir()
                    (output / ("setup." + family)).write_bytes(b"setup-" + str(manifest["signing_generation"]).encode())

                def run(command, **_kwargs):
                    actions.append(command)
                    if "--install" in command or "--upgrade" in command:
                        installed.parent.mkdir(parents=True, exist_ok=True)
                        installed.write_bytes(b"configuration")
                    if "--purge" in command or "--erase" in command:
                        installed.unlink()
                    return Mock(returncode=0)

                native = {"file": "setup." + family, "version": "0.0.1", "release": "1"}
                with ExitStack() as patches:
                    for name, replacement in {
                            "selection": lambda catalog, _rehearsal: ({"generation": 1 if catalog.name == "old" else 2,
                                                                         "fingerprint": "C" * 40 if catalog.name == "old" else "D" * 40},
                                                                        catalog.parent / "public.asc"),
                            "package_identity": lambda *_args: native,
                            "build": build, "admit_setup": lambda *_args, **_kwargs: {"installed_files": old["installed_files"]},
                            "identity": lambda _manifest: "a" * 64,
                            "digest": lambda _path: "d" * 64,
                            "restore_owner": Mock(),
                    }.items():
                        patches.enter_context(patch.object(repository_setup_rehearsal, name, replacement))
                    patches.enter_context(patch.object(repository_setup_rehearsal.subprocess, "run", side_effect=run))
                    patches.enter_context(patch.object(repository_setup_rehearsal.subprocess, "check_output",
                                                       return_value="0.0.1" if family == "deb" else "0.0.1-1"))
                    repository_setup_rehearsal.rehearse_upgrade(work, (old, work), (successor, work), work / "old", work / "new",
                                                                "https://repository.example.invalid")
                self.assertEqual(sum("--install" in action for action in actions), 2 if family == "deb" else 1)
                self.assertTrue(any("--purge" in action or "--erase" in action for action in actions))
                self.assertFalse(installed.exists())
                self.assertEqual((root / "home/fixture/.local/share/paintedwolf/preserve").read_text(),
                                 "mutable user data\n")


class RepositoryR2CliContracts(unittest.TestCase):
    def test_cli_uses_reviewed_receipt_and_explicit_rehearsal_mode(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            receipt = {"family": "deb", "version": "0.0.1"}
            (directory / "generation.json").write_text(json.dumps(receipt))
            arguments = ["repository-r2", "--directory", str(directory), "--catalog", str(directory / "key"),
                         "--storage-prefix", "release-system-tests/test", "--base-url", "https://example.invalid",
                         "--rehearsal", "--phase", "stage"]
            with patch.object(sys, "argv", arguments), patch.object(repository_r2, "publish_r2",
                                                      return_value={"publication_phase": "staged"}) as publish, \
                 patch.object(sys, "stdout", new_callable=StringIO) as output:
                repository_r2.main()
            self.assertEqual(publish.call_args.args[0], receipt)
            self.assertEqual(publish.call_args.args[-2:], ("publish", "stage"))
            self.assertTrue(publish.call_args.args[-3])
            self.assertIn('"publication_phase": "staged"', output.getvalue())

    def test_cli_reports_rejected_publication_without_emitting_success(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            (directory / "generation.json").write_text('{}')
            arguments = ["repository-r2", "--directory", str(directory), "--catalog", str(directory / "key"),
                         "--storage-prefix", "release-system-tests/test", "--base-url", "https://example.invalid"]
            with patch.object(sys, "argv", arguments), patch.object(repository_r2, "publish_r2",
                                                      side_effect=ValueError("authorization refused")), \
                 patch.object(sys, "stderr", new_callable=StringIO) as error:
                with self.assertRaises(SystemExit) as exit_status:
                    repository_r2.main()
            self.assertEqual(exit_status.exception.code, 1)
            self.assertIn("authorization refused", error.getvalue())
