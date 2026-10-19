"""Install disposable signed repository fixtures into isolated apt and dnf roots."""

from __future__ import annotations

import os
from pathlib import Path
import subprocess

from .openpgp import selection


def restore_owner(root: Path):
    subprocess.run(["sudo", "-n", "chown", "-R", f"{os.getuid()}:{os.getgid()}", "--", str(root)], check=True)
    homes = {path.parent for pattern in ["pubring.kbx", "pubring.gpg"] for path in root.rglob(pattern)}
    for home in homes:
        subprocess.run(["sudo", "-n", "gpgconf", "--homedir", str(home), "--kill", "gpg-agent"], check=True)


def apt(root: Path, repository: str, certificate: Path, catalog: Path, package: str, version: str):
    root.mkdir(exist_ok=True)
    for name in ["etc/apt/apt.conf.d", "etc/apt/sources.list.d", "var/lib/dpkg", "var/lib/apt/lists/partial",
                 "var/cache/apt/archives/partial", "var/log/apt"]:
        (root / name).mkdir(parents=True, exist_ok=True)
    status = root / "var/lib/dpkg/status"
    status.touch(exist_ok=True)
    _, public = selection(catalog, True)
    (root / "etc/apt/sources.list.d/fixture.sources").write_text(
        f"Types: deb\nURIs: {repository}/\nSuites: preview\nComponents: main\nSigned-By: {public}\n")
    config = root / "apt.conf"
    config.write_text(f"""Dir::Etc "{root}/etc/apt";
Dir::State "{root}/var/lib/apt";
Dir::State::status "{status}";
Dir::Cache "{root}/var/cache/apt";
Dir::Log "{root}/var/log/apt";
APT::Sandbox::User "root";
Acquire::https::CaInfo "{certificate}";
DPkg::Options {{ "--root={root}"; "--log={root}/dpkg.log"; }};
""")
    command = ["sudo", "-n", "env", "APT_CONFIG=" + str(config), "apt-get", "--assume-yes",
               "--no-install-recommends"]
    try:
        subprocess.run([*command, "update"], check=True)
        action = ["upgrade"] if (root / "usr/share" / package / "value").exists() else ["install", package]
        subprocess.run([*command, *action], check=True)
    finally:
        restore_owner(root)
    value = subprocess.check_output(["dpkg-query", "--admindir=" + str(root / "var/lib/dpkg"),
                                     "--show", "--showformat=${Package} ${Version}", package], text=True)
    if value != package + " " + version + "-1":
        raise ValueError("isolated apt upgrade selected the wrong package version")
    if (root / "usr/share" / package / "value").read_text() != version + "\n":
        raise ValueError("isolated apt upgrade installed the wrong payload")


def dnf(root: Path, mirrorlist: str, certificate: Path, catalog: Path, package: str, version: str):
    root.mkdir(exist_ok=True)
    for name in ["repos", "vars", "cache", "persist", "logs"]:
        (root / name).mkdir(exist_ok=True)
    _, public = selection(catalog, True)
    config = root / "dnf.conf"
    config.write_text(f"""[main]
plugins=0
install_weak_deps=False
reposdir={root}/repos
varsdir={root}/vars
cachedir={root}/cache
persistdir={root}/persist
logdir={root}/logs
gpgcheck=1
repo_gpgcheck=1
[paintedwolf-fixture]
name=Disposable Painted Wolf Code repository fixture
mirrorlist={mirrorlist}
sslcacert={certificate}
sslverify=1
gpgkey={public.as_uri()}
enabled=1
metadata_expire=0
""")
    action = "upgrade" if (root / "usr/share" / package / "value").exists() else "install"
    command = ["sudo", "-n", "dnf", "--config", str(config), "--installroot", str(root),
               "--releasever=0", "--refresh", "--assumeyes", action, package]
    try:
        subprocess.run(command, check=True)
    finally:
        restore_owner(root)
    value = subprocess.check_output(["sudo", "-n", "rpm", "--root", str(root), "-q", "--qf",
                                     "%{NAME} %{VERSION}-%{RELEASE}", package], text=True)
    if value != package + " " + version + "-1":
        raise ValueError("isolated dnf upgrade selected the wrong package version")
    if (root / "usr/share" / package / "value").read_text() != version + "\n":
        raise ValueError("isolated dnf upgrade installed the wrong payload")
