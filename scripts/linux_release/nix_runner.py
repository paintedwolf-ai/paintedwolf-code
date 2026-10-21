"""Provision the pinned Nix toolchain and locked inputs before an offline hosted lane."""

from __future__ import annotations

import hashlib
import os
from pathlib import Path
import platform
import subprocess
import tarfile
import tempfile
import urllib.request

VERSION = "2.35.3"
INSTALLERS = {
    "x86_64": "6ce3ce3703fdc241b560cba401c5d338042122fe5b1a81e07bf1a623f259eba0",
    "aarch64": "4a770896faa4d308806d0b4516575e0581443dc817d634b951a219a280554c09",
}
PROFILE = Path("/nix/var/nix/profiles/default/bin")
# Nix builds stay sandboxed; only root may change daemon trust.
DAEMON_CONFIGURATION = "sandbox = true\ntrusted-users = root\n"


def installer_url(machine: str) -> str:
    return f"https://releases.nixos.org/nix/nix-{VERSION}/nix-{VERSION}-{machine}-linux.tar.xz"


def verify(path: Path, machine: str) -> None:
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    if digest != INSTALLERS[machine]:
        raise ValueError(f"Nix {VERSION} installer for {machine} has digest {digest}")


def install_command(directory: Path, machine: str) -> list[str]:
    return [str(directory / f"nix-{VERSION}-{machine}-linux" / "install"), "--daemon", "--yes",
            "--daemon-user-count", "4", "--no-channel-add", "--no-modify-profile",
            "--nix-extra-conf-file", str(directory / "nix.conf")]


def install(environment=os.environ, fetch=urllib.request.urlretrieve, run=subprocess.run) -> Path:
    """Install the pinned multi-user Nix on an ephemeral hosted runner; elsewhere require an existing one."""
    if (PROFILE / "nix").exists():
        return PROFILE
    if environment.get("GITHUB_ACTIONS") != "true" or environment.get("RUNNER_ENVIRONMENT") != "github-hosted":
        raise ValueError("Nix is not installed; only an ephemeral GitHub-hosted runner installs it here")
    machine = platform.machine()
    if platform.system() != "Linux" or machine not in INSTALLERS:
        raise ValueError("the pinned Nix installer supports Linux amd64 and arm64")
    with tempfile.TemporaryDirectory(prefix="nix-installer-") as temporary:
        directory = Path(temporary)
        archive = directory / f"nix-{VERSION}-{machine}-linux.tar.xz"
        fetch(installer_url(machine), archive)
        verify(archive, machine)
        with tarfile.open(archive) as source:
            source.extractall(directory, filter="data")
        (directory / "nix.conf").write_text(DAEMON_CONFIGURATION)
        run(install_command(directory, machine), check=True)
    if environment.get("GITHUB_PATH"):
        with open(environment["GITHUB_PATH"], "a", encoding="utf-8") as path:
            path.write(f"{PROFILE}\n")
    return PROFILE


def prefetch_commands(nix: list[str], root: Path) -> list[list[str]]:
    """Fetch every locked flake input and the non-flake evaluator while the network is available."""
    return [nix + ["flake", "archive", "--no-write-lock-file", f"git+file://{root}"],
            ["nix-instantiate", "--eval", "--expr",
             f"builtins.attrNames (import {root}/packaging/nix/flake-compat.nix {{ }})"]]


def prepare(nix: list[str], root: Path, environment: dict, run=subprocess.run, installer=install) -> int:
    profile = installer(environment)
    environment = {**environment, "PATH": f"{profile}{os.pathsep}{environment.get('PATH', '')}"}
    for command in prefetch_commands(nix, root):
        result = run(command, cwd=root, env=environment, stdout=subprocess.DEVNULL)
        if result.returncode != 0:
            return result.returncode
    return 0
