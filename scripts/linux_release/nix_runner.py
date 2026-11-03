"""Provision the pinned Nix toolchain, locked inputs, and build progress tracking."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shlex
import subprocess
import sys
import tarfile
import tempfile
import threading
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
SYSTEMS = {"x86_64": "x86_64-linux", "aarch64": "aarch64-linux"}
NIX = ["nix", "--extra-experimental-features", "nix-command flakes"]
CREDENTIALS = ("GH_TOKEN", "GITHUB_TOKEN", "LINUX_PACKAGE_SIGNING_KEY", "FEED_SIGNING_KEYS_JSON",
               "TAURI_SIGNING_PRIVATE_KEY", "TAURI_SIGNING_PRIVATE_KEY_PASSWORD")
FIELDS = ("bash", "coreutils", "stdenv")
STORE_TOOL = re.compile(r"/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[^\s]+\Z")
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
        # The verified installer carries absolute store links, so only member paths are confined.
        with tarfile.open(archive) as source:
            source.extractall(directory, filter="tar")
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


def environment_from(fixture: dict) -> dict:
    if fixture.get("schema") != 1 or set(fixture) != {"schema", *FIELDS}:
        raise ValueError("invalid locked Nix workflow fixture")
    values = {}
    for field in FIELDS:
        value = fixture[field]
        if not isinstance(value, str) or not STORE_TOOL.fullmatch(value):
            raise ValueError("invalid locked Nix workflow path")
        values["PW_NIX_FIXTURE_" + field.upper()] = value
    return values


def build(root: Path = ROOT, run=subprocess.run) -> dict:
    """Realize the fixture from this checkout's lock and return the suites' environment."""
    if platform.system() != "Linux" or platform.machine() not in SYSTEMS:
        raise ValueError("the Nix workflow fixture builds on native Linux amd64 or arm64")
    target = f"git+file://{root}#packages.{SYSTEMS[platform.machine()]}.nix-workflow-fixture"
    environment = {name: value for name, value in os.environ.items() if name not in CREDENTIALS}
    result = run(NIX + ["build", "--json", "--no-link", "--no-write-lock-file", target],
                 cwd=root, env=environment, text=True, stdout=subprocess.PIPE)
    if result.returncode != 0:
        raise ValueError("the locked Nix workflow fixture did not build")
    outputs = [path for item in json.loads(result.stdout) for path in item["outputs"].values()]
    if len(outputs) != 1:
        raise ValueError("the Nix workflow fixture has exactly one output")
    return environment_from(json.loads(Path(outputs[0]).read_text()))


def run(command, *, cwd, env):
    destination = env.get("PW_TEST_STAGE_PROGRESS")
    if not destination:
        return subprocess.run(command, cwd=cwd, env=env, text=True, stdout=subprocess.PIPE)
    # Nix's stdout remains its machine-readable result; stderr is the build log.
    with Path(destination).open("ab", buffering=0) as progress:
        with subprocess.Popen(command, cwd=cwd, env=env, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE) as process:
            errors = []

            def copy_log():
                while chunk := process.stderr.read1(64 << 10):
                    if errors:
                        continue
                    try:
                        progress.write(chunk)
                        sys.stderr.write(chunk.decode("utf-8", errors="replace"))
                        sys.stderr.flush()
                    except (OSError, ValueError) as error:
                        errors.append(error)

            reader = threading.Thread(target=copy_log)
            reader.start()
            try:
                output = process.stdout.read()
                status = process.wait()
            finally:
                reader.join()
            if errors:
                raise errors[0]
    return subprocess.CompletedProcess(command, status, output.decode("utf-8"))


def main() -> int:
    try:
        values = build()
    except (OSError, ValueError, KeyError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    for name, value in values.items():
        print(f"export {name}={shlex.quote(value)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
