"""Admit final native pacman metadata and safely extract bytes for shared payload audits."""

import argparse
import json
from pathlib import Path, PurePosixPath
import subprocess
import tarfile

from release_semver import pacman_package_version, parse, parse_release_build


def extract(package, destination, root, architecture, version):
    if package.is_symlink() or not package.is_file() or destination.exists():
        raise ValueError("pacman audit requires a regular package and new extraction root")
    destination.mkdir(parents=True)
    process = subprocess.Popen(["bsdtar", "-cf", "-", "@" + str(package.resolve())], stdout=subprocess.PIPE)
    seen, total = set(), 0
    try:
        with tarfile.open(fileobj=process.stdout, mode="r|") as archive:
            for member in archive:
                name = PurePosixPath(member.name)
                if (name.is_absolute() or ".." in name.parts or member.name in seen or
                        member.mode & 0o6002 or not (member.isfile() or member.isdir() or member.issym() or member.islnk())):
                    raise ValueError("pacman package contains an unsafe or repeated payload object")
                seen.add(member.name)
                total += member.size
                if len(seen) > 200000 or total > 16 << 30:
                    raise ValueError("pacman package exceeds its audited payload bounds")
                archive.extract(member, destination, filter="data")
        if process.wait() != 0:
            raise ValueError("pacman archive decoding failed")
    finally:
        process.stdout.close()
        if process.poll() is None:
            process.terminate()
            process.wait()
    fields = {}
    for line in (destination / ".PKGINFO").read_text().splitlines():
        if line and not line.startswith("#"):
            key, separator, value = line.partition(" = ")
            if not separator:
                raise ValueError("pacman package metadata is malformed")
            fields.setdefault(key, []).append(value)
    revision = parse_release_build((root / "RELEASE_BUILD").read_text().strip())
    expected = pacman_package_version(parse(version)) + "-" + str(revision)
    if any(fields.get(key) != [value] for key, value in {
            "pkgname": "painted-wolf-code", "pkgver": expected, "arch": architecture}.items()):
        raise ValueError("pacman package metadata differs from the selected release")
    floors = dict(line.split(maxsplit=1) for line in
                  (root / "lycaon/internal/platformfloor/linux_floor.txt").read_text().splitlines() if line.strip())
    dependencies = json.loads((root / "packaging/linux/dependencies.json").read_text())["pacman"]
    for key, catalog in [("depend", "depends"), ("optdepend", "recommends")]:
        if sorted(fields.get(key, [])) != sorted(value.format(**floors) for value in dependencies[catalog]):
            raise ValueError("pacman dependencies differ from the shared native catalog")
    if not (destination / ".BUILDINFO").is_file() or not (destination / ".MTREE").is_file():
        raise ValueError("pacman package lacks native build or file inventory metadata")
    return fields


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ["package", "destination", "root"]:
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--architecture", choices=["x86_64", "aarch64"], required=True)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    extract(args.package, args.destination, args.root, args.architecture, args.version)


if __name__ == "__main__":
    main()
