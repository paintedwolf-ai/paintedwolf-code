"""Public Linux platforms are signed, collected and published by the shared release lane."""

import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release import release_repositories, release_signing, repository_withdrawal
from scripts.linux_release.release_repositories import ReleaseAuthority, assemble, plan, publish
from scripts.linux_release.signing import CATALOG

ROOT = Path(__file__).resolve().parents[3]
VERSION = "1.2.3"
SOURCE = "a" * 40
LINUX = {"linux-x86_64": ("amd64", "x86_64"), "linux-aarch64": ("arm64", "aarch64")}


def sha(raw: bytes) -> str:
    return hashlib.sha256(raw).hexdigest()


def catalog(directory: Path, linux="public") -> Path:
    value = json.loads((ROOT / "packaging/release-platforms.json").read_text())
    for row in value["platforms"]:
        if row["goos"] == "linux":
            row["publication"] = linux
    path = directory / "release-platforms.json"
    path.write_text(json.dumps(value))
    return path


def offer_name(receipt: dict) -> str:
    return receipt["family"] + ("-" + receipt["architecture"] if receipt["family"] == "pacman" else "")


def public_artifacts(directory: Path) -> Path:
    directory.mkdir()
    for platform in LINUX:
        for extension in ["deb", "rpm", "pkg.tar.zst"]:
            path = directory / f"painted-wolf-code_v{VERSION}_{platform}.{extension}"
            path.write_bytes(f"{platform}.{extension}".encode())
            (directory / (path.name + ".sha256")).write_text(sha(path.read_bytes()) + "\n")
    return directory


class LinuxReleaseSigningTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.stem = f"painted-wolf-code_v{VERSION}_linux-x86_64"
        for extension in ["deb", "rpm", "pkg.tar.zst"]:
            path = self.directory / f"{self.stem}.{extension}"
            path.write_bytes(extension.encode())
            (self.directory / (path.name + ".sha256")).write_text(sha(path.read_bytes()) + "\n")
        (self.directory / "candidate-packages.json").write_text("{}\n")

    def sign(self, payload=lambda path: ("painted-wolf-code 1.2.3-1 x86_64", "payload"), rehearsal=False):
        modes = []

        def rpm(kind, subject, destination, catalog, private, mode):
            modes.append(mode)
            destination.write_bytes(subject.read_bytes() + b" signed header")
            return {"kind": kind}

        def detached(subject, destination, catalog, private, mode):
            modes.append(mode)
            destination.write_bytes(b"signature of " + subject.read_bytes())
        with patch.object(release_signing, "sign", side_effect=rpm), \
                patch.object(release_signing, "binary_signature", side_effect=detached):
            result = release_signing.sign_staged(self.directory, "linux-x86_64", "x86_64", VERSION, SOURCE,
                                                 private=self.directory / "key", payload=payload, rehearsal=rehearsal)
        self.assertEqual(modes, [rehearsal] * 3)
        return result

    def test_final_signed_bytes_rebind_checksums_and_inventory(self):
        result = self.sign()
        rpm = self.directory / f"{self.stem}.rpm"
        self.assertEqual(rpm.read_bytes(), b"rpm signed header")
        for extension in ["deb", "rpm", "pkg.tar.zst"]:
            path = self.directory / f"{self.stem}.{extension}"
            self.assertEqual((self.directory / (path.name + ".sha256")).read_text().strip(), sha(path.read_bytes()))
            self.assertEqual(result["packages"][path.name], sha(path.read_bytes()))
        self.assertTrue((self.directory / f"{self.stem}.deb.sig").is_file())
        self.assertTrue((self.directory / f"{self.stem}.pkg.tar.zst.sig").is_file())
        self.assertFalse((self.directory / f"{self.stem}.rpm.sig").exists())
        inventory = json.loads((self.directory / "candidate-packages.json").read_text())
        self.assertEqual({row["file"]: row["sha256"] for row in inventory["packages"]}, result["packages"])
        self.assertEqual(inventory["source_commit"], SOURCE)

    def test_signing_refuses_payload_changes_incomplete_and_resigned_sets(self):
        observed = iter([("identity", "audited"), ("identity", "changed")])
        with self.assertRaisesRegex(ValueError, "audited package"):
            self.sign(payload=lambda path: next(observed))
        self.assertFalse((self.directory / f"{self.stem}.deb.sig").exists())
        self.sign()
        with self.assertRaisesRegex(ValueError, "already signed"):
            self.sign()
        (self.directory / f"{self.stem}.deb.sig").unlink()
        (self.directory / f"{self.stem}.pkg.tar.zst.sha256").unlink()
        with self.assertRaisesRegex(ValueError, "incomplete"):
            self.sign()

    def test_explicit_rehearsal_signs_every_package_with_disposable_trust(self):
        self.sign(rehearsal=True)
        self.assertTrue((self.directory / f"{self.stem}.deb.sig").is_file())
        for mode in (None, 1, "true"):
            with self.subTest(mode=mode), self.assertRaisesRegex(ValueError, "explicit trust mode"):
                release_signing.sign_staged(self.directory, "linux-x86_64", "x86_64", VERSION, SOURCE, rehearsal=mode)

    def test_private_key_is_owner_only_and_consumed_once(self):
        with patch.dict(os.environ, {"LINUX_PACKAGE_SIGNING_KEY": "private material"}):
            with release_signing.private_key() as path:
                self.assertEqual(path.read_text(), "private material")
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
                self.assertNotIn("LINUX_PACKAGE_SIGNING_KEY", os.environ)
            self.assertFalse(path.exists())
        with patch.dict(os.environ, {}, clear=True), self.assertRaisesRegex(ValueError, "LINUX_PACKAGE_SIGNING_KEY"):
            with release_signing.private_key():
                pass


class LinuxReleaseRepositoryTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.platforms = catalog(self.work)
        self.artifacts = public_artifacts(self.work / "public")
        patcher = patch.object(release_repositories, "PLATFORMS", self.platforms)
        patcher.start()
        self.addCleanup(patcher.stop)

    def plan(self):
        return plan(self.artifacts, VERSION, SOURCE, read_version=lambda family, path: "1.2.3-1")

    def test_every_public_linux_package_enters_exactly_one_repository_generation(self):
        rows = self.plan()
        generations = {row["name"]: row["manifest"] for row in rows if not row["name"].endswith("-withdrawal")}
        self.assertEqual(sorted(generations), ["deb", "pacman-aarch64", "pacman-x86_64", "rpm"])
        # Every offer carries the signed empty generation that withdraws it on the same channel.
        withdrawals = {row["name"]: row for row in rows if row["name"].endswith("-withdrawal")}
        self.assertEqual(sorted(withdrawals), sorted(name + "-withdrawal" for name in generations))
        for name, manifest in generations.items():
            empty = withdrawals[name + "-withdrawal"]
            self.assertEqual(empty["manifest"], {**manifest, "packages": []})
            expected = None if empty["family"] == "pacman" else ["amd64", "arm64"]
            self.assertEqual(empty["architectures"], expected)
        selected = sorted(row["file"] for manifest in generations.values() for row in manifest["packages"])
        public = sorted(path.name for path in self.artifacts.iterdir() if not path.name.endswith(".sha256"))
        self.assertEqual(selected, public)
        self.assertEqual({row["architecture"] for row in generations["deb"]["packages"]}, {"amd64", "arm64"})
        self.assertEqual(generations["pacman-aarch64"]["architecture"], "aarch64")
        self.assertTrue(all(m["source_commit"] == SOURCE and m["version"] == VERSION for m in generations.values()))

    def test_halt_withdrawal_covers_every_published_repository_channel(self):
        published = {release_repositories.channel_key({**row["manifest"], "family": row["family"], "channel": channel})
                     for row in self.plan() for channel in ["stable", "preview"]}
        self.assertEqual(published, {repository_withdrawal.state_key(*row) for row in repository_withdrawal.CHANNELS})
        # Each retained empty generation sits where a halt of that channel looks for it.
        retained = {release_repositories.withdrawal_key({**row["manifest"], "family": row["family"], "channel": channel})
                    for row in self.plan() if row["name"].endswith("-withdrawal") for channel in ["stable", "preview"]}
        looked_up = {release_repositories.withdrawal_key({"family": family, "channel": channel, "version": VERSION,
                                                          **({"architecture": arch} if arch else {})})
                     for family, channel, arch in repository_withdrawal.CHANNELS}
        self.assertEqual(retained, looked_up)

    def test_candidate_linux_contributes_no_repository(self):
        self.assertEqual(plan(self.artifacts, VERSION, SOURCE, catalog(self.work, "candidate")), [])

    def test_checksum_and_presence_bind_the_public_package_set(self):
        name = f"painted-wolf-code_v{VERSION}_linux-aarch64.rpm"
        (self.artifacts / name).write_bytes(b"changed after checksum")
        with self.assertRaisesRegex(ValueError, "published checksum"):
            self.plan()
        (self.artifacts / name).unlink()
        with self.assertRaisesRegex(ValueError, "missing"):
            self.plan()

    def test_assembly_signs_each_selected_family_with_the_release_key(self):
        calls = []

        def native(family, manifest, inputs, destination, channel, catalog, private, architectures=None, rehearsal=False):
            if architectures is not None:
                self.assertEqual((manifest["packages"], architectures), ([], ["amd64", "arm64"]))
                destination.mkdir()
                return {"family": family, "withdrawal": True}
            calls.append((family, channel, catalog, private))
            destination.mkdir()
            return {"family": family}

        def pacman(manifest, inputs, destination, channel, catalog, private, rehearsal=False):
            if not manifest["packages"]:
                destination.mkdir()
                return {"family": "pacman", "withdrawal": True}
            calls.append(("pacman", channel, catalog, private))
            destination.mkdir()
            return {"family": "pacman", "architecture": manifest["architecture"]}
        # Each assembly host reads only its own package format.
        read = []
        with patch.object(release_repositories, "native_version", side_effect=lambda family, path: read.append(family) or "1.2.3-1"), \
                patch.object(release_repositories.native_repositories, "assemble", side_effect=native), \
                patch.object(release_repositories.pacman_repository, "assemble", side_effect=pacman):
            assemble({"deb", "rpm"}, self.artifacts, self.work / "native", VERSION, SOURCE, ["stable"], Path("key"))
            self.assertEqual(sorted(set(read)), ["deb", "rpm"])
            read.clear()
            assemble({"pacman"}, self.artifacts, self.work / "pacman", VERSION, SOURCE, ["stable"], Path("key"))
            self.assertEqual(set(read), {"pacman"})
        self.assertEqual(calls, [("deb", "stable", CATALOG, Path("key")), ("rpm", "stable", CATALOG, Path("key")),
                                 ("pacman", "stable", CATALOG, Path("key")), ("pacman", "stable", CATALOG, Path("key"))])
        index = json.loads((self.work / "pacman/generations.json").read_text())
        self.assertEqual(index, [{"name": "stable/pacman-aarch64", "family": "pacman"},
                                 {"name": "stable/pacman-x86_64", "family": "pacman"},
                                 {"name": "stable/pacman-aarch64-withdrawal", "family": "pacman"},
                                 {"name": "stable/pacman-x86_64-withdrawal", "family": "pacman"}])
        self.assertEqual(json.loads((self.work / "native/stable/rpm/generation.json").read_text()), {"family": "rpm"})

    def generations(self, **changes):
        roots = [self.work / "native", self.work / "pacman"]
        for root in roots:
            root.mkdir(exist_ok=True)
        indexes = {root: [] for root in roots}
        for row in self.plan():
            receipt = {**row["manifest"], "family": row["family"], "channel": "stable", "rehearsal": False, **changes}
            receipt["packages"] = [{"file": "pool/x/" + item["file"] if row["family"] != "pacman" else item["file"],
                                    "sha256": item["sha256"]} for item in row["manifest"]["packages"]]
            root = roots[row["family"] == "pacman"]
            (root / "stable" / row["name"]).mkdir(exist_ok=True, parents=True)
            (root / "stable" / row["name"] / "generation.json").write_text(json.dumps(receipt))
            indexes[root].append({"name": "stable/" + row["name"], "family": row["family"]})
        for root, rows in indexes.items():
            (root / "generations.json").write_text(json.dumps(rows))
        return roots

    def publish(self, roots, qualified, active=None):
        published, reads, self.retained = [], [], []

        def retain(receipt, offer, directory, base_url, qualified, **configuration):
            self.assertEqual((receipt["packages"], directory.name), ([], offer_name(offer) + "-withdrawal"))
            self.assertFalse(any(item[:2] == (offer["family"], offer.get("architecture")) for item in published))
            self.retained.append(directory.name)

        def publisher(receipt, directory, catalog, prefix, base_url, expected, rehearsal, operation, phase, *, authorize):
            request = {"manifest": receipt, "directory": directory, "catalog": catalog, "storage_prefix": prefix,
                       "base_url": base_url, "expected": expected, "operation": operation, "phase": phase}
            self.assertFalse(rehearsal)
            # The real publishers authorize before credentials and again before activation.
            self.assertIs(authorize(request), True)
            self.assertIs(authorize(request), True)
            published.append((receipt["family"], receipt.get("architecture"), expected))
            return {"generation": directory.name}

        def read(key, directory):
            reads.append(key)
            return active
        result = publish(roots, self.artifacts, VERSION, SOURCE, ["stable"], "https://downloads.paintedwolf.dev", qualified,
                         read_current=read, publishers={"deb": publisher, "rpm": publisher, "pacman": publisher},
                         retain=retain)
        return result, published, reads

    def test_publication_rereads_qualification_and_binds_final_digests(self):
        qualified = []
        result, published, reads = self.publish(self.generations(), qualified.append, {"generation": "b" * 64})
        self.assertEqual(len(result), 4)
        self.assertEqual(sorted(self.retained), ["deb-withdrawal", "pacman-aarch64-withdrawal",
                                                 "pacman-x86_64-withdrawal", "rpm-withdrawal"])
        self.assertEqual(qualified, [SOURCE] * 8)
        self.assertEqual(sorted(published), [("deb", None, "b" * 64), ("pacman", "aarch64", "b" * 64),
                                             ("pacman", "x86_64", "b" * 64), ("rpm", None, "b" * 64)])
        self.assertIn("linux/channels/pacman/stable/aarch64/current.json", reads)
        self.assertIn("linux/channels/deb/stable/current.json", reads)

    def test_publication_refuses_unqualified_or_unbound_generations(self):
        def unqualified(_source):
            raise ValueError("no passing exact-commit qualification")
        with self.assertRaisesRegex(ValueError, "qualification"):
            self.publish(self.generations(), unqualified)
        for changes in [{"rehearsal": True}, {"version": "1.2.4"}, {"source_commit": "b" * 40}, {"channel": "preview"}]:
            with self.subTest(changes=changes), self.assertRaisesRegex(ValueError, "selected release"):
                self.publish(self.generations(**changes), lambda _source: None)
        roots = self.generations()
        receipt = json.loads((roots[1] / "stable/pacman-x86_64/generation.json").read_text())
        receipt["packages"][0]["sha256"] = "c" * 64
        (roots[1] / "stable/pacman-x86_64/generation.json").write_text(json.dumps(receipt))
        with self.assertRaisesRegex(ValueError, "signed public packages"):
            self.publish(roots, lambda _source: None)
        roots = self.generations()
        (roots[1] / "generations.json").write_text(json.dumps([{"name": "stable/pacman-x86_64", "family": "pacman"}]))
        with self.assertRaisesRegex(ValueError, "catalog-public"):
            self.publish(roots, lambda _source: None)

    def test_authority_refuses_other_targets_and_operations(self):
        authority = ReleaseAuthority(self.artifacts, VERSION, SOURCE, "stable", "https://downloads.paintedwolf.dev",
                                     lambda _source: None, self.platforms)
        manifest = {**self.plan()[0]["manifest"], "family": "deb", "channel": "stable", "rehearsal": False}
        request = {"manifest": manifest, "catalog": CATALOG, "storage_prefix": "linux",
                   "base_url": "https://downloads.paintedwolf.dev", "operation": "publish"}
        self.assertTrue(authority(request))
        for changes in [{"operation": "withdraw"}, {"storage_prefix": "release-system-tests/x"},
                        {"base_url": "https://other.example.invalid"}, {"catalog": Path("rehearsal.json")}]:
            with self.subTest(changes=changes), self.assertRaisesRegex(ValueError, "production target"):
                authority({**request, **changes})


