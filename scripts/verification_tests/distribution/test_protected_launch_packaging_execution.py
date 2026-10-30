"""The packages install the protected launch: bootstrap, sealed catalog and generation lifecycle."""

import argparse
import copy
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import shutil
import sys

from linux_release import payload, protected_launch
from verification_tests.distribution.maintainer_fixture import program

ROOT = Path(__file__).resolve().parents[3]
LINUX = ROOT / "packaging/linux"


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), ROOT / "scripts" / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class Catalogs(unittest.TestCase):
    def test_the_committed_catalogs_name_every_family_cpu_and_the_fixed_bootstrap(self):
        catalog = protected_launch.load()
        self.assertEqual(set(catalog["families"]), {"deb", "rpm", "pacman"})
        self.assertEqual(catalog["architectures"]["arm64"]["multiarch"], "aarch64-linux-gnu")
        for family, row in catalog["families"].items():
            self.assertTrue(all(path.rsplit("/", 1)[1].startswith("WebKit") for path in row["system_executables"]), family)
        self.assertEqual({family: row["library_roots"] for family, row in catalog["families"].items()},
                         {"deb": ["/usr/lib/{multiarch}"], "rpm": ["/usr/lib64"], "pacman": ["/usr/lib"]})
        self.assertEqual(payload.load()["setuid_bootstrap"], {"name": "pw-launch", "path": "/usr/libexec/painted-wolf-code/pw-launch"})
        self.assertIn("pw-launch-entry", payload.load()["helpers"])
        self.assertIn("pw-generations", payload.load()["helpers"])
        self.assertIn("pw-mount-intake-transition", payload.load()["helpers"])

    def test_invalid_launch_catalogs_are_refused(self):
        valid = json.loads(protected_launch.CATALOG.read_text())
        changes = {
            "missing family": lambda c: c["families"].pop("rpm"),
            "missing cpu": lambda c: c["architectures"].pop("arm64"),
            "bad multiarch": lambda c: c["architectures"]["amd64"].update(multiarch="../x"),
            "relative program": lambda c: c["families"]["deb"].update(system_executables=["usr/lib/WebKitWebProcess"]),
            "outside usr": lambda c: c["families"]["deb"].update(system_executables=["/opt/WebKitWebProcess"]),
            "unknown template": lambda c: c["families"]["rpm"].update(system_executables=["/usr/{lib}/WebKitWebProcess"]),
            "empty family": lambda c: c["families"]["pacman"].update(system_executables=[]),
            "no library roots": lambda c: c["families"]["deb"].pop("library_roots"),
            "empty library roots": lambda c: c["families"]["rpm"].update(library_roots=[]),
            "library root outside usr": lambda c: c["families"]["pacman"].update(library_roots=["/home/lib"]),
            "extra key": lambda c: c.update(notes="x"),
        }
        for name, change in changes.items():
            with self.subTest(name), tempfile.TemporaryDirectory() as temporary:
                catalog = copy.deepcopy(valid)
                change(catalog)
                path = Path(temporary) / "catalog.json"
                path.write_text(json.dumps(catalog))
                with self.assertRaises(ValueError):
                    protected_launch.load(path)

    def test_the_bootstrap_must_stay_outside_the_payload_at_its_fixed_path(self):
        valid = json.loads(payload.CATALOG.read_text())
        for name, bootstrap in {"moved": {"name": "pw-launch", "path": "/usr/bin/pw-launch"},
                                "payload helper": {"name": "pw", "path": "/usr/libexec/painted-wolf-code/pw"},
                                "missing": None}.items():
            with self.subTest(name), tempfile.TemporaryDirectory() as temporary:
                catalog = {**valid, "setuid_bootstrap": bootstrap}
                path = Path(temporary) / "payload.json"
                path.write_text(json.dumps(catalog))
                with self.assertRaisesRegex(ValueError, "setuid_bootstrap"):
                    payload.load(path)


