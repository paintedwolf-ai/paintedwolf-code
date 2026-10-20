"""Own the disposable runner's TLS identity and download-origin mapping."""
from __future__ import annotations

from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import platform
import pwd
import subprocess
import sys
import time
import uuid

HOST = "downloads.paintedwolf.dev"
KEYCHAIN = "/Library/Keychains/System.keychain"


def require_disposable_host() -> Path:
    if (platform.system() != "Darwin" or platform.machine() != "arm64"
            or os.environ.get("GITHUB_ACTIONS") != "true"
            or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted"):
        raise ValueError("desktop rehearsal requires a disposable GitHub-hosted arm64 macOS runner")
    actual = Path(pwd.getpwuid(os.getuid()).pw_dir).resolve()
    if Path.home().resolve() != actual:
        raise ValueError("LaunchServices rehearsal must use the account's actual home")
    if any(os.environ.get(name) for name in ("FEED_SIGNING_KEYS_JSON", "TAURI_SIGNING_PRIVATE_KEY", "APPLE_CERTIFICATE")):
        raise ValueError("desktop rehearsal must not receive signing credentials")
    config = actual / ".config/paintedwolf"
    if config.exists() or config.is_symlink():
        raise ValueError("desktop rehearsal requires an absent product store")
    return config


def run(arguments, **kwargs):
    return subprocess.run(arguments, check=True, timeout=60, capture_output=True, **kwargs)


def certificates(work: Path) -> tuple[Path, Path, Path]:
    root, certificate, key = work / "root.pem", work / "server.pem", work / "server.key"
    run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
         "-subj", f"/CN=Upgrade rehearsal {uuid.uuid4()}", "-keyout", str(work / "root.key"), "-out", str(root)])
    run(["openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes", "-subj", f"/CN={HOST}",
         "-keyout", str(key), "-out", str(work / "server.csr")])
    extensions = work / "server.ext"
    extensions.write_text(f"subjectAltName=DNS:{HOST}\nextendedKeyUsage=serverAuth\nbasicConstraints=CA:FALSE\n")
    run(["openssl", "x509", "-req", "-in", str(work / "server.csr"), "-CA", str(root),
         "-CAkey", str(work / "root.key"), "-CAcreateserial", "-days", "1", "-extfile", str(extensions),
         "-out", str(certificate)])
    return root, certificate, key


def change_hosts(line: str, remove: bool) -> None:
    script = """import pathlib, sys
path = pathlib.Path('/etc/hosts')
line = sys.argv[1]
text = path.read_text()
if sys.argv[2] == 'remove':
    if text.count(line) != 1:
        raise SystemExit('owned hosts entry is missing or duplicated')
    path.write_text(text.replace(line, '', 1))
else:
    for entry in text.splitlines():
        if 'downloads.paintedwolf.dev' in entry.split('#', 1)[0].split()[1:]:
            raise SystemExit('download origin already has a hosts entry')
    with path.open('a') as output:
        output.write(line)
"""
    run(["sudo", "-n", sys.executable, "-c", script, line, "remove" if remove else "add"])
    run(["sudo", "-n", "dscacheutil", "-flushcache"])


@contextmanager
def local_origin(root: Path, work: Path, evidence: Path):
    ca, certificate, key = certificates(work)
    der = run(["openssl", "x509", "-in", str(ca), "-outform", "DER"]).stdout
    fingerprint = hashlib.sha1(der).hexdigest()
    line = f"\n127.0.0.1 {HOST} # upgrade-rehearsal-{uuid.uuid4()}\n"
    trusted = mapped = False
    process = None
    try:
        run(["sudo", "-n", "security", "add-trusted-cert", "-d", "-r", "trustRoot", "-k", KEYCHAIN, str(ca)])
        trusted = True
        change_hosts(line, False)
        mapped = True
        ready = work / "server-ready.json"
        with (evidence / "server.log").open("wb") as log:
            process = subprocess.Popen(["sudo", "-n", sys.executable, str(Path(__file__).with_name("bundle_upgrade_server.py")),
                                        "--root", str(root), "--certificate", str(certificate), "--key", str(key),
                                        "--log", str(evidence / "requests.jsonl"), "--ready", str(ready)], stdout=log, stderr=log)
        deadline = time.monotonic() + 30
        while not ready.exists():
            if process.poll() is not None or time.monotonic() >= deadline:
                raise ValueError("local HTTPS fixture did not become ready")
            time.sleep(0.1)
        yield
    finally:
        errors = []
        if process is not None:
            try:
                process.terminate()
                process.wait(timeout=15)
            except (OSError, subprocess.TimeoutExpired) as error:
                errors.append(str(error))
        for needed, action in ((mapped, lambda: change_hosts(line, True)),
                               (trusted, lambda: run(["sudo", "-n", "security", "delete-certificate", "-Z", fingerprint, KEYCHAIN]))):
            if needed:
                try:
                    action()
                except (OSError, subprocess.SubprocessError) as error:
                    errors.append(str(error))
        (evidence / "host-cleanup.json").write_text(json.dumps({"success": not errors, "errors": errors}) + "\n")
        if errors:
            raise ValueError("desktop rehearsal host cleanup failed")
