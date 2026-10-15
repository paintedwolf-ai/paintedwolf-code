#!/usr/bin/env python3
"""Finish the Linux .deb and .rpm from the one tree Tauri staged.

`tauri bundle --bundles deb` renders the icons and places every path of the
`files` map from linux-package-config.py into `bundle/deb/<stem>/data`; it also
patches the shell executable with its bundle type. The adapter moves the shell
under the install root, keeps only the packaged desktop entry, and builds both
packages from that tree with the distributions' own tools:

- The manifest in the tree is sealed against the staged bytes, so it describes
  the patched shell.
- .deb: dpkg-deb with xz on every core. The control file is Tauri's with the
  release's Debian version (Tauri writes the native app version, which drops the
  prerelease) and the installed size and md5sums of the sealed tree.
- .rpm: rpmbuild with zstd on every core. Tauri's rpm builder holds the whole
  payload in memory and compresses it on one thread. The spec takes its
  dependencies from the catalog the .deb uses, its scriptlets from
  packaging/linux/rpm, and its metadata from the Tauri configuration; fragments
  only one family ships are swapped.
"""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import os
import resource
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import textwrap
import time

PACKAGE_NAME = "painted-wolf-code"
OWNED_ROOT = "opt/painted-wolf-code"
DEB_ARCHES = {"x86_64": "amd64", "aarch64": "arm64"}
DEB_COMPRESSION = ("xz", "6")
DEB_SCRIPTS = ("preinst", "postinst", "prerm", "postrm")
RPM_SCRIPTLETS = (("pre", "pre"), ("post", "post"), ("preun", "preun"), ("postun", "postun"))
# rpm expands globs and macros in %files lines; quoting covers spaces only.
UNSAFE_PATH_CHARACTERS = set('[]*?{}"\\%\n\r\t')


