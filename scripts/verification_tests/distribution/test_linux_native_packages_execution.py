"""Native package preparation reuses exact archives without changing test dependencies."""
import importlib.util
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / ".github/actions/setup-linux-native/provision.py"
SPEC = importlib.util.spec_from_file_location("native_packages", SCRIPT)
NATIVE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(NATIVE)
PAYLOAD = b"cached archive"
DIGEST = hashlib.sha256(PAYLOAD).hexdigest()
PLAN = f"'http://mirror/pool/a.deb' a_1_amd64.deb {len(PAYLOAD)} SHA256:{DIGEST}\n"


class Response:
    """A fake mirror response: each read returns the next chunk or raises the next error."""

    def __init__(self, reads):
        self.reads = list(reads)

    def read(self, _size):
        if not self.reads:
            return b""
        value = self.reads.pop(0)
        if isinstance(value, Exception):
            raise value
        return value

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False


class LinuxNativePackagesTests(unittest.TestCase):
    def test_hosted_ubuntu_mirrorlist_uses_verified_canonical_archives(self):
        plan = PLAN.replace("http://mirror", "mirror+file:/etc/apt/apt-mirrors.txt")
        row = NATIVE.archives(plan)[0]
        self.assertTrue(row["uri"].startswith("https://archive.ubuntu.com/ubuntu/pool/"))
        self.assertEqual(row["sha256"], DIGEST)

    def test_identity_covers_dependencies_image_architecture_and_requested_packages(self):
        env = {"ImageOS": "ubuntu24", "ImageVersion": "20261022.1", "RUNNER_ARCH": "X64"}
        key = NATIVE.archive_identity(PLAN, ["xclip"], env)
        self.assertEqual(key, NATIVE.archive_identity(PLAN.replace("http://mirror", "http://other"), ["xclip"], env))
        for plan, names, environment in [
            (PLAN.replace(DIGEST, "b" * 64), ["xclip"], env),
            (PLAN.replace("a_1_", "a_2_"), ["xclip"], env),
            (PLAN, ["xclip", "sway"], env),
            (PLAN, ["xclip"], {**env, "ImageVersion": "20261023.1"}),
            (PLAN, ["xclip"], {**env, "RUNNER_ARCH": "ARM64"}),
        ]:
            self.assertNotEqual(key, NATIVE.archive_identity(plan, names, environment))
        for malformed in ["'url' a.deb 42 MD5Sum:abc", "'url' a.deb missing SHA256:abc", "'url' a.deb"]:
            with self.assertRaises(ValueError):
                NATIVE.archive_identity(malformed, ["xclip"], env)

    def test_cold_and_restored_archives_reach_the_same_package_manager_install(self):
        for restored in (False, True):
            with self.subTest(restored=restored), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / "packages.txt").write_text("xclip\nsway\n")
                calls = root / "calls.jsonl"
                sudo = root / "sudo"
                sudo.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
with open(os.environ['NATIVE_CALLS'], 'a') as output:
    output.write(json.dumps(sys.argv[1:]) + '\\n')
if '--print-uris' in sys.argv:
    print(os.environ['NATIVE_PLAN'])
elif 'install' in sys.argv:
    archive = next(a.split('=', 1)[1] for a in sys.argv if a.startswith('Dir::Cache::archives='))
    assert (pathlib.Path(archive) / 'partial').is_dir(), 'APT partial directory is missing'
    marker = pathlib.Path(archive) / 'a_1_amd64.deb'
    if os.environ['NATIVE_RESTORED'] == 'true':
        assert marker.read_bytes() == b'cached archive'
    marker.write_bytes(b'cached archive')
    sys.exit(int(os.environ.get('NATIVE_INSTALL_EXIT', '0')))