class PublicLinuxCollectionTests(unittest.TestCase):
    def test_collection_requires_every_package_checksum_and_detached_signature(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            platforms = catalog(work)
            source = work / "input"
            source.mkdir()
            (source / "opengrep").mkdir()
            for row in json.loads(platforms.read_text())["platforms"]:
                if row["publication"] != "public":
                    continue
                stem = f"painted-wolf-code_v{VERSION}_{row['updater_key']}"
                names = [f"{stem}.{extension}{suffix}" for extension in row["package_extensions"]
                         for suffix in ["", ".sha256"]]
                if row["updater_extension"]:
                    names += [f"{stem}.{row['updater_extension']}", f"{stem}.{row['updater_extension']}.sig"]
                if row["goos"] == "linux":
                    names += [f"{stem}.deb.sig", f"{stem}.pkg.tar.zst.sig"]
                for name in names:
                    (source / name).write_text(name)
                (source / "opengrep" / f"{stem}.json").write_text("{}")

            def collect(output):
                return subprocess.run([sys.executable, str(ROOT / "scripts/release-collect-public-artifacts.py"),
                                       "--catalog", str(platforms), "--input", str(source), "--output", str(output),
                                       "--version", VERSION], capture_output=True, text=True,
                                      env={**os.environ, "PYTHONPATH": str(ROOT / "scripts")})
            result = collect(work / "complete")
            self.assertEqual(result.returncode, 0, result.stderr)
            collected = {path.name for path in (work / "complete").iterdir()}
            for platform in LINUX:
                self.assertIn(f"painted-wolf-code_v{VERSION}_{platform}.pkg.tar.zst.sig", collected)
                self.assertIn(f"painted-wolf-code_v{VERSION}_{platform}.deb.sig", collected)
                self.assertNotIn(f"painted-wolf-code_v{VERSION}_{platform}.rpm.sig", collected)
            (source / f"painted-wolf-code_v{VERSION}_linux-aarch64.deb.sig").unlink()
            result = collect(work / "unsigned")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("linux-aarch64.deb.sig", result.stderr)


if __name__ == "__main__":
    unittest.main()
