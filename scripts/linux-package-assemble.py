#!/usr/bin/env python3
"""Finish the Linux .deb and .rpm from the one tree Tauri staged.

`tauri bundle --bundles deb` compiles nothing new; it renders the desktop entries
and icons and places every path of the `files` map from linux-package-config.py
into `bundle/deb/<stem>/data`. Both packages are finished from that tree:

- .deb: Tauri writes the native app version into `control`, which drops the
  prerelease. The adapter rewrites only the control member with the release's
  Debian version and keeps Tauri's data member byte for byte.
- .rpm: Tauri's rpm builder holds the whole payload in memory and compresses it
  on one thread. rpmbuild packs the same tree, with the fragments only one
  family ships swapped, and streams it through zstd on every core. The spec takes its dependencies from the catalog the .deb uses,
  its scriptlets from packaging/linux/rpm, and its metadata from the Tauri
  configuration.
"""

from __future__ import annotations

import argparse
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time

PACKAGE_NAME = "painted-wolf-code"
OWNED_ROOT = "opt/painted-wolf-code"
DEB_ARCHES = {"x86_64": "amd64", "aarch64": "arm64"}
RPM_SCRIPTLETS = (("pre", "pre"), ("post", "post"), ("preun", "preun"), ("postun", "postun"))
# rpm expands globs and macros in %files lines; quoting covers spaces only.
UNSAFE_PATH_CHARACTERS = set('[]*?{}"\\%\n\r\t')
AR_MAGIC = b"!<arch>\n"


