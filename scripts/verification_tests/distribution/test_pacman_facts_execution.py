"""Installed ALPM evidence is bound to selected ownership and final bytes."""

from copy import deepcopy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release import pacman_facts


class PacmanInstalledFactsTests(unittest.TestCase):
    def fixture(self, directory):
        root = Path(directory)
        database = root / "var/lib/pacman"
        record = database / "local/painted-wolf-code-1.0.2pre.rc6-1"
        record.mkdir(parents=True)
        (record.parent / "ALPM_DB_VERSION").write_text("9\n")
        (record / "desc").write_text("%NAME%\npainted-wolf-code\n\n%VERSION%\n1.0.2pre.rc6-1\n\n%ARCH%\nx86_64\n\n%VALIDATION%\npgp\n\n")
        (record / "files").write_text("%FILES%\nopt/painted-wolf-code/bin/pw\n\n%BACKUP%\n")
        executable = root / "opt/painted-wolf-code/bin/pw"
        executable.parent.mkdir(parents=True)
        executable.write_bytes(b"selected final executable")
        executable.chmod(0o755)
        manifest = [{"path": "/opt/painted-wolf-code/bin/pw", "size": executable.stat().st_size, "mode": 0o755,
                     "sha256": hashlib.sha256(executable.read_bytes()).hexdigest()}]
        return root, database, record, executable, manifest

    def collect(self, fixture, **changes):
        root, database, _, _, manifest = fixture
        values = {"database": database, "package": "painted-wolf-code", "expected_version": "1.0.2pre.rc6-1",
                  "architecture": "x86_64", "executables": manifest, "root": root}
        values.update(changes)
        return pacman_facts.installed(**values)

    def test_selected_package_owns_final_bytes_but_database_is_not_fresh_signature_or_desktop_proof(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.fixture(directory)
            report = self.collect(fixture)
            self.assertEqual(report["executables"], fixture[-1])
            self.assertEqual(report["validation_record"], ["pgp"])
            self.assertFalse(report["package_signature_verified"])
            self.assertFalse(report["desktop_qualified"])

    def test_changed_bytes_modes_and_missing_ownership_fail(self):
        for change in ["bytes", "mode", "executable_mode", "owner", "symlink"]:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                fixture = self.fixture(directory)
                _, _, record, executable, _ = fixture
                if change == "bytes":
                    executable.write_bytes(b"different final executable")
                elif change == "mode":
                    executable.chmod(0o644)
                elif change == "executable_mode":
                    executable.chmod(0o775)
                elif change == "owner":
                    (record / "files").write_text("%FILES%\nother/file\n")
                else:
                    real = executable.with_suffix(".real")
                    executable.rename(real)
                    executable.symlink_to(real)
                with self.assertRaises(ValueError):
                    self.collect(fixture)

    def test_transaction_and_unknown_database_format_are_unverified(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.fixture(directory)
            lock = fixture[1] / "db.lck"
            lock.touch()
            with self.assertRaisesRegex(ValueError, "transaction"):
                self.collect(fixture)
            lock.unlink()
            (fixture[2].parent / "ALPM_DB_VERSION").write_text("10\n")
            with self.assertRaisesRegex(ValueError, "unsupported"):
                self.collect(fixture)

    def test_version_architecture_and_duplicate_install_records_cannot_match(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.fixture(directory)
            for changes in [{"expected_version": "1.0.2-1"}, {"architecture": "aarch64"}]:
                with self.subTest(changes=changes), self.assertRaises(ValueError):
                    self.collect(fixture, **changes)
            second = fixture[2].parent / "other-version"
            second.mkdir()
            (second / "desc").write_bytes((fixture[2] / "desc").read_bytes())
            with self.assertRaisesRegex(ValueError, "exactly one"):
                self.collect(fixture)

    def test_database_changes_during_measurement_are_unverified(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.fixture(directory)
            original = pacman_facts.read
            count = 0
            def changed(path, limit=16 << 20):
                nonlocal count
                if path == fixture[2] / "files":
                    count += 1
                    if count == 2:
                        return original(path, limit) + b"changed\n"
                return original(path, limit)
            with patch.object(pacman_facts, "read", side_effect=changed), self.assertRaisesRegex(ValueError, "changed"):
                self.collect(fixture)

    def test_malformed_fields_and_escaping_paths_are_rejected(self):
        for data in [b"unframed value", b"%NAME%\na\n\n%NAME%\nb\n"]:
            with self.subTest(data=data), self.assertRaises(ValueError):
                pacman_facts.sections(data)
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.fixture(directory)
            (fixture[2] / "files").write_text("%FILES%\n../escape\n")
            with self.assertRaisesRegex(ValueError, "unsafe"):
                self.collect(fixture)

    def test_selected_manifest_cannot_repeat_or_traverse_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.fixture(directory)
            manifest = deepcopy(fixture[-1])
            for rows in [manifest + manifest, [dict(manifest[0], path="/opt/../escape")],
                         [dict(manifest[0], path="//opt/painted-wolf-code/bin/pw")]]:
                with self.subTest(rows=rows), self.assertRaises(ValueError):
                    self.collect(fixture, executables=rows)

    def test_descriptor_and_named_executable_must_remain_the_same_file(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.fixture(directory)
            executable = fixture[3]
            original = pacman_facts.os.fstat
            calls = 0
            def replaced(descriptor):
                nonlocal calls
                result = original(descriptor)
                calls += 1
                if calls == 2:
                    executable.rename(executable.with_suffix(".old"))
                    executable.write_bytes(b"selected final executable")
                    executable.chmod(0o755)
                return result
            with patch.object(pacman_facts.os, "fstat", side_effect=replaced):
                with self.assertRaisesRegex(ValueError, "changed"):
                    pacman_facts.executable_identity(executable, fixture[-1][0])

    def test_cli_cannot_write_evidence_over_inputs_or_inside_the_alpm_database(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.fixture(directory)
            manifest = fixture[0] / "selected.json"
            manifest.write_text(json.dumps({"executables": fixture[-1]}))
            for destination in [manifest, fixture[2] / "desc", fixture[1] / "new-record"]:
                argv = ["pacman_facts", "--database", str(fixture[1]), "--version", "1.0.2pre.rc6-1",
                        "--manifest", str(manifest), "--output", str(destination)]
                with self.subTest(destination=destination), patch("sys.argv", argv):
                    with patch.object(pacman_facts.platform, "system", return_value="Linux"):
                        with patch.object(pacman_facts, "installed") as collect:
                            with self.assertRaises(SystemExit) as failure:
                                pacman_facts.main()
                            self.assertEqual(failure.exception.code, 1)
                            collect.assert_not_called()
            self.assertFalse((fixture[1] / "new-record").exists())
