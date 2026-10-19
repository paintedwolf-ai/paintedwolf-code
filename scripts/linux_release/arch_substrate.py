"""Run makepkg on matching native CPUs in authenticated official distribution roots."""

import hashlib
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import tempfile
import urllib.request
import uuid


def command(*arguments, **options):
    return subprocess.check_output(list(arguments), text=True, **options).strip()


def download(url, destination, limit):
    with urllib.request.urlopen(url, timeout=60) as source, destination.open("xb") as output:
        total = 0
        while block := source.read(1 << 20):
            total += len(block)
            if total > limit:
                raise ValueError("official packaging substrate exceeds its size bound")
            output.write(block)


def rootfs(root, row):
    cache = Path(os.environ.get("XDG_CACHE_HOME", str(Path.home() / ".cache"))) / "paintedwolf/arch-substrates"
    cache.mkdir(parents=True, exist_ok=True)
    selected = cache / (row["rootfs_sha256"] + ".tar.gz")
    if not selected.exists():
        with tempfile.TemporaryDirectory(prefix=".download-", dir=cache) as temporary:
            archive = Path(temporary) / "rootfs.tar.gz"
            download(row["rootfs_url"], archive, 2 << 30)
            with archive.open("rb") as source:
                measured = hashlib.file_digest(source, "sha256").hexdigest()
            if measured != row["rootfs_sha256"]:
                raise ValueError("official Arch Linux ARM rootfs differs from its reviewed pin")
            archive.replace(selected)
    with selected.open("rb") as source:
        if selected.is_symlink() or hashlib.file_digest(source, "sha256").hexdigest() != row["rootfs_sha256"]:
            raise ValueError("cached Arch Linux ARM substrate differs from its reviewed pin")
    with tempfile.TemporaryDirectory(prefix="paintedwolf-arch-root-trust-") as temporary:
        home = Path(temporary)
        home.chmod(0o700)
        public = root / "packaging/linux/arch" / row["key_file"]
        records = command("gpg", "--homedir", str(home), "--show-keys", "--with-colons", str(public))
        fingerprints = [line.split(":")[9] for line in records.splitlines() if line.startswith("fpr:")]
        if not fingerprints or fingerprints[0] != row["key_fingerprint"]:
            raise ValueError("Arch Linux ARM substrate key differs from its reviewed fingerprint")
        signature = home / "rootfs.sig"
        download(row["signature_url"], signature, 16384)
        keyring = home / "trusted.gpg"
        command("gpg", "--homedir", str(home), "--batch", "--dearmor", "--output", str(keyring), str(public))
        status = command("gpgv", "--homedir", str(home), "--status-fd", "1", "--keyring", str(keyring),
                         str(signature), str(selected))
        valid = [line.split()[2] for line in status.splitlines() if line.startswith("[GNUPG:] VALIDSIG ")]
        if valid != [row["key_fingerprint"]]:
            raise ValueError("Arch Linux ARM rootfs signature differs from the reviewed producer")
    return selected


def build(root, work, architecture, epoch):
    if platform.system() != "Linux" or platform.machine() != architecture:
        raise ValueError("pacman substrate requires its matching native Linux CPU")
    catalog = json.loads((root / "packaging/linux/arch/substrates.json").read_text())
    if catalog.get("format_version") != 1 or architecture not in {"x86_64", "aarch64"}:
        raise ValueError("unsupported pacman substrate catalog")
    row = catalog[architecture]
    expected_platform = {"x86_64": "linux/amd64", "aarch64": "linux/arm64"}[architecture]
    if row["platform"] != expected_platform:
        raise ValueError("pacman substrate platform differs from its native CPU")
    image = row.get("image")
    imported = image is None
    unique = "paintedwolf-pacman-" + uuid.uuid4().hex
    prepared, container = unique + ":prepared", None
    try:
        if imported:
            image = unique + ":rootfs"
            archive = rootfs(root, row)
            command("docker", "import", "--platform", expected_platform, str(archive), image)
        else:
            if not re.fullmatch(r"archlinux:base-devel@sha256:[a-f0-9]{64}", image):
                raise ValueError("Arch packaging requires a pinned official image")
            command("docker", "pull", "--platform", expected_platform, image)
        measured = command("docker", "image", "inspect", "--format", "{{.Architecture}}", image)
        if measured != expected_platform.split("/")[1]:
            raise ValueError("Docker substrate architecture differs from the native CPU")
        uid, gid = str(os.getuid()), str(os.getgid())
        if uid == "0":
            raise ValueError("pacman packaging must be requested by an unprivileged owner")
        prepare = ('set -eu; pacman-key --init; pacman-key --populate "$1"; '
                   'pacman -Syu --noconfirm --needed base-devel; '
                   'getent group "$3" >/dev/null || groupadd -g "$3" pwbuild; '
                   'getent passwd "$2" >/dev/null || useradd -M -u "$2" -g "$3" pwbuild')
        keyring = "archlinuxarm" if imported else "archlinux"
        container = command("docker", "create", "--platform", expected_platform, image,
                            "/bin/bash", "-c", prepare, "prepare", keyring, uid, gid)
        subprocess.run(["docker", "start", "--attach", container], check=True)
        if command("docker", "inspect", "--format", "{{.State.ExitCode}}", container) != "0":
            raise ValueError("native pacman substrate preparation failed")
        command("docker", "commit", container, prepared)
        launch = ["docker", "run", "--rm", "--platform", expected_platform, "--network=none", "--cap-drop=ALL",
                  "--security-opt=no-new-privileges", "--user", uid + ":" + gid,
                  "--env", "HOME=/tmp", "--env", "SOURCE_DATE_EPOCH=" + str(epoch),
                  "--env", "PKGEXT=.pkg.tar.zst", "--mount", "type=bind,source=" + str(work.resolve()) + ",target=/build",
                  "--workdir", "/build", prepared]
        info = command(*launch, "makepkg", "--printsrcinfo")
        (work / ".SRCINFO").write_text(info + "\n")
        subprocess.run([*launch, "makepkg", "--nodeps", "--noconfirm"], check=True)
        packages = command(*launch, "pacman", "-Q")
        return {"distribution": row["distribution"], "native_cpu": architecture, "platform": expected_platform,
                "substrate": row, "package_toolchain": packages.splitlines(), "network_during_packaging": False,
                "desktop_qualified": False}
    finally:
        if container is not None:
            subprocess.run(["docker", "rm", "--force", container], check=False)
        subprocess.run(["docker", "image", "rm", prepared], check=False)
        if imported:
            subprocess.run(["docker", "image", "rm", image], check=False)
