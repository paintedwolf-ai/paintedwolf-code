"""Build a native pacman package from the sealed common Linux installation tree."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import platform
import shlex
import shutil
import subprocess
import tarfile
import sys
import tempfile

from .arch_substrate import build as build_in_substrate

from release_semver import pacman_package_version, parse, parse_release_build

PACKAGE = "painted-wolf-code"
ARCHITECTURES = {"x86_64", "aarch64"}


def validate_resources(tree: Path) -> None:
    app = tree / "opt/painted-wolf-code"
    required = ["release-version", "THIRD-PARTY-NOTICES.md",
                "engine-root/gitengine/bin/git", "engine-root/gitengine/bin/git-lfs",
                "engine-root/gitengine/libexec/git-core/git-remote-https",
                "engine-root/browser/chrome-headless-shell", "engine-root/ocr/bin/tesseract",
                "engine-root/ocr/tessdata/eng.traineddata", "engine-root/ocr/tessdata/osd.traineddata",
                "engine-root/decide/heads/release.json"]
    for relative in required:
        path = app / relative
        if not path.is_file() or path.stat().st_size == 0:
            raise ValueError(f"incomplete pacman payload: {relative}")
    for pattern in ["engine-root/schemas/**/*", "engine-root/bundled/opengrep-*/opengrep",
                    "engine-root/decide/models/*/.complete"]:
        if not any(path.is_file() for path in app.glob(pattern)):
            raise ValueError(f"incomplete pacman payload: {pattern}")


def stream_digest(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as source:
        while chunk := source.read(1 << 20):
            value.update(chunk)
    return value.hexdigest()


def write_payload(tree: Path, output: Path, epoch: int) -> None:
    """Preserve sealed bytes/modes with deterministic metadata and safe link roots."""
    with tarfile.open(output, "w:xz") as archive:
        for path in sorted(tree.rglob("*")):
            relative = path.relative_to(tree)
            if relative.parts[0] not in {"opt", "usr", "etc"}:
                raise ValueError(f"unexpected pacman payload root: {relative}")
            if relative.parts[0] == "etc" and len(relative.parts) > 1 and relative.parts[1] != "init.d":
                raise ValueError(f"unexpected pacman configuration path: {relative}")
            if path.is_symlink():
                target = os.readlink(path)
                resolved = ((tree / target.lstrip("/")) if target.startswith("/") else
                            (path.parent / target)).resolve()
                if not resolved.is_relative_to(tree.resolve()):
                    raise ValueError(f"payload symlink escapes package tree: {relative}")
            if not (path.is_symlink() or path.is_dir() or path.is_file()):
                raise ValueError(f"unsupported payload type: {relative}")
            info = archive.gettarinfo(str(path), arcname="payload/" + relative.as_posix())
            info.uid = info.gid = 0
            info.uname = info.gname = "root"
            info.mtime = epoch
            if info.isfile():
                with path.open("rb") as source:
                    archive.addfile(info, source)
            else:
                archive.addfile(info)


def recipe(root: Path, payload: Path, architecture: str, product: str) -> str:
    if architecture not in ARCHITECTURES:
        raise ValueError(f"unsupported pacman architecture: {architecture}")
    floors = dict(line.split(maxsplit=1) for line in
                  (root / "lycaon/internal/platformfloor/linux_floor.txt").read_text().splitlines() if line.strip())
    catalog = json.loads((root / "packaging/linux/dependencies.json").read_text())["pacman"]
    install = root / "packaging/linux/arch/painted-wolf-code.install"
    sources = [shlex.quote(payload.name)]
    hashes = [shlex.quote(stream_digest(payload))]
    if install.is_file():
        sources.append(shlex.quote(install.name))
        hashes.append(shlex.quote(stream_digest(install)))
    replacements = {
        "PKGVER": pacman_package_version(parse(product)),
        "PKGREL": str(parse_release_build((root / "RELEASE_BUILD").read_text().strip())),
        "ARCH": shlex.quote(architecture),
        "DEPENDS": " ".join(shlex.quote(row.format(**floors)) for row in catalog["depends"]),
        "OPTDEPENDS": " ".join(shlex.quote(row.format(**floors)) for row in catalog["recommends"]),
        "INSTALL": "install=" + shlex.quote(install.name) if install.is_file() else "",
        "PAYLOAD": " ".join(sources),
        "SHA256": " ".join(hashes),
    }
    text = (root / "packaging/linux/arch/PKGBUILD.in").read_text()
    for key, value in replacements.items():
        text = text.replace("@" + key + "@", value)
    return text


def build(root: Path, tree: Path, architecture: str, output: Path, container=False) -> Path:
    if platform.system() != "Linux" or platform.machine() != architecture:
        raise ValueError("pacman construction requires the matching native Linux architecture")
    if not container and shutil.which("makepkg") is None:
        raise ValueError("makepkg is required for native pacman construction")
    if os.geteuid() == 0:
        raise ValueError("makepkg must run as an unprivileged build user")
    product = (root / "VERSION").read_text().strip()
    version = pacman_package_version(parse(product))
    validate_resources(tree)
    subprocess.run([sys.executable, str(root / "scripts/linux-package-manifest.py"), "verify-tree",
                    "--tree", str(tree), "--version", product, "--architecture", architecture], check=True)
    revision = parse_release_build((root / "RELEASE_BUILD").read_text().strip())
    output.mkdir(parents=True, exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix=f"{PACKAGE}-{architecture}-recipe-", dir=output))
    payload = work / f"{PACKAGE}-{product}-{architecture}-payload.tar.xz"
    epoch = int(subprocess.check_output(["git", "show", "-s", "--format=%ct", "HEAD"], cwd=root, text=True))
    write_payload(tree, payload, epoch)
    (work / "PKGBUILD").write_text(recipe(root, payload, architecture, product))
    install = root / "packaging/linux/arch/painted-wolf-code.install"
    if install.is_file():
        shutil.copyfile(install, work / install.name)
    env = dict(os.environ, SOURCE_DATE_EPOCH=str(epoch), PKGDEST=str(work.resolve()),
               CARCH=architecture, PKGEXT=".pkg.tar.zst")
    if container:
        substrate = build_in_substrate(root, work, architecture, epoch)
    else:
        info = subprocess.check_output(["makepkg", "--printsrcinfo"], cwd=work, env=env, text=True)
        (work / ".SRCINFO").write_text(info)
        subprocess.run(["makepkg", "--nodeps", "--noconfirm"], cwd=work, env=env, check=True)
        substrate = {"distribution": "native host", "native_cpu": architecture, "desktop_qualified": False}
    package = work / f"{PACKAGE}-{version}-{revision}-{architecture}.pkg.tar.zst"
    if not package.is_file():
        raise ValueError(f"makepkg did not produce {package.name}")
    destination = output / package.name
    package.replace(destination)
    report = {"format_version": 1, "family": "pacman", "source_commit": subprocess.check_output(
        ["git", "rev-parse", "HEAD"], cwd=root, text=True).strip(), "version": product,
        "native_version": version + "-" + str(revision), "architecture": architecture,
        "file": destination.name, "sha256": stream_digest(destination), "bytes": destination.stat().st_size,
        "sealed_payload_sha256": stream_digest(payload), "substrate": substrate,
        "qualification_status": "unverified", "published": False}
    (output / "pacman-candidate.json").write_text(json.dumps(report, sort_keys=True, indent=2) + "\n")
    return destination
