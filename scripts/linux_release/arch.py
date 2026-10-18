"""Build a native pacman package from the sealed common Linux installation tree."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import re
import platform
import shlex
import shutil
import subprocess
import tarfile
import sys
import tempfile

from release_semver import parse, parse_release_build

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


def package_version(product: str) -> str:
    version = parse(product)
    if version.prerelease is None:
        return version.core
    # The alphabetic component sorts below the final numeric core in libalpm.
    # Restrict channel labels so numeric-vs-alphabetic SemVer ordering is preserved.
    parts = version.prerelease
    if not re.fullmatch(r"(?:alpha|beta|rc|preview|dev)[0-9]*", parts[0]) or any(
        not part.isdigit() for part in parts[1:]
    ):
        raise ValueError("pacman prereleases require alpha/beta/rc/preview/dev followed by numeric identifiers")
    return version.core + "pre." + ".".join(parts)


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
            if relative.parts[0] not in {"opt", "usr"}:
                raise ValueError(f"unexpected pacman payload root: {relative}")
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
    replacements = {
        "PKGVER": package_version(product),
        "PKGREL": str(parse_release_build((root / "RELEASE_BUILD").read_text().strip())),
        "ARCH": shlex.quote(architecture),
        "DEPENDS": " ".join(shlex.quote(row.format(**floors)) for row in catalog["depends"]),
        "OPTDEPENDS": " ".join(shlex.quote(row.format(**floors)) for row in catalog["recommends"]),
        "PAYLOAD": shlex.quote(payload.name),
        "SHA256": shlex.quote(stream_digest(payload)),
    }
    text = (root / "packaging/linux/arch/PKGBUILD.in").read_text()
    for key, value in replacements.items():
        text = text.replace("@" + key + "@", value)
    return text


def build(root: Path, tree: Path, architecture: str, output: Path) -> Path:
    if platform.system() != "Linux" or platform.machine() != architecture:
        raise ValueError("pacman construction requires the matching native Linux architecture")
    if shutil.which("makepkg") is None:
        raise ValueError("makepkg is required for native pacman construction")
    if os.geteuid() == 0:
        raise ValueError("makepkg must run as an unprivileged build user")
    product = (root / "VERSION").read_text().strip()
    version = package_version(product)
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
    env = dict(os.environ, SOURCE_DATE_EPOCH=str(epoch), PKGDEST=str(work.resolve()),
               CARCH=architecture, PKGEXT=".pkg.tar.zst")
    info = subprocess.check_output(["makepkg", "--printsrcinfo"], cwd=work, env=env, text=True)
    (work / ".SRCINFO").write_text(info)
    # All compilation and dependency admission happened in the shared producer path.
    # This packaging step neither installs dependencies nor invokes the package manager.
    subprocess.run(["makepkg", "--nodeps", "--noconfirm"], cwd=work, env=env, check=True)
    package = work / f"{PACKAGE}-{version}-{revision}-{architecture}.pkg.tar.zst"
    if not package.is_file():
        raise ValueError(f"makepkg did not produce {package.name}")
    destination = output / package.name
    package.replace(destination)
    return destination