def load_package_config(root: Path):
    spec = importlib.util.spec_from_file_location("linux_package_config", root / "scripts" / "linux-package-config.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def tauri_metadata(root: Path) -> dict:
    tauri_dir = root / "lycaon-den" / "src-tauri"
    base = json.loads((tauri_dir / "tauri.conf.json").read_text(encoding="utf-8"))
    linux = json.loads((tauri_dir / "tauri.linux.conf.json").read_text(encoding="utf-8"))
    bundle = {**base.get("bundle", {}), **linux.get("bundle", {}), "identifier": base["identifier"]}
    for key in ("publisher", "homepage", "license", "shortDescription", "longDescription"):
        if not isinstance(bundle.get(key), str) or not bundle[key].strip():
            raise ValueError(f"Tauri bundle configuration lacks {key}")
    return bundle


def staged_tree(bundle_dir: Path) -> Path:
    staged = [path for path in (bundle_dir / "deb").iterdir() if path.is_dir()]
    if len(staged) != 1 or not (staged[0] / "data").is_dir() or not (staged[0] / "control" / "control").is_file():
        raise ValueError(f"expected exactly one tree staged by Tauri under {bundle_dir / 'deb'}")
    return staged[0]


def tree_files(tree: Path):
    for dirpath, dirnames, filenames in os.walk(tree):
        dirnames.sort()
        for name in sorted(filenames):
            yield Path(dirpath) / name


def rendered_dependencies(root: Path, family: str) -> dict[str, list[str]]:
    config = load_package_config(root)
    floors = config.floor_values(root / "lycaon" / "internal" / "platformfloor" / "linux_floor.txt")
    catalog = json.loads((root / "packaging" / "linux" / "dependencies.json").read_text(encoding="utf-8"))
    return config.render_dependencies(catalog, family, floors)


def deb_control(root: Path, data: Path, version: str, arch: str) -> str:
    meta = tauri_metadata(root)
    deb = meta.get("linux", {}).get("deb", {})
    dependencies = rendered_dependencies(root, "deb")
    installed_kib = sum(path.lstat().st_size for path in tree_files(data)) // 1024
    description = textwrap.wrap(meta["longDescription"], width=78)
    fields = [
        f"Package: {PACKAGE_NAME}",
        f"Version: {version}",
        f"Architecture: {DEB_ARCHES[arch]}",
        f"Installed-Size: {installed_kib}",
        f"Maintainer: {meta['publisher']}",
        f"Section: {deb.get('section', 'devel')}",
        f"Priority: {deb.get('priority', 'optional')}",
        f"Homepage: {meta['homepage']}",
        f"Depends: {', '.join(dependencies['depends'])}",
        f"Recommends: {', '.join(dependencies['recommends'])}",
        f"Description: {meta['shortDescription']}",
        *(f" {line}" for line in description),
    ]
    return "\n".join(fields) + "\n"


def write_deb(root: Path, staged: Path, workdir: Path, version: str, arch: str, output: Path) -> Path:
    data = staged / "data"
    deb_root = workdir / "deb-root"
    subprocess.run(["cp", "-al", f"{data}/.", str(deb_root)], check=True)
    control = deb_root / "DEBIAN"
    control.mkdir(mode=0o755)
    (control / "control").write_text(deb_control(root, data, version, arch), encoding="utf-8")
    for script in DEB_SCRIPTS:
        shutil.copyfile(root / "packaging" / "linux" / "deb" / script, control / script)
        (control / script).chmod(0o755)
    sums = []
    for path in tree_files(data):
        if path.is_symlink():
            continue
        digest = hashlib.md5(path.read_bytes(), usedforsecurity=False).hexdigest()
        sums.append(f"{digest}  {path.relative_to(data).as_posix()}")
    (control / "md5sums").write_text("\n".join(sums) + "\n", encoding="utf-8")
    for path in control.iterdir():
        if path.name not in DEB_SCRIPTS:
            path.chmod(0o644)
    target = output / f"{PACKAGE_NAME}_{version}_{DEB_ARCHES[arch]}.deb"
    kind, level = DEB_COMPRESSION
    subprocess.run(
        ["dpkg-deb", "--root-owner-group", f"-Z{kind}", f"-z{level}", "--build", str(deb_root), str(target)],
        check=True,
        stdout=subprocess.DEVNULL,
    )
    return target


def runs_as_program(path: Path) -> bool:
    """An ELF executable (a shared object only with a program interpreter) or a #! script."""
    with path.open("rb") as handle:
        header = handle.read(64)
        if header[:2] == b"#!":
            return True
        if len(header) < 64 or header[:4] != b"\x7fELF" or header[4] != 2:
            return False
        order = "little" if header[5] == 1 else "big"
        kind = int.from_bytes(header[16:18], order)
        if kind == 2:  # ET_EXEC
            return True
        if kind != 3:  # ET_DYN: a PIE executable carries PT_INTERP, a shared library does not
            return False
        phoff = int.from_bytes(header[32:40], order)
        phentsize = int.from_bytes(header[54:56], order)
        phnum = int.from_bytes(header[56:58], order)
        handle.seek(phoff)
        return any(int.from_bytes(handle.read(phentsize)[:4], order) == 3 for _ in range(phnum))


def place_shell(data: Path, app_id: str) -> None:
    """Move the shell Tauri installs in /usr/bin under the install root, and drop
    the product-named desktop entry Tauri always writes; packaging/linux ships
    the one entry, named after the application identifier."""
    staged_shell = data / "usr" / "bin" / PACKAGE_NAME
    shell = data / OWNED_ROOT / "bin" / PACKAGE_NAME
    if not staged_shell.is_file():
        raise ValueError(f"Tauri staged no shell executable at /usr/bin/{PACKAGE_NAME}")
    shell.parent.mkdir(parents=True, exist_ok=True)
    staged_shell.replace(shell)
    applications = data / "usr" / "share" / "applications"
    for entry in applications.glob("*.desktop"):
        if entry.name != f"{app_id}.desktop":
            entry.unlink()
    if not (applications / f"{app_id}.desktop").is_file():
        raise ValueError(f"the staged tree has no {app_id}.desktop")


def normalise_modes(data: Path) -> None:
    """Directories and programs 0755; data, documents and shared libraries 0644."""
    for dirpath, dirnames, filenames in os.walk(data):
        Path(dirpath).chmod(0o755)
        for name in filenames:
            path = Path(dirpath) / name
            if not path.is_symlink():
                path.chmod(0o755 if runs_as_program(path) else 0o644)


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
    dependencies = rendered_dependencies(root, "rpm")
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
        f"Packager: {meta['publisher']}",
        "Group: Development/Tools",
        # Dependencies are declared from the measured floor, never scanned from bundled ELF files.
        "AutoReqProv: no",
        *(f"Requires: {row}" for row in dependencies["depends"]),
        *(f"Recommends: {row}" for row in dependencies["recommends"]),
        "",
        "%description",
        textwrap.fill(meta["longDescription"].replace("%", "%%"), width=79),
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


def write_rpm(root: Path, staged: Path, workdir: Path, version: str, arch: str, output: Path, zstd_level: int) -> Path:
    tree = rpm_tree(root, staged / "data", workdir / "rpm-tree")
    topdir = workdir / "rpmbuild"
    topdir.mkdir()
    spec_path = topdir / f"{PACKAGE_NAME}.spec"
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
        for tool, package in (("dpkg-deb", "dpkg"), ("rpmbuild", "rpm")):
            if shutil.which(tool) is None:
                raise ValueError(f"{tool} is required to assemble the Linux packages (Debian and Ubuntu: {package})")
        staged = staged_tree(bundle_dir)
        place_shell(staged / "data", tauri_metadata(root)["identifier"])
        normalise_modes(staged / "data")
        subprocess.run(
            [sys.executable, str(root / "scripts" / "linux-package-manifest.py"), "seal-tree", "--tree", str(staged / "data")],
            check=True,
        )
        for output in (bundle_dir / "deb", bundle_dir / "rpm"):
            output.mkdir(parents=True, exist_ok=True)
            for stale in [*output.glob("*.deb"), *output.glob("*.rpm")]:
                stale.unlink()
        with tempfile.TemporaryDirectory(prefix="linux-package-", dir=bundle_dir) as workdir:
            # rpm first, so the first peak reading covers rpmbuild alone.
            started = time.monotonic()
            rpm = write_rpm(root, staged, Path(workdir), args.version, args.arch, bundle_dir / "rpm", args.zstd_level)
            print(
                f"linux-package-assemble: {rpm} ({rpm.stat().st_size} bytes, zstd {args.zstd_level}) "
                f"in {time.monotonic() - started:.1f}s, peak RSS "
                f"{resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss // 1024} MiB",
                file=sys.stderr,
            )
            started = time.monotonic()
            deb = write_deb(root, staged, Path(workdir), args.version, args.arch, bundle_dir / "deb")
            print(
                f"linux-package-assemble: {deb} ({deb.stat().st_size} bytes, {' '.join(DEB_COMPRESSION)}) "
                f"in {time.monotonic() - started:.1f}s, peak RSS of rpmbuild and dpkg-deb "
                f"{resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss // 1024} MiB",
                file=sys.stderr,
            )
    except (OSError, ValueError, KeyError, json.JSONDecodeError, subprocess.CalledProcessError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    build = commands.add_parser("assemble", help="build both packages from Tauri's staged tree")
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
