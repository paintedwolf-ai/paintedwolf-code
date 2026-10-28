"""Pacman rehearsal, installation, remote VM, and assembly CLI contracts."""

from contextlib import ExitStack, contextmanager
from io import StringIO
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release import (
    pacman_installation,
    pacman_rehearsal,
    pacman_repository,
    repository_remote,
)
from scripts.linux_release.signing import digest
from scripts.verification_tests.distribution.pacman_repository_fixtures import (
    PACKAGE,
    inputs,
    manifest,
    package_name,
    trust,
)


@contextmanager
def fake_keyring(root: Path):
    home = root / "gnupg"
    home.mkdir(parents=True, exist_ok=True)
    yield home


class PacmanRepositoryCliAndSignatureTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.inputs = inputs(self.work)
        self.catalog = trust(self.work / "trust")

    def test_binary_signature_and_assemble_build_signed_generation(self):
        subject = self.inputs / package_name("0.0.1")
        sig_out = self.work / "out.sig"
        with patch.object(pacman_repository, "selection", return_value=({"fingerprint": "F" * 40, "generation": 1}, self.work / "pub.asc")), \
             patch.object(pacman_repository, "sign"), \
             patch.object(pacman_repository, "keyring", side_effect=lambda *_a: fake_keyring(self.work)), \
             patch.object(pacman_repository, "run", side_effect=lambda cmd, _h: Path(cmd[cmd.index("--output") + 1]).write_bytes(b"sig")), \
             patch.object(pacman_repository, "verify"):
            pacman_repository.binary_signature(subject, sig_out, self.catalog, self.work / "priv.asc", True)
        self.assertEqual(sig_out.read_bytes(), b"sig")

        dest = self.work / "assembled"
        selected = manifest(self.inputs)

        def fake_repo_add(cmd, **_kwargs):
            db = Path(cmd[3])
            db.write_bytes(b"db")
            db.with_name(db.name.replace(".db.", ".files.")).write_bytes(b"files")

        with patch.object(pacman_repository, "selection", return_value=({"fingerprint": "F" * 40, "generation": 1}, self.work / "pub.asc")), \
             patch.object(pacman_repository, "package_info", return_value={"pkgname": PACKAGE, "pkgver": "0.0.1-1", "arch": "x86_64"}), \
             patch.object(pacman_repository, "binary_signature", side_effect=lambda _s, d, *_a: d.write_bytes(b"sig")), \
             patch.object(pacman_repository.subprocess, "run", side_effect=fake_repo_add):
            receipt = pacman_repository.assemble(selected, self.inputs, dest, "preview", self.catalog,
                                                 self.work / "priv.asc", True, PACKAGE)
        self.assertEqual(receipt["family"], "pacman")
        self.assertTrue((dest / "painted-wolf-code-preview.db.sig").is_symlink())

        manifest_file = self.work / "manifest.json"
        manifest_file.write_text(json.dumps(selected))
        cli_dest = self.work / "cli-dest"
        argv = ["pacman_repository", "--manifest", str(manifest_file), "--inputs", str(self.inputs),
                "--destination", str(cli_dest), "--catalog", str(self.catalog),
                "--private-key", str(self.work / "priv.asc"), "--channel", "preview", "--rehearsal"]
        with patch.object(sys, "argv", argv), \
             patch.object(pacman_repository, "assemble", side_effect= lambda *_a: (cli_dest.mkdir(), {"ok": True})[1]):
            pacman_repository.main()
        self.assertTrue((cli_dest / "generation.json").is_file())
        with patch.object(sys, "argv", argv), \
             patch.object(pacman_repository, "assemble", side_effect=ValueError("bad input")), \
             patch.object(sys, "stderr", new_callable=StringIO):
            with self.assertRaises(SystemExit):
                pacman_repository.main()


class PacmanInstallationContractTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)

    def test_pacman_installation_local_and_repository_modes(self):
        pkg = self.work / "pkg.pkg.tar.zst"
        pkg.write_bytes(b"pkg")
        root = self.work / "root"
        with patch.object(pacman_installation, "selection", return_value=({"fingerprint": "F" * 40}, self.work / "pub.asc")), \
             patch.object(pacman_installation, "keyring", side_effect=lambda *_a: fake_keyring(self.work)), \
             patch.object(pacman_installation, "run"), \
             patch.object(pacman_installation.subprocess, "run"), \
             patch.object(pacman_installation.subprocess, "check_output", return_value=PACKAGE + "\n"):
            pacman_installation.install(pkg, root, self.work / "cat", PACKAGE)
            pacman_installation.install(pkg, root, self.work / "cat", PACKAGE, self.work / "repo")
        with patch.object(pacman_installation, "selection", return_value=({"fingerprint": "F" * 40}, self.work / "pub.asc")), \
             patch.object(pacman_installation, "keyring", side_effect=lambda *_a: fake_keyring(self.work)), \
             patch.object(pacman_installation, "run"), \
             patch.object(pacman_installation.subprocess, "run"), \
             patch.object(pacman_installation.subprocess, "check_output", return_value="other\n"):
            with self.assertRaisesRegex(ValueError, "wrong package owner"):
                pacman_installation.install(pkg, root, self.work / "cat", PACKAGE)



class PacmanRehearsalContractTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)


    def test_repository_remote_transfers_bundle_and_validates_receipt(self):
        receipt = self.work / "remote-receipt.json"
        remote_dir = "/tmp/paintedwolf-repository-check.AbCd1234"
        remote_json = json.dumps({"rehearsal": True, "application_qualified": False}).encode()

        def check_out(cmd, **_kwargs):
            if "mktemp" in cmd[-1]:
                return remote_dir + "\n"
            if cmd[-1].startswith("cat "):
                return remote_json
            if cmd[:2] == ["git", "status"]:
                return ""
            return "f" * 40 + "\n"

        argv = ["repository_remote", "--receipt", str(receipt), "--ssh-host", "host.example",
                "--ssh-port", "22", "--ssh-identity", str(self.work / "id"), "--ssh-known-hosts", str(self.work / "kh")]
        with patch.object(sys, "argv", argv), \
             patch.object(repository_remote.subprocess, "check_output", side_effect=check_out), \
             patch.object(repository_remote.subprocess, "run"), \
             patch.object(sys, "stdout", new_callable=StringIO):
            repository_remote.main()
        self.assertTrue(json.loads(receipt.read_text())["rehearsal"])
        with patch.object(sys, "argv", [*argv[:4], "-bad-host", *argv[5:]]), \
             patch.object(sys, "stderr", new_callable=StringIO):
            with self.assertRaises(SystemExit):
                repository_remote.main()

    def test_pacman_rehearsal_runs_local_and_r2_lifecycle(self):
        with self.assertRaisesRegex(ValueError, "invalid pacman generation"):
            pacman_rehearsal.rejected(lambda: None)

        def fake_ephemeral(path: Path):
            path.mkdir(parents=True, exist_ok=True)
            cat = path / "keys.json"
            cat.write_text(json.dumps({"signing_generation": 1, "generations": [{"generation": 1}]}))
            return cat

        def fake_makepkg(_cmd, cwd=None, **_kwargs):
            ver = Path(cwd).name
            (Path(cwd) / f"{PACKAGE}-{ver}-1-x86_64.pkg.tar.zst").write_bytes(ver.encode())

        def fake_assemble(manifest_doc, _src, dest, *_a):
            dest.mkdir(parents=True, exist_ok=True)
            pkg_file = manifest_doc["packages"][0]["file"]
            (dest / pkg_file).write_bytes(manifest_doc["version"].encode())
            (dest / "painted-wolf-code-preview.db.sig").write_bytes(b"sig")
            return {**manifest_doc, "repository": "painted-wolf-code-preview",
                    "objects": [{"path": pkg_file}], "signer": str(_a[1])}

        def fake_admit(rec, dest, cat, rehearsal=False):
            pkg_path = dest / rec["packages"][0]["file"]
            if (not rehearsal or rec.get("channel") == "stable" or str(cat) != rec["signer"]
                    or not pkg_path.is_file() or pkg_path.read_bytes() != rec["version"].encode()):
                raise ValueError("refused")

        state = {"active": None, "r2_active": None, "withdrawn": False, "r2_withdrawn": False, "tampered": False}

        def fake_publish(rec, _dest, origin, _url, cat, prev, _reh, op="publish"):
            if prev != state["active"] or (op == "publish" and (state["withdrawn"] or "rotation" in str(cat))):
                raise ValueError("publish refused")
            if op == "withdraw":
                state["withdrawn"] = True
            ch = origin / "channels/pacman/preview/x86_64"
            ch.mkdir(parents=True, exist_ok=True)
            (ch / "painted-wolf-code-preview.db.sig").write_bytes(b"sig")
            state["active"] = "gen-" + rec["version"] + "-" + op
            return {"generation": state["active"], "version": rec["version"]}

        def fake_publish_r2(rec, _dest, cat, _pref, _orig, prev, _reh, op="publish", phase="all"):
            if state["tampered"]:
                raise subprocess.CalledProcessError(1, "r2")
            if phase == "stage":
                return {"generation": "staged-" + rec["version"]}
            if prev != state["r2_active"] or (op == "publish" and state["r2_withdrawn"]):
                raise ValueError("r2 refused")
            if op == "withdraw":
                state["r2_withdrawn"] = True
            state["r2_active"] = "r2-" + rec["version"] + "-" + op
            return {"generation": state["r2_active"]}

        def fake_install(pkg_path, root, *_a):
            if any(k in root.name for k in ("mismatched", "wrong-successor")):
                raise subprocess.CalledProcessError(1, "pacman")
            ver = "0.0.3" if ("0.0.3" in pkg_path.name) else ("0.0.2" if "0.0.2" in pkg_path.name else "0.0.1")
            val = root / "usr/share" / PACKAGE / "value"
            val.parent.mkdir(parents=True, exist_ok=True)
            val.write_text(ver + "\n")

        with ExitStack() as stack:
            stack.enter_context(patch.object(pacman_rehearsal.platform, "machine", return_value="x86_64"))
            stack.enter_context(patch.object(pacman_rehearsal, "ephemeral", side_effect=fake_ephemeral))
            stack.enter_context(patch.object(pacman_rehearsal.subprocess, "run", side_effect=fake_makepkg))
            stack.enter_context(patch.object(pacman_rehearsal, "assemble", side_effect=fake_assemble))
            stack.enter_context(patch.object(pacman_rehearsal, "admit", side_effect=fake_admit))
            stack.enter_context(patch.object(pacman_rehearsal, "publish", side_effect=fake_publish))
            stack.enter_context(patch.object(pacman_rehearsal, "publish_r2", side_effect=fake_publish_r2))
            stack.enter_context(patch.object(pacman_rehearsal, "install", side_effect=fake_install))
            stack.enter_context(patch.object(pacman_rehearsal, "warm_absence"))
            stack.enter_context(patch.object(pacman_rehearsal, "put"))
            stack.enter_context(patch.object(pacman_rehearsal, "public_bytes"))
            stack.enter_context(patch.object(pacman_rehearsal, "tamper_fixture_object", side_effect=lambda *_a: state.__setitem__("tampered", True)))
            stack.enter_context(patch.object(pacman_rehearsal, "delete_fixture_object", side_effect=lambda *_a: state.__setitem__("tampered", False)))
            result = pacman_rehearsal.rehearse(self.work / "run", "https://r2.example.invalid")
        self.assertTrue(result["isolated_r2_published"])
        receipt_out = self.work / "rehearsal-receipt.json"
        with patch.object(sys, "argv", ["pacman_rehearsal", "r2", "--receipt", str(receipt_out)]), \
             patch.object(pacman_rehearsal, "rehearsal_environment", return_value="https://r2.example.invalid"), \
             patch.object(pacman_rehearsal, "rehearse", return_value={"cases": []}), \
             patch.object(sys, "stdout", new_callable=StringIO):
            pacman_rehearsal.main()
        self.assertTrue(receipt_out.is_file())


if __name__ == "__main__":
    unittest.main()
