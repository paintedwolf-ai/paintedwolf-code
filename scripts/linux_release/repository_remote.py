"""Install disposable signed pacman fixtures in isolated roots and run owned VM rehearsals."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shlex
import subprocess
import tarfile
import tempfile

from .openpgp import keyring, run, selection

PACKAGE = "paintedwolf-repository-fixture"


def install(package: Path, root: Path, catalog: Path, expected: str, repository: Path | str | None = None):
    root.mkdir(exist_ok=True)
    for name in ["db", "cache", "hooks"]:
        (root / name).mkdir(exist_ok=True)
    config = root / "pacman.conf"
    config.write_text("[options]\nArchitecture = " + platform.machine()
                      + "\nSigLevel = Required DatabaseRequired\nLocalFileSigLevel = Required\n")
    if repository is not None:
        with config.open("a") as output:
            url = repository.as_uri() if isinstance(repository, Path) else repository
            output.write("\n[painted-wolf-code-preview]\nServer = " + url + "\n")
    selected, public = selection(catalog, True)
    with keyring(public, selected["fingerprint"]) as home:
        run(["gpg", "--batch", "--import-ownertrust"], home,
            (selected["fingerprint"] + ":6:\n").encode())
        run(["gpg", "--batch", "--check-trustdb"], home)
        action = ["-U", "--noconfirm", str(package)]
        if repository is not None:
            action = ["-Syu", "--noconfirm"]
            if not (root / "usr/share" / PACKAGE / "value").exists():
                action.append(expected)
        try:
            subprocess.run(["sudo", "-n", "pacman", "--config", str(config), "--root", str(root),
                            "--dbpath", str(root / "db"), "--cachedir", str(root / "cache"),
                            "--hookdir", str(root / "hooks"), "--gpgdir", str(home),
                            "--logfile", str(root / "pacman.log"), *action], check=True)
        finally:
            subprocess.run(["sudo", "-n", "chown", "-R", f"{os.getuid()}:{os.getgid()}", "--", str(root)], check=True)
    query = root / "query.conf"
    query.write_text("[options]\nArchitecture = " + platform.machine() + "\n")
    owned = subprocess.check_output(["pacman", "--config", str(query), "--root", str(root), "--dbpath", str(root / "db"),
                                     "-Qoq", "--", str(root / "usr/share" / PACKAGE / "value")], text=True).strip()
    if owned != expected:
        raise ValueError("installed fixture file has the wrong package owner")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--ssh-host", required=True)
    parser.add_argument("--ssh-port", type=int, required=True)
    parser.add_argument("--ssh-identity", type=Path, required=True)
    parser.add_argument("--ssh-known-hosts", type=Path, required=True)
    args = parser.parse_args()
    if args.ssh_host.startswith("-") or not 1 <= args.ssh_port <= 65535:
        parser.error("select an explicit SSH host and port")
    ssh = ["ssh", "-i", str(args.ssh_identity), "-p", str(args.ssh_port), "-o", "BatchMode=yes",
           "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=30", "-o",
           "UserKnownHostsFile=" + str(args.ssh_known_hosts), args.ssh_host]
    remote = None
    try:
        remote = subprocess.check_output([*ssh, "mktemp -d /tmp/paintedwolf-repository-check.XXXXXXXX"],
                                         text=True, timeout=60).strip()
        if not re.fullmatch(r"/tmp/paintedwolf-repository-check\.[A-Za-z0-9]{8}", remote):
            raise ValueError("remote temporary directory has an unexpected identity")
        root = Path(__file__).resolve().parents[2]
        with tempfile.TemporaryFile() as archive:
            with tarfile.open(fileobj=archive, mode="w:gz") as bundle:
                bundle.add(root / "scripts/release_semver.py", arcname="scripts/release_semver.py")
                for path in sorted((root / "scripts/linux_release").glob("*.py")):
                    bundle.add(path, arcname="scripts/linux_release/" + path.name)
            archive.seek(0)
            bundle_sha256 = hashlib.sha256(archive.read()).hexdigest()
            archive.seek(0)
            subprocess.run([*ssh, "tar -xzf - -C " + shlex.quote(remote)], stdin=archive, check=True)
        subprocess.run([*ssh, "cd " + shlex.quote(remote)
                        + " && python3 -m scripts.linux_release.pacman_rehearsal fixtures --receipt result.json"],
                       check=True)
        raw = subprocess.check_output([*ssh, "cat " + shlex.quote(remote + "/result.json")], timeout=60)
        result = json.loads(raw)
        if result.get("rehearsal") is not True or result.get("application_qualified") is not False:
            raise ValueError("remote receipt differs from the selected fixture scope")
        result["module_bundle_sha256"] = bundle_sha256
        result["verification_source_commit"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
        result["verification_source_dirty"] = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=root))
        result["verification_source_tree"] = subprocess.check_output(["git", "rev-parse", "HEAD^{tree}"], cwd=root, text=True).strip()
        args.receipt.parent.mkdir(parents=True, exist_ok=True)
        args.receipt.write_text(json.dumps(result, indent=2) + "\n")
        print(f"Native VM pacman rehearsal receipt: {args.receipt}")
    except (OSError, ValueError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Native VM pacman rehearsal failed: {exc}\n")
    finally:
        if remote and re.fullmatch(r"/tmp/paintedwolf-repository-check\.[A-Za-z0-9]{8}", remote):
            subprocess.run([*ssh, "rm -rf -- " + shlex.quote(remote)], timeout=60, check=False)


if __name__ == "__main__":
    main()
