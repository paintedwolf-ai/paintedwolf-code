"""Run the admitted pacman format rehearsal inside an explicitly selected owned VM."""

from __future__ import annotations

import argparse
from pathlib import Path
import shlex
import re
import subprocess
import tarfile
import tempfile


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
                for path in sorted((root / "scripts/linux_release").glob("*.py")):
                    bundle.add(path, arcname="scripts/linux_release/" + path.name)
            archive.seek(0)
            subprocess.run([*ssh, "tar -xzf - -C " + shlex.quote(remote)], stdin=archive, check=True)
        subprocess.run([*ssh, "cd " + shlex.quote(remote)
                        + " && python3 -m scripts.linux_release.pacman_rehearsal --receipt result.json"],
                       check=True)
        raw = subprocess.check_output([*ssh, "cat " + shlex.quote(remote + "/result.json")], timeout=60)
        args.receipt.parent.mkdir(parents=True, exist_ok=True)
        args.receipt.write_bytes(raw)
        print(f"Native VM pacman rehearsal receipt: {args.receipt}")
    except (OSError, ValueError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Native VM pacman rehearsal failed: {exc}\n")
    finally:
        if remote and re.fullmatch(r"/tmp/paintedwolf-repository-check\.[A-Za-z0-9]{8}", remote):
            subprocess.run([*ssh, "rm -rf -- " + shlex.quote(remote)], timeout=60, check=False)


if __name__ == "__main__":
    main()
