"""Own the disposable runner's TLS identity and download-origin mapping."""
from __future__ import annotations

from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import socket
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
    import pwd
    actual = Path(pwd.getpwuid(os.getuid()).pw_dir).resolve()
    if Path.home().resolve() != actual:
        raise ValueError("LaunchServices rehearsal must use the account's actual home")
    if any(os.environ.get(name) for name in ("FEED_SIGNING_KEYS_JSON", "TAURI_SIGNING_PRIVATE_KEY", "APPLE_CERTIFICATE")):
        raise ValueError("desktop rehearsal must not receive signing credentials")
    config = actual / ".config/paintedwolf"
    if config.exists() or config.is_symlink():
        raise ValueError("desktop rehearsal requires an absent product store")
    return config


def run(arguments, check=True, **kwargs):
    return subprocess.run(arguments, check=check, timeout=60, capture_output=True, **kwargs)


def certificates(work: Path) -> tuple[Path, Path, Path]:
    root, certificate, key = work / "root.pem", work / "server.pem", work / "server.key"
    ca_config = work / "root.cnf"
    ca_config.write_text("[req]\ndistinguished_name=dn\nx509_extensions=ca\n[dn]\n[ca]\nbasicConstraints=critical,CA:TRUE\nkeyUsage=critical,keyCertSign,cRLSign\n")
    run(["openssl", "req", "-config", str(ca_config), "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
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


@contextmanager
def local_origin(root: Path, work: Path, evidence: Path):
    ca, certificate, key = certificates(work)
    der = run(["openssl", "x509", "-in", str(ca), "-outform", "DER"]).stdout
    fingerprint = hashlib.sha1(der).hexdigest()
    line = f"\n127.0.0.1 {HOST} # upgrade-rehearsal-{uuid.uuid4()}\n"
    trusted = mapped = mocked = False
    previous_mock = None
    process = None
    try:
        previous_mock = run(["launchctl", "getenv", "LYCAON_LLM_MOCK"], check=False)
        if previous_mock.returncode not in (0, 1):
            raise ValueError("cannot inspect the runner launch environment")
        run(["launchctl", "setenv", "LYCAON_LLM_MOCK", "1"])
        mocked = True
        run(["sudo", "-n", "security", "add-trusted-cert", "-d", "-r", "trustRoot", "-k", KEYCHAIN, str(ca)])
        trusted = True
        change_hosts(line, False)
        mapped = True
        run(["sudo", "-n", "dscacheutil", "-flushcache"])
        addresses = {row[4][0] for row in socket.getaddrinfo(HOST, 443, type=socket.SOCK_STREAM)}
        if addresses != {"127.0.0.1"}:
            raise ValueError("download origin did not resolve exclusively to the local fixture")
        (evidence / "origin.json").write_text(json.dumps({"host": HOST, "addresses": sorted(addresses)}) + "\n")
        ready = work / "server-ready.json"
        with (evidence / "server.log").open("wb") as log:
            process = subprocess.Popen(["sudo", "-n", sys.executable, str(Path(__file__).resolve().parent.parent / "release_control.py"), "bundle-serve",
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
            owner = process.pid
            try:
                if process.poll() is None:
                    captured = json.loads(ready.read_text())["pid"] if ready.exists() else process.pid
                    if type(captured) is not int or captured <= 1:
                        raise ValueError("invalid captured HTTPS server PID")
                    owner = captured
                    run(["sudo", "-n", "kill", "-TERM", str(owner)])
                process.wait(timeout=15)
            except (OSError, ValueError, subprocess.SubprocessError) as error:
                errors.append(str(error))
                if process.poll() is None:
                    for pid in dict.fromkeys((owner, process.pid)):
                        try:
                            run(["sudo", "-n", "kill", "-KILL", str(pid)], check=False)
                        except (OSError, subprocess.SubprocessError) as failure:
                            errors.append(str(failure))
                    try:
                        process.wait(timeout=5)
                    except subprocess.TimeoutExpired as failure:
                        errors.append(str(failure))
        for needed, action in ((mapped, lambda: change_hosts(line, True)),
                               (trusted, lambda: run(["sudo", "-n", "security", "remove-trusted-cert", "-d", str(ca)])),
                               (trusted, lambda: run(["sudo", "-n", "security", "delete-certificate", "-Z", fingerprint, KEYCHAIN]))):
            if needed:
                try:
                    action()
                except (OSError, subprocess.SubprocessError) as error:
                    errors.append(str(error))
        if mocked:
            try:
                if previous_mock.returncode == 0:
                    run(["launchctl", "setenv", "LYCAON_LLM_MOCK", previous_mock.stdout.decode().removesuffix("\n")])
                else:
                    run(["launchctl", "unsetenv", "LYCAON_LLM_MOCK"])
            except (OSError, subprocess.SubprocessError) as error:
                errors.append(str(error))
        if mapped:
            try:
                run(["sudo", "-n", "dscacheutil", "-flushcache"])
            except (OSError, subprocess.SubprocessError) as error:
                errors.append(str(error))
        (evidence / "host-cleanup.json").write_text(json.dumps({"success": not errors, "errors": errors}) + "\n")
        if errors:
            raise ValueError("desktop rehearsal host cleanup failed")
