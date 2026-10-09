"""The real pointer publisher preserves signed bytes and refuses invalid preparation."""
import base64
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import textwrap
import unittest

from update_keys import key_id, load_registry, release_binding


class FeedPublicationTests(unittest.TestCase):
    def test_prepared_publication_is_exact_repeatable_and_refuses_a_bad_binding(self):
        root = Path(__file__).resolve().parents[2]
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            scripts, packaging, commands, store = [work / name for name in ("scripts", "packaging", "commands", "store")]
            for directory in [scripts, packaging, commands, store]:
                directory.mkdir()
            for name in ["release-r2-publish-pointer.sh", "release-validate-updater-manifest.sh", "release_pointer_headers.py", "feed_signature.py", "feed_signing.py", "update_keys.py", "release_semver.py", "semver-compare.py", "updater_signature.py"]:
                shutil.copyfile(root / "scripts" / name, scripts / name)
            # Fix the registry at one open generation independently of future rotations.
            keys = load_registry()
            keys.update(signing_generation=1, embedded_generation=1)
            keys["generations"] = [{**keys["generations"][0], "successor": None, "bridge_version": None}]
            (packaging / "update-keys.json").write_text(json.dumps(keys))
            (packaging / "release-platforms.json").write_text(json.dumps({"schema_version": 1, "platforms": [{"updater_key": "darwin-aarch64", "publication": "public", "updater_extension": "app.tar.gz"}]}))
            manifest = json.loads((root / "lycaon/testdata/release-halt/last-good.json").read_text())
            manifest["update_keys"] = release_binding(keys, "0.1.0")
            pointer = work / "latest-stable-key-1.json"
            pointer.write_text(json.dumps(manifest, indent=2) + "\n")
            current = store / "latest.json"
            current.write_text(json.dumps(manifest, separators=(",", ":")))
            signature = work / "latest-stable-key-1.json.sig"
            signer = key_id(keys["generations"][0]["feed_public_key"])
            def signed(version):
                document = "untrusted comment: fixture\n" + base64.b64encode(b"ED" + signer + bytes(64)).decode() + f"\ntrusted comment: timestamp:1800000000\tfile:{pointer.name}\tversion:{version}\nunused\n"
                return base64.b64encode(document.encode()).decode()
            signature.write_text(signed("0.1.0"))
            (store / "latest.json.sig").write_text("previous signature")
            shim = "#!" + sys.executable + "\n" + textwrap.dedent('''
                import os, pathlib, sys
                command = pathlib.Path(sys.argv[0]).name
                args = sys.argv[1:]
                store = pathlib.Path(os.environ["PW_TEST_STORAGE"])
                if command == "python3":
                    if args and args[0].endswith("release_distribution.py"):
                        assert args[1] == "storage-read", args
                        key = args[args.index("--key") + 1]
                        source = store / pathlib.Path(key).name
                        if source.exists():
                            pathlib.Path(args[args.index("--output") + 1]).write_bytes(source.read_bytes())
                            print(200)
                        else:
                            print(404)
                    else:
                        os.execv(sys.executable, [sys.executable, *args])
                elif command == "bunx":
                    assert args[1:4] == ["r2", "object", "put"], args
                    name = pathlib.Path(args[4]).name
                    (store / name).write_bytes(pathlib.Path(args[args.index("--file") + 1]).read_bytes())
                    with (store / "puts").open("a") as output:
                        output.write(name + "\\n")
                elif command == "curl":
                    url = next(arg for arg in args if arg.startswith("https://"))
                    pathlib.Path(args[args.index("-o") + 1]).write_bytes((store / pathlib.Path(url).name).read_bytes())
                    if "--dump-header" in args:
                        pathlib.Path(args[args.index("--dump-header") + 1]).write_text("HTTP/1.1 200 OK\\r\\nCache-Control: no-cache, no-store, must-revalidate\\r\\n\\r\\n")
                else:
                    raise AssertionError(command)
                ''')
            for command in ["python3", "bunx", "curl"]:
                path = commands / command
                path.write_text(shim)
                path.chmod(0o700)
            env = {key: value for key, value in os.environ.items() if key not in {"FEED_SIGNING_KEYS_JSON", "FEED_TEST_REGISTRY"}}
            env.update(PATH=str(commands) + os.pathsep + env["PATH"], PW_TEST_STORAGE=str(store), R2_BUCKET="test", CLOUDFLARE_ACCOUNT_ID="test", CLOUDFLARE_API_TOKEN="test", DOWNLOAD_BASE_URL="https://downloads.paintedwolf.dev")
            command = ["bash", str(scripts / "release-r2-publish-pointer.sh"), "--file", str(pointer), "--signature", str(signature), "--channel", "stable", "--generation", "1", "--from-version", "0.2.0"]
            result = subprocess.run(command, env=env, capture_output=True, text=True, timeout=60)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(current.read_bytes(), pointer.read_bytes())
            self.assertEqual((store / "latest.json.sig").read_bytes(), signature.read_bytes())
            self.assertEqual((store / "puts").read_text().splitlines(), ["latest.json.sig", "latest.json"])
            (store / "puts").write_text("")
            result = subprocess.run(command, env=env, capture_output=True, text=True, timeout=60)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((store / "puts").read_text().splitlines(), ["latest.json.sig"])
            (store / "puts").write_text("")
            signature.write_text(signed("0.1.1"))
            result = subprocess.run(command, env=env, capture_output=True, text=True, timeout=60)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual((store / "puts").read_text(), "")
            self.assertEqual(current.read_bytes(), pointer.read_bytes())