def load_package_config(root: Path):
    spec = importlib.util.spec_from_file_location("linux_package_config", root / "scripts" / "linux-package-config.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def tauri_metadata(root: Path) -> dict:
    tauri_dir = root / "lycaon-den" / "src-tauri"
    base = json.loads((tauri_dir / "tauri.conf.json").read_text(encoding="utf-8"))
    linux = json.loads((tauri_dir / "tauri.linux.conf.json").read_text(encoding="utf-8"))
    bundle = {**base.get("bundle", {}), **linux.get("bundle", {})}
    for key in ("publisher", "homepage", "license", "shortDescription", "longDescription"):
        if not isinstance(bundle.get(key), str) or not bundle[key].strip():
            raise ValueError(f"Tauri bundle configuration lacks {key}")
    return bundle


def staged_deb(bundle_dir: Path) -> tuple[Path, Path]:
    debs = sorted((bundle_dir / "deb").glob("*.deb"))
    if len(debs) != 1:
        raise ValueError(f"expected exactly one Tauri .deb under {bundle_dir / 'deb'}, found {len(debs)}")
    staged = debs[0].with_suffix("")
    if not (staged / "data").is_dir() or not (staged / "control" / "control").is_file():
        raise ValueError(f"Tauri left no staged tree beside {debs[0].name}")
    return debs[0], staged


def read_ar(path: Path) -> list[tuple[str, int, int]]:
    """Return (name, offset, size) for each member of an ar archive."""
    members = []
    with path.open("rb") as handle:
        if handle.read(8) != AR_MAGIC:
            raise ValueError(f"{path} is not an ar archive")
        while header := handle.read(60):
            if len(header) != 60 or header[58:60] != b"`\n":
                raise ValueError(f"{path} has a malformed ar header")
            name = header[0:16].decode().strip().rstrip("/")
            size = int(header[48:58].decode().strip())
            members.append((name, handle.tell(), size))
            handle.seek(size + (size % 2), os.SEEK_CUR)
    return members


def ar_header(name: str, size: int, mtime: int) -> bytes:
    return f"{name:<16}{mtime:<12}{0:<6}{0:<6}{'100644':<8}{size:<10}`\n".encode()


def control_member(control_dir: Path, version: str, mtime: int) -> bytes:
    lines = (control_dir / "control").read_text(encoding="utf-8").splitlines()
    versions = [index for index, line in enumerate(lines) if line.startswith("Version: ")]
    if len(versions) != 1:
        raise ValueError("Tauri's control file has no single Version field")
    lines[versions[0]] = f"Version: {version}"
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz", format=tarfile.GNU_FORMAT) as archive:
        root = tarfile.TarInfo("./")
        root.type, root.mode, root.mtime = tarfile.DIRTYPE, 0o755, mtime
        archive.addfile(root)
        for path in sorted(control_dir.iterdir()):
            data = ("\n".join(lines) + "\n").encode() if path.name == "control" else path.read_bytes()
            info = tarfile.TarInfo(f"./{path.name}")
            info.size, info.mtime = len(data), mtime
            info.mode = 0o755 if os.access(path, os.X_OK) else 0o644
            info.uname = info.gname = "root"
            archive.addfile(info, io.BytesIO(data))
    return buffer.getvalue()


def write_deb(tauri_deb: Path, staged: Path, version: str, arch: str, output: Path) -> Path:
    members = read_ar(tauri_deb)
    names = [name for name, _, _ in members]
    if names[0] != "debian-binary" or not names[1].startswith("control.tar") or not names[2].startswith("data.tar"):
        raise ValueError(f"{tauri_deb.name} members are {names}, not debian-binary, control, data")
    mtime = int(tauri_deb.stat().st_mtime)
    control = control_member(staged / "control", version, mtime)
    target = output / f"{PACKAGE_NAME}_{version}_{DEB_ARCHES[arch]}.deb"
    partial = target.with_suffix(".deb.partial")
    with tauri_deb.open("rb") as source, partial.open("wb") as handle:
        handle.write(AR_MAGIC)
        for name, offset, size in members:
            if name.startswith("control.tar"):
                name, size = "control.tar.gz", len(control)
            handle.write(ar_header(name, size, mtime))
            if name == "control.tar.gz":
                handle.write(control)
            else:
                source.seek(offset)
                remaining = size
                while remaining:
                    chunk = source.read(min(remaining, 1 << 20))
                    handle.write(chunk)
                    remaining -= len(chunk)
            if size % 2:
                handle.write(b"\n")
    partial.replace(target)
    if tauri_deb != target:
        tauri_deb.unlink()
    return target


def rpm_quote(relative: str) -> str:
    if UNSAFE_PATH_CHARACTERS & set(relative) or relative != relative.strip():
        raise ValueError(f"/{relative} cannot be named in an rpm file list")
    return f'"/{relative}"'


def rpm_file_list(tree: Path) -> list[str]:
    """Own every file and link, and the directories under the install root only."""
    lines = []
    for dirpath, dirnames, filenames in os.walk(tree):
        dirnames.sort()
        relative_dir = Path(dirpath).relative_to(tree).as_posix()
        if relative_dir == OWNED_ROOT or relative_dir.startswith(f"{OWNED_ROOT}/"):
            lines.append(f"%dir {rpm_quote(relative_dir)}")
        for name in sorted(filenames + [d for d in dirnames if (Path(dirpath) / d).is_symlink()]):
            relative = (Path(dirpath) / name).relative_to(tree).as_posix()
            lines.append(rpm_quote(relative))
    if not any(line == f'%dir "/{OWNED_ROOT}"' for line in lines):
        raise ValueError(f"the staged tree has no /{OWNED_ROOT}")
    return lines


def rpm_tree(root: Path, deb_tree: Path, destination: Path) -> Path:
    """Link the staged tree and swap the fragments only one family ships."""
    config = load_package_config(root)
    deb_only = config.optional_files(root, "deb")
    rpm_only = config.optional_files(root, "rpm")
    subprocess.run(["cp", "-al", f"{deb_tree}/.", str(destination)], check=True)
    for installed in deb_only.keys() - rpm_only.keys():
        (destination / installed.lstrip("/")).unlink()
    for installed in rpm_only.keys() - deb_only.keys():
        target = destination / installed.lstrip("/")
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(rpm_only[installed], target)
    return destination


def rpm_spec(root: Path, tree: Path, version: str, zstd_level: int) -> str:
    config = load_package_config(root)
    floors = config.floor_values(root / "lycaon" / "internal" / "platformfloor" / "linux_floor.txt")
    catalog = json.loads((root / "packaging" / "linux" / "dependencies.json").read_text(encoding="utf-8"))
    dependencies = config.render_dependencies(catalog, "rpm", floors)
    meta = tauri_metadata(root)
    scriptlets = []
    for section, filename in RPM_SCRIPTLETS:
        body = (root / "packaging" / "linux" / "rpm" / filename).read_text(encoding="utf-8")
        scriptlets.append(f"%{section}\n{body.replace('%', '%%').rstrip()}\n")
    lines = [
        f"%global _binary_payload w{zstd_level}T0.zstdio",
        "%global debug_package %{nil}",
        "%global __os_install_post %{nil}",
        "%global _build_id_links none",
        f"Name: {PACKAGE_NAME}",
        f"Version: {version}",
        "Release: 1",
        f"Summary: {meta['shortDescription']}",
        f"License: {meta['license']}",
        f"URL: {meta['homepage']}",
        f"Vendor: {meta['publisher']}",
        # Dependencies are declared from the measured floor, never scanned from bundled ELF files.
        "AutoReqProv: no",
        *(f"Requires: {row}" for row in dependencies["depends"]),
        *(f"Recommends: {row}" for row in dependencies["recommends"]),
        "",
        "%description",
        meta["longDescription"].replace("%", "%%"),
        "",
        "%install",
        f'cp -al "{tree}/." "%{{buildroot}}/"',
        "",
        *scriptlets,
        "%files",
        "%defattr(-,root,root,-)",
        *rpm_file_list(tree),
        "",
    ]
    return "\n".join(lines)


def write_rpm(root: Path, staged: Path, version: str, arch: str, output: Path, zstd_level: int) -> Path:
    if shutil.which("rpmbuild") is None:
        raise ValueError("rpmbuild is required to assemble the .rpm (Debian and Ubuntu: the rpm package)")
    for stale in output.glob("*.rpm"):
        stale.unlink()
    with tempfile.TemporaryDirectory(prefix="rpmbuild-", dir=staged.parent) as topdir:
        tree = rpm_tree(root, staged / "data", Path(topdir) / "tree")
        spec_path = Path(topdir) / f"{PACKAGE_NAME}.spec"
        spec_path.write_text(rpm_spec(root, tree, version, zstd_level), encoding="utf-8")
        subprocess.run(
            [
                "rpmbuild", "-bb", "--quiet", "--target", arch,
                "--define", f"_topdir {topdir}",
                "--define", f"_rpmdir {output}",
                "--define", "_build_name_fmt %%{NAME}-%%{VERSION}-%%{RELEASE}.%%{ARCH}.rpm",
                str(spec_path),
            ],
            check=True,
        )
    rpms = sorted(output.glob("*.rpm"))
    if len(rpms) != 1:
        raise ValueError(f"rpmbuild produced {len(rpms)} packages in {output}")
    return rpms[0]


def tree_entries(tree: Path, skip: set[str]) -> dict[str, tuple[str, int, str]]:
    entries = {}
    for dirpath, dirnames, filenames in os.walk(tree):
        for name in filenames + [d for d in dirnames if (Path(dirpath) / d).is_symlink()]:
            path = Path(dirpath) / name
            installed = "/" + path.relative_to(tree).as_posix()
            if installed in skip:
                continue
            info = path.lstat()
            link = os.readlink(path) if path.is_symlink() else ""
            entries[installed] = ("link" if link else "file", info.st_mode & 0o7777, link)
    return entries


def compare(args: argparse.Namespace) -> int:
    root = args.root.resolve()
    config = load_package_config(root)
    deb_fragments = config.optional_files(root, "deb").keys()
    rpm_fragments = config.optional_files(root, "rpm").keys()
    deb = tree_entries(args.deb_tree, set(deb_fragments - rpm_fragments))
    rpm = tree_entries(args.rpm_tree, set(rpm_fragments - deb_fragments))
    differences = sorted(
        f"{path}: deb {deb.get(path)} rpm {rpm.get(path)}"
        for path in deb.keys() | rpm.keys()
        if deb.get(path) != rpm.get(path)
    )
    if differences:
        print("error: .deb and .rpm install different trees:", *differences[:20], sep="\n  ", file=sys.stderr)
        return 1
    print(f"linux-package-assemble: .deb and .rpm install the same {len(deb)} files and links", file=sys.stderr)
    return 0


def assemble(args: argparse.Namespace) -> int:
    root = args.root.resolve()
    bundle_dir = args.bundle_dir.resolve()
    try:
        tauri_deb, staged = staged_deb(bundle_dir)
        started = time.monotonic()
        deb = write_deb(tauri_deb, staged, args.version, args.arch, bundle_dir / "deb")
        print(f"linux-package-assemble: {deb} in {time.monotonic() - started:.1f}s", file=sys.stderr)
        rpm_dir = bundle_dir / "rpm"
        rpm_dir.mkdir(parents=True, exist_ok=True)
        started = time.monotonic()
        rpm = write_rpm(root, staged, args.version, args.arch, rpm_dir, args.zstd_level)
        print(
            f"linux-package-assemble: {rpm} ({rpm.stat().st_size} bytes, zstd {args.zstd_level}) "
            f"in {time.monotonic() - started:.1f}s",
            file=sys.stderr,
        )
    except (OSError, ValueError, KeyError, json.JSONDecodeError, subprocess.CalledProcessError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    build = commands.add_parser("assemble", help="finish both packages from Tauri's staged tree")
    build.add_argument("--root", type=Path, default=Path(__file__).resolve().parent.parent)
    build.add_argument("--bundle-dir", type=Path, required=True, help="target/<triple>/release/bundle")
    build.add_argument("--arch", required=True, choices=sorted(DEB_ARCHES))
    build.add_argument("--version", required=True, help="Linux package version (release-metadata.py)")
    build.add_argument("--zstd-level", type=int, default=19)
    build.set_defaults(run=assemble)
    check = commands.add_parser("compare", help="require two extracted package trees to match")
    check.add_argument("--root", type=Path, default=Path(__file__).resolve().parent.parent)
    check.add_argument("--deb-tree", type=Path, required=True)
    check.add_argument("--rpm-tree", type=Path, required=True)
    check.set_defaults(run=compare)
    args = parser.parse_args()
    return args.run(args)


if __name__ == "__main__":
    raise SystemExit(main())