class PackagePlacement(unittest.TestCase):
    def stage(self, root):
        target = "x86_64-unknown-linux-gnu"
        tauri = root / "lycaon-den/src-tauri"
        manifest = load("linux-package-manifest")
        paths = [tauri / "target" / target / "release/painted-wolf-code"]
        paths += [tauri / "binaries" / f"{helper}-{target}" for helper in (*manifest.HELPERS, "pw-launch")]
        paths += [root / "packaging/linux/bin" / name for name in manifest.ENTRY_POINTS]
        for path in paths:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b"fixture executable " + path.name.encode())
            path.chmod(0o755)
        (tauri / "engine-root").mkdir()
        (root / "VERSION").write_text("1.2.3\n")
        (tauri / "THIRD-PARTY-NOTICES.md").write_text("notices\n")
        (root / "packaging/linux/protected-launch.json").write_bytes(protected_launch.CATALOG.read_bytes())
        return manifest, target, tauri

    def test_the_catalog_is_sealed_payload_and_the_bootstrap_is_a_listed_executable(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            manifest, target, _ = self.stage(root)
            args = argparse.Namespace(root=root, target=target, version="1.2.3", output=root / "manifest.json")
            manifest.generate(args)
            sealed = json.loads(args.output.read_text())
            self.assertIn("/usr/libexec/painted-wolf-code/pw-launch", {row["path"] for row in sealed["executables"]})
            self.assertIn("protected-launch.json", {row["path"] for row in sealed["payload"]})
            transition = next(row for row in sealed["payload"] if row["path"] == "bin/pw-mount-intake-transition")
            self.assertEqual(transition["mode"], 0o755)
            self.assertIn("/opt/painted-wolf-code/bin/pw-mount-intake-transition",
                          {row["path"] for row in sealed["executables"]})
            self.assertFalse(any(row["path"].startswith("bin/pw-launch") and row["path"] != "bin/pw-launch-entry"
                                 for row in sealed["payload"]), "the setuid bootstrap must not be generation payload")

    def test_the_package_places_the_bootstrap_outside_the_install_root(self):
        config = load("linux-package-config")
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            _, target, tauri = self.stage(root)
            (root / "packaging/linux/dev.paintedwolf.code.desktop").write_text("[Desktop Entry]\n")
            (root / "manifest.json").write_text("{}")
            files = config.required_files(root, tauri, target, root / "manifest.json")
            self.assertEqual(files["/usr/libexec/painted-wolf-code/pw-launch"], tauri / "binaries" / f"pw-launch-{target}")
            self.assertIn("/opt/painted-wolf-code/protected-launch.json", files)
            (tauri / "binaries" / f"pw-launch-{target}").unlink()
            with self.assertRaisesRegex(ValueError, "pw-launch"):
                config.required_files(root, tauri, target, root / "manifest.json")

    def test_the_desktop_entry_starts_the_protected_bootstrap(self):
        entry = (LINUX / "dev.paintedwolf.code.desktop").read_text().splitlines()
        self.assertIn("Exec=/usr/libexec/painted-wolf-code/pw-launch %U", entry)
        self.assertIn("TryExec=/usr/libexec/painted-wolf-code/pw-launch", entry)

    def test_the_rpm_file_list_owns_the_install_root_and_the_bootstrap_directory(self):
        assemble = load("linux-package-assemble")
        with tempfile.TemporaryDirectory() as temporary:
            tree = Path(temporary)
            for relative in ("opt/painted-wolf-code/bin/painted-wolf-code",
                             "usr/libexec/painted-wolf-code/pw-launch",
                             "usr/bin/pw"):
                path = tree / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b"fixture")
            entries = assemble.rpm_file_list(tree)
            self.assertIn('%dir "/opt/painted-wolf-code"', entries)
            self.assertIn('%dir "/opt/painted-wolf-code/bin"', entries)
            self.assertIn('%dir "/usr/libexec/painted-wolf-code"', entries)
            self.assertIn('"/usr/libexec/painted-wolf-code/pw-launch"', entries)
            for shared in ('%dir "/usr"', '%dir "/usr/libexec"', '%dir "/usr/bin"'):
                self.assertNotIn(shared, entries)
            shutil.rmtree(tree / "usr/libexec/painted-wolf-code")
            with self.assertRaisesRegex(ValueError, "/usr/libexec/painted-wolf-code"):
                assemble.rpm_file_list(tree)


