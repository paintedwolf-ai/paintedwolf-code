"""CI package installs move off an unreachable Ubuntu origin without hiding other failures."""
import importlib.util
import io
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]
SPEC = importlib.util.spec_from_file_location("apt_origins", ROOT / "scripts/ci_policy/apt_origins.py")
APT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(APT)

DEB822 = """Types: deb
URIs: http://azure.archive.ubuntu.com/ubuntu/
Suites: noble noble-updates noble-backports
Components: main universe restricted multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg

Types: deb
URIs: http://security.ubuntu.com/ubuntu/
Suites: noble-security
Components: main universe restricted multiverse
"""
THIRD_PARTY = "deb [arch=amd64] https://packages.microsoft.com/ubuntu/24.04/prod noble main\n"
AZURE_DOWN = ("Ign:2 http://azure.archive.ubuntu.com/ubuntu noble InRelease\n"
              "Err:2 http://azure.archive.ubuntu.com/ubuntu noble InRelease\n  Connection timed out\n")


class AptOriginsTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        (self.root / "sources.list.d").mkdir()
        (self.root / "sources.list.d/ubuntu.sources").write_text(DEB822)
        (self.root / "sources.list.d/microsoft-prod.list").write_text(THIRD_PARTY)
        (self.root / "sources.list").write_text("# Ubuntu sources have moved to sources.list.d\n")
        for name, value in (("APT_ROOT", self.root), ("next_stage", 0)):
            patcher = patch.object(APT, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        self.calls = []

    def apt(self, *results):
        """Replay apt-get results in order, recording each argument list."""
        queue = list(results)

        def run(command, **_options):
            self.calls.append(command)
            code, output = queue.pop(0)
            return subprocess.CompletedProcess(command, code, stdout=output)
        return patch.object(APT.subprocess, "run", side_effect=run)

    def sources(self):
        return (self.root / "sources.list.d/ubuntu.sources").read_text()

    def test_reachable_origin_is_kept(self):
        with self.apt((0, "Hit:1 http://azure.archive.ubuntu.com/ubuntu noble InRelease\n")), \
                patch("sys.stdout", io.StringIO()):
            APT.update()
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(self.calls[0][:4], ["sudo", "timeout", "--kill-after=10s", f"{APT.ATTEMPT_SECONDS}s"])
        self.assertEqual(self.calls[0][-1], "update")
        self.assertEqual(self.sources(), DEB822)

    def test_unreachable_ubuntu_origin_moves_to_canonical_archive(self):
        for code in (0, 100, 124):
            with self.subTest(code=code):
                (self.root / "sources.list.d/ubuntu.sources").write_text(DEB822)
                APT.next_stage = 0
                self.calls.clear()
                with self.apt((code, AZURE_DOWN if code != 124 else ""), (0, "")), patch("sys.stdout", io.StringIO()):
                    APT.update()
                self.assertEqual(len(self.calls), 2)
                self.assertIn("URIs: https://archive.ubuntu.com/ubuntu/", self.sources())
                self.assertIn("URIs: https://security.ubuntu.com/ubuntu/", self.sources())
                self.assertNotIn("azure", self.sources())
                self.assertEqual((self.root / "sources.list.d/microsoft-prod.list").read_text(), THIRD_PARTY)

    def test_unreachable_ubuntu_hosts_move_to_a_mirror_outside_ubuntu_com(self):
        (self.root / "sources.list").write_text("deb http://ports.ubuntu.com/ubuntu-ports noble main\n")
        canonical_down = "Err:5 https://archive.ubuntu.com/ubuntu noble InRelease\n"
        with self.apt((100, AZURE_DOWN), (100, canonical_down), (0, "")), patch("sys.stdout", io.StringIO()):
            APT.update()
        self.assertEqual(len(self.calls), 3)
        self.assertIn("URIs: https://mirrors.edge.kernel.org/ubuntu/\nSuites: noble noble-updates", self.sources())
        self.assertIn("URIs: https://mirrors.edge.kernel.org/ubuntu/\nSuites: noble-security", self.sources())
        self.assertEqual((self.root / "sources.list").read_text(),
                         "deb https://mirrors.ocf.berkeley.edu/ubuntu-ports noble main\n")
        self.assertEqual((self.root / "sources.list.d/microsoft-prod.list").read_text(), THIRD_PARTY)

    def test_other_repository_failures_stay_warnings(self):
        output = ("Err:3 https://packages.microsoft.com/ubuntu/24.04/prod noble InRelease\n"
                  "W: Failed to fetch https://packages.microsoft.com/ubuntu/24.04/prod/dists/noble/InRelease\n")
        with self.apt((0, output)), patch("sys.stdout", io.StringIO()):
            APT.update()
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(self.sources(), DEB822)

    def test_failure_detection_covers_runner_origin_forms(self):
        output = ("Err:1 mirror+file:/etc/apt/apt-mirrors.txt noble InRelease\n"
                  "Err:2 http://ports.ubuntu.com/ubuntu-ports noble InRelease\n"
                  "E: Failed to fetch https://security.ubuntu.com/ubuntu/dists/noble-security/InRelease\n"
                  "Err:4 https://example.test/archive.ubuntu.com noble InRelease\n"
                  "Err:5 https://mirrors.edge.kernel.org/ubuntu noble InRelease\n")
        self.assertEqual(APT.ubuntu_failures(output), [
            "mirror+file:/etc/apt/apt-mirrors.txt", "http://ports.ubuntu.com/ubuntu-ports",
            "https://security.ubuntu.com/ubuntu/dists/noble-security/InRelease",
            "https://mirrors.edge.kernel.org/ubuntu"])

    def test_mirror_list_and_ports_origins_are_rewritten(self):
        (self.root / "sources.list").write_text("deb mirror+file:/etc/apt/apt-mirrors.txt noble main\n"
                                                "deb http://ports.ubuntu.com/ubuntu-ports noble main\n"
                                                "deb http://archive.ubuntu.com/ubuntu-extra noble main\n")
        with patch("sys.stdout", io.StringIO()):
            changed = APT.use_stage(0)
        self.assertEqual(changed, [self.root / "sources.list", self.root / "sources.list.d/ubuntu.sources"])
        self.assertEqual((self.root / "sources.list").read_text(),
                         "deb https://archive.ubuntu.com/ubuntu noble main\n"
                         "deb https://ports.ubuntu.com/ubuntu-ports noble main\n"
                         "deb http://archive.ubuntu.com/ubuntu-extra noble main\n")
        with patch("sys.stdout", io.StringIO()):
            self.assertEqual(APT.use_stage(0), [])

    def test_root_owned_sources_are_written_through_sudo(self):
        path = self.root / "sources.list.d/ubuntu.sources"
        with patch.object(APT.os, "access", return_value=False), self.apt((0, "")):
            APT.write(path, "replacement\n")
        self.assertEqual(self.calls, [["sudo", "tee", str(path)]])
        self.assertEqual(path.read_text(), DEB822)

    def test_persistent_failure_is_bounded_and_reported(self):
        attempts = len(APT.STAGES) + 1
        with self.apt(*[(100, AZURE_DOWN)] * attempts), patch("sys.stdout", io.StringIO()), \
                self.assertRaisesRegex(RuntimeError, f"attempt {attempts}: exit 100, failed http://azure"):
            APT.update()
        self.assertEqual(len(self.calls), attempts)

    def test_install_retries_once_on_the_canonical_archive(self):
        with self.apt((0, ""), (100, AZURE_DOWN), (0, ""), (0, "")), patch("sys.stdout", io.StringIO()):
            APT.main(["install", "--no-recommends", "--", "minisign", "gnupg"])
        self.assertEqual([call[-1] for call in self.calls], ["update", "gnupg", "update", "gnupg"])
        self.assertEqual(self.calls[1][-6:], ["--yes", "--no-install-recommends", "install", "--", "minisign", "gnupg"])
        self.assertNotIn("azure", self.sources())
        with self.apt((0, ""), (100, ""), (0, ""), (100, "")), patch("sys.stdout", io.StringIO()), \
                self.assertRaisesRegex(RuntimeError, "install failed with exit 100"):
            APT.install(["minisign"])
        self.assertNotIn("--no-install-recommends", self.calls[-1])
        self.calls.clear()
        with self.apt((0, ""), (100, "")), patch("sys.stdout", io.StringIO()), self.assertRaises(RuntimeError):
            APT.install(["minisign"])
        self.assertEqual(len(self.calls), 2)

    def test_stages_that_change_nothing_are_skipped(self):
        with patch("sys.stdout", io.StringIO()):
            APT.main(["canonical"])
        with self.apt((100, AZURE_DOWN), (0, "")), patch("sys.stdout", io.StringIO()):
            APT.update()
        self.assertEqual(APT.next_stage, 2)
        self.assertIn("mirrors.edge.kernel.org", self.sources())
        for path in (self.root / "sources.list.d/ubuntu.sources", self.root / "sources.list.d/microsoft-prod.list"):
            path.unlink()
        APT.next_stage = 0
        self.assertFalse(APT.fail_over())
        self.assertEqual(APT.next_stage, len(APT.STAGES))

    def test_commands_refresh_rewrite_and_refuse_option_injection(self):
        with self.apt((0, "")), patch("sys.stdout", io.StringIO()):
            APT.main(["install"])
            APT.main(["canonical"])
        self.assertEqual([call[-1] for call in self.calls], ["update"])
        self.assertNotIn("azure", self.sources())
        with self.apt((0, "")), patch("sys.stdout", io.StringIO()):
            APT.main(["update"])
        for names in (["--allow-unauthenticated"], ["minisign;true"], ["Minisign"]):
            with self.subTest(names=names), self.assertRaises(ValueError):
                APT.install(names)


if __name__ == "__main__":
    unittest.main()