''')
                sudo.chmod(0o755)
                env = {**os.environ, "RUNNER_OS": "Linux", "RUNNER_TEMP": str(root),
                       "ImageOS": "ubuntu24", "ImageVersion": "20261022.1", "RUNNER_ARCH": "X64",
                       "GITHUB_OUTPUT": str(root / "output"), "NATIVE_CALLS": str(calls), "NATIVE_PLAN": PLAN,
                       "NATIVE_RESTORED": str(restored).lower(), "PATH": str(root) + os.pathsep + os.environ["PATH"]}
                def transfer(_uri, path, _row):
                    path.write_bytes(PAYLOAD)

                with patch.object(NATIVE, "ROOT", root), patch.dict(os.environ, env), \
                        patch.object(NATIVE.apt_origins, "APT_ROOT", root / "apt"), \
                        patch.object(NATIVE, "transfer", side_effect=transfer) as fetch:
                    NATIVE.main("plan")
                    output = (root / "output").read_text()
                    self.assertIn("key=linux-native-v1-", output)
                    archives = root / "paintedwolf-native-archives"
                    if restored:
                        (archives / "a_1_amd64.deb").write_bytes(b"cached archive")
                    NATIVE.main("install")
                    self.assertEqual(fetch.call_count, 0 if restored else 1)
                    self.assertEqual((archives / "a_1_amd64.deb").read_bytes(), b"cached archive")
                    with patch.dict(os.environ, {"NATIVE_INSTALL_EXIT": "19"}), self.assertRaises(subprocess.CalledProcessError):
                        NATIVE.main("install")
                records = [json.loads(line) for line in calls.read_text().splitlines()]
                self.assertIn("update", records[0])
                self.assertIn("Acquire::ForceHash=SHA256", records[1])
                self.assertIn("APT::Keep-Downloaded-Packages=true", records[2])
                self.assertIn("--no-download", records[2])
                self.assertIn(f"{NATIVE.apt_origins.ATTEMPT_SECONDS}s", records[0])
                self.assertEqual(records[2][-2:], ["xclip", "sway"])

    def test_only_matching_archives_are_reused(self):
        row = NATIVE.archives(PLAN)[0]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / row["name"]
            self.assertFalse(NATIVE.verified(path, row))
            path.write_bytes(PAYLOAD)
            self.assertTrue(NATIVE.verified(path, row))
            self.assertFalse(NATIVE.verified(path, {**row, "sha256": "f" * 64}))
            self.assertFalse(NATIVE.verified(path, {**row, "size": 1}))
            link = Path(directory) / "link.deb"
            link.symlink_to(path)
            self.assertFalse(NATIVE.verified(link, row))

    def test_a_stalled_mirror_falls_back_and_is_named_in_the_report(self):
        plan = PLAN.replace("http://mirror/pool/a.deb", "http://azure.archive.ubuntu.com/ubuntu/pool/a.deb")
        row = NATIVE.archives(plan)[0]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "partial").mkdir()
            calls = []

            def transfer(uri, path, received):
                calls.append(uri)
                self.assertEqual(received, row)
                if len(calls) == 1:
                    path.write_bytes(b"partial")
                    raise NATIVE.Stalled("stalled: no bytes for 60 s after 7 of 14 bytes")
                path.write_bytes(PAYLOAD)

            with patch.object(NATIVE, "transfer", side_effect=transfer), patch("builtins.print") as report:
                self.assertEqual(NATIVE.fetch(row, root), "downloaded")
            self.assertEqual(calls, [row["uri"], "https://archive.ubuntu.com/ubuntu/pool/a.deb"])
            self.assertIn("azure.archive.ubuntu.com: stalled", " ".join(str(call.args[0]) for call in report.call_args_list))
            self.assertTrue(NATIVE.verified(root / row["name"], row))
            self.assertFalse((root / "partial" / row["name"]).exists())
        self.assertEqual(NATIVE.apt_origins.fallback_uris("https://packages.example.test/pool/a.deb"),
                         ["https://packages.example.test/pool/a.deb"])

    def test_single_origin_transient_failure_has_a_bounded_retry(self):
        row = NATIVE.archives(PLAN.replace("http://mirror", "https://mirror"))[0]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "partial").mkdir()
            calls = []

            def transfer(uri, path, received):
                calls.append(uri)
                if len(calls) == 1:
                    raise OSError("truncated response")
                path.write_bytes(PAYLOAD)

            with patch.object(NATIVE, "transfer", side_effect=transfer):
                self.assertEqual(NATIVE.fetch(row, root), "downloaded")
            self.assertEqual(calls, [row["uri"], row["uri"]])
            (root / row["name"]).unlink()
            with patch.object(NATIVE, "transfer", side_effect=NATIVE.Stalled("stalled")) as transfer:
                with self.assertRaisesRegex(ValueError, "mirror: stalled; mirror: stalled"):
                    NATIVE.fetch(row, root)
            self.assertEqual(transfer.call_count, 2)

    def test_download_refuses_corruption_and_oversize(self):
        row = NATIVE.archives(PLAN)[0]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / row["name"]
            for payload, accepted in [(b"wrong", False), (PAYLOAD + b"extra", False),
                                      (b"x" * len(PAYLOAD), False), (PAYLOAD, True)]:
                with self.subTest(payload=payload), patch.object(NATIVE, "open_uri", return_value=Response([payload])):
                    if accepted:
                        NATIVE.transfer(row["uri"], path, row)
                    else:
                        with self.assertRaises(ValueError):
                            NATIVE.transfer(row["uri"], path, row)
            self.assertTrue(NATIVE.verified(path, row))

    def test_a_download_that_keeps_delivering_bytes_never_fails_for_elapsed_time(self):
        row = NATIVE.archives(PLAN)[0]
        hours = iter(range(0, 10 ** 6, 3600))
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(NATIVE, "open_uri", return_value=Response([bytes([b]) for b in PAYLOAD])) as opened, \
                patch.object(NATIVE.time, "monotonic", side_effect=lambda: next(hours)):
            path = Path(directory) / row["name"]
            NATIVE.transfer(row["uri"], path, row)
            self.assertTrue(NATIVE.verified(path, row))
        self.assertEqual(opened.call_args.args, (row["uri"], NATIVE.STALL_SECONDS))

    def test_a_download_that_stops_delivering_bytes_fails_as_stalled(self):
        row = NATIVE.archives(PLAN)[0]
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(NATIVE, "open_uri", return_value=Response([PAYLOAD[:5], TimeoutError("timed out")])):
            with self.assertRaisesRegex(NATIVE.Stalled, f"after 5 of {len(PAYLOAD)} bytes"):
                NATIVE.transfer(row["uri"], Path(directory) / row["name"], row)

    def test_redirects_stay_on_http(self):
        handler = NATIVE.HTTPOnlyRedirects()
        request = NATIVE.urllib.request.Request("https://mirror/pool/a.deb")
        with self.assertRaisesRegex(OSError, "refused"):
            handler.redirect_request(request, None, 302, "Found", {}, "file:///etc/passwd")
        self.assertEqual(handler.redirect_request(request, None, 302, "Found", {}, "https://other/pool/a.deb").full_url,
                         "https://other/pool/a.deb")

    def test_manifest_refuses_unsafe_paths_duplicate_archives_and_credentials(self):
        for plan in (PLAN.replace(" a_1_amd64.deb ", " ../a.deb "), PLAN + PLAN,
                     PLAN.replace("http://mirror", "file:///tmp"),
                     PLAN.replace("http://mirror", "http://user:password@mirror")):
            with self.subTest(plan=plan), self.assertRaises(ValueError):
                NATIVE.archives(plan)

    def test_missing_runner_metadata_has_a_clear_error(self):
        complete = {"ImageOS": "ubuntu24", "ImageVersion": "20261022.1", "RUNNER_ARCH": "X64"}
        for name in complete:
            with self.subTest(name=name):
                incomplete = {key: value for key, value in complete.items() if key != name}
                with self.assertRaisesRegex(ValueError, "GitHub-hosted runner metadata: " + name):
                    NATIVE.archive_identity(PLAN, ["xclip"], incomplete)
                with patch.dict(os.environ, {"RUNNER_OS": "Linux", **incomplete}, clear=True), \
                        patch.object(NATIVE, "apt") as apt, self.assertRaises(ValueError):
                    NATIVE.main("plan")
                apt.assert_not_called()

    def test_invalid_package_names_and_non_linux_hosts_are_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for names in ("", "xclip\nxclip\n", "--allow-unauthenticated\n", "xclip;true\n"):
                (root / "packages.txt").write_text(names)
                with patch.object(NATIVE, "ROOT", root), self.assertRaises(ValueError):
                    NATIVE.packages()
            with patch.dict(os.environ, {"RUNNER_OS": "macOS"}), self.assertRaises(ValueError):
                NATIVE.main("plan")