class MaintainerScripts(unittest.TestCase):
    """The real scripts run with their fixed system paths moved under a scratch root."""

    def setUp(self):
        if sys.platform == "linux" and shutil.which("flock") is None:
            raise AssertionError("Linux maintainer script tests require flock")
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.install = self.root / "opt/painted-wolf-code"
        self.policy = self.root / "etc/paintedwolf"
        self.locks = self.root / "run/paintedwolf/generation-locks"
        self.calls = self.root / "calls"
        self.shims = self.root / "shims"
        self.shims.mkdir()
        for name in ("systemctl", "update-desktop-database", "gtk-update-icon-cache", "apparmor_parser"):
            self.shim(name, "exit 0")
        if shutil.which("flock") is None:
            flock = self.shims / "flock"
            flock.write_text(
                f"#!{sys.executable}\n"
                "import fcntl, os, sys\n"
                "assert sys.argv[1:3] == ['-n', '-x']\n"
                "fd = os.open(sys.argv[3], os.O_RDWR | os.O_CREAT, 0o600)\n"
                "try:\n"
                "    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)\n"
                "except BlockingIOError:\n"
                "    raise SystemExit(1)\n"
                "os.execvp(sys.argv[4], sys.argv[4:])\n"
            )
            flock.chmod(0o755)
        self.installer = self.root / "pw-generations"
        self.installer_status(0)

    def shim(self, name, body):
        path = self.shims / name
        path.write_text(f"#!/bin/sh\n{body}\n")
        path.chmod(0o755)

    def installer_status(self, status):
        self.installer.write_text(f'#!/bin/sh\necho "$*" >> "{self.calls}"\nexit {status}\n')
        self.installer.chmod(0o755)

    def run_script(self, family, name, *args):
        source = (LINUX / family / name).read_text()
        script = program(source, self.root, self.installer)
        path = self.root / f"{family}-{name}"
        path.write_text(script if script.startswith("#!") else "#!/bin/sh\n" + script)
        path.chmod(0o755)
        environment = {"PATH": f"{self.shims}:/usr/bin:/bin"}
        return subprocess.run([str(path), *args], env=environment, capture_output=True, text=True, timeout=60)

    def recorded(self):
        return self.calls.read_text().splitlines() if self.calls.exists() else []

    def test_configure_and_post_install_select_the_payload_and_fail_closed(self):
        for family, script, args, call in (("deb", "postinst", ("configure", "1.0.1"), "install --family deb"),
                                            ("rpm", "post", ("2",), "install --family rpm")):
            with self.subTest(family):
                self.calls.unlink(missing_ok=True)
                self.installer_status(0)
                self.assertEqual(self.run_script(family, script, *args).returncode, 0)
                self.assertEqual(self.recorded(), [call])
                self.installer_status(1)
                self.assertNotEqual(self.run_script(family, script, *args).returncode, 0)
        self.calls.unlink(missing_ok=True)
        self.installer_status(0)
        self.assertEqual(self.run_script("deb", "postinst", "abort-upgrade", "1.0.2").returncode, 0)
        self.assertEqual(self.recorded(), [])

    def test_only_removal_withdraws_and_a_failure_never_blocks_it(self):
        for family, script, removal, upgrade in (("deb", "prerm", ("remove",), ("upgrade", "1.0.3")),
                                                  ("rpm", "preun", ("0",), ("1",))):
            with self.subTest(family):
                self.calls.unlink(missing_ok=True)
                self.installer_status(1)
                result = self.run_script(family, script, *removal)
                self.assertEqual((result.returncode, self.recorded()), (0, ["remove"]))
                self.calls.unlink(missing_ok=True)
                self.assertEqual(self.run_script(family, script, *upgrade).returncode, 0)
                self.assertEqual(self.recorded(), [])

    def generation(self, name):
        path = self.install / "generations" / name
        (path / "bin").mkdir(parents=True)
        (path / "bin" / "pw").write_bytes(b"engine")
        (path / "empty-resource-directory").mkdir()
        return path

    def test_purge_reclaims_only_unheld_generations_and_the_policy(self):
        for family, script, purge in (("deb", "postrm", ("purge",)), ("rpm", "postun", ("0",))):
            with self.subTest(family):
                held, free = self.generation("a" * 64), self.generation("b" * 64)
                interrupted = self.generation(".install-" + "c" * 64)
                invalid = [
                    self.generation("not-a-digest"),
                    self.generation("F" * 64),
                    self.generation("d" * 63),
                    self.generation("d" * 65),
                    self.generation(".install-not-hex"),
                    self.generation(".install-.install-" + "e" * 64),
                ]
                self.policy.mkdir(parents=True, exist_ok=True)
                (self.policy / "launch.json").write_text("{}")
                self.locks.mkdir(parents=True, exist_ok=True)
                lock = self.locks / ("a" * 64 + ".lock")
                lock.touch(mode=0o600)
                with lock.open("r+") as handle:
                    fcntl.flock(handle, fcntl.LOCK_SH)
                    result = self.run_script(family, script, *purge)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertTrue((held / "empty-resource-directory").is_dir(), "a running launch's generation was changed")
                self.assertFalse(free.exists() or interrupted.exists())
                self.assertTrue(all(path.is_dir() for path in invalid))
                self.assertFalse(self.policy.exists())
                self.assertEqual({created.name for created in self.locks.iterdir()}, {f"{ch * 64}.lock" for ch in "abc"})
                for created in self.locks.iterdir():
                    self.assertEqual(created.stat().st_mode & 0o777, 0o600, created.name)
                subprocess.run(["rm", "-rf", str(self.install), str(self.locks)], check=True)

    def test_a_reinstall_accepts_held_generations_but_not_foreign_content(self):
        self.generation("d" * 64)
        for family, script, args in (("deb", "preinst", ("install", "1.0.1")), ("rpm", "pre", ("1",))):
            with self.subTest(family):
                self.assertEqual(self.run_script(family, script, *args).returncode, 0)
        (self.install / "notes.txt").write_text("someone else's")
        for family, script, args in (("deb", "preinst", ("install",)), ("rpm", "pre", ("1",))):
            with self.subTest(family + " foreign"):
                self.assertNotEqual(self.run_script(family, script, *args).returncode, 0)


if __name__ == "__main__":
    unittest.main()
