#!/usr/bin/env python3
"""Reproducible builder for the Portable Python CPython/WASI curated image.

Reads scripts/portable-python/lock.json, verifies SHA-256 digests of every
pinned upstream input, extracts CPython 3.12 WASI + stdlib, installs all
29 locked distributions (13 public + 16 runtime deps, zero host binaries),
generates image-manifest.json (portable-python.image-manifest/v1), and packs
a deterministic tarball + SHA256SUMS.
"""

from __future__ import annotations

import argparse
import base64
import datetime as dt
import hashlib
import json
import os
import shutil
import struct
import subprocess
import sys
import tarfile
import urllib.request
import zipfile
from pathlib import Path
from typing import Any

from image_assets import (
    apply_wasi_package_patches,
    install_bundled_fonts,
    write_third_party_notices,
)

PINNED_CPYTHON_WASM_SHA256 = "fd955c1b91ef90a070564314f9bc6c98443bc731c462191ebca2e5a1ec317c8c"
PINNED_METERED_WASM_SHA256 = "22d6ad6719bd89707465bcf066b02b411d53ebe710cce056af3ec41cbaca257d"
PINNED_IMPORT_INVENTORY_SHA256 = "d4dca3e9def0586bdc7c43af04340073fcbd99b28a87f26f33341b30f00d623b"
CPYTHON_312_MAGIC_HEX = "cb0d0d0a"

FORBIDDEN_SUFFIXES = (".so", ".dylib", ".pyd", ".dll", ".a", ".o", ".c", ".h", ".pyx", ".pxd", ".pxi")

STDLIB_CAPABILITIES = [
    "ast",
    "base64",
    "bz2",
    "collections",
    "csv",
    "datetime",
    "decimal",
    "difflib",
    "fractions",
    "glob",
    "hashlib",
    "io",
    "itertools",
    "json",
    "math",
    "os",
    "pathlib",
    "pyexpat",
    "re",
    "sqlite3",
    "statistics",
    "tokenize",
    "tomllib",
    "unittest",
    "urllib.parse",
    "xml.etree.ElementTree",
    "zipfile",
    "zlib",
    "zoneinfo",
]


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def sha256_tree(root: Path, rel_paths: list[str] | None = None) -> str:
    """Compute a deterministic SHA-256 over relative paths + file contents."""
    h = hashlib.sha256()
    if rel_paths is None:
        files = sorted(
            p.relative_to(root).as_posix()
            for p in root.rglob("*")
            if p.is_file()
        )
    else:
        files = sorted(set(rel_paths))
    for rel in files:
        full = root / rel
        if not full.is_file():
            continue
        h.update(rel.encode("utf-8"))
        h.update(b"\x00")
        h.update(sha256_file(full).encode("ascii"))
        h.update(b"\n")
    return h.hexdigest()


WASI_ALLOWED_IMPORTS = frozenset(
    {
        "args_get",
        "args_sizes_get",
        "clock_res_get",
        "clock_time_get",
        "environ_get",
        "environ_sizes_get",
        "fd_advise",
        "fd_close",
        "fd_fdstat_get",
        "fd_fdstat_set_flags",
        "fd_filestat_get",
        "fd_prestat_dir_name",
        "fd_prestat_get",
        "fd_read",
        "fd_readdir",
        "fd_seek",
        "fd_tell",
        "fd_write",
        "poll_oneoff",
        "proc_exit",
        "random_get",
        "sched_yield",
    }
)

WASI_MEDIATED_FS_IMPORTS = frozenset(
    {
        "fd_allocate",
        "fd_datasync",
        "fd_filestat_set_size",
        "fd_filestat_set_times",
        "fd_pread",
        "fd_pwrite",
        "fd_renumber",
        "fd_sync",
        "path_create_directory",
        "path_filestat_get",
        "path_filestat_set_times",
        "path_link",
        "path_open",
        "path_readlink",
        "path_remove_directory",
        "path_rename",
        "path_symlink",
        "path_unlink_file",
    }
)

WASI_NO_AUTHORITY_SOCKET_IMPORTS = frozenset(
    {
        "sock_accept",
        "sock_recv",
        "sock_send",
        "sock_shutdown",
    }
)


def classify_wasm_import(mod: str, name: str, kind: str) -> dict[str, str]:
    entry: dict[str, str] = {"module": mod, "name": name, "kind": kind}
    if mod == "wasi_snapshot_preview1":
        if name in WASI_ALLOWED_IMPORTS:
            entry["disposition"] = "allowed"
            entry["authority"] = "runtime"
            return entry
        if name in WASI_MEDIATED_FS_IMPORTS:
            entry["disposition"] = "mediated"
            entry["authority"] = "workspace_adapter"
            return entry
        if name in WASI_NO_AUTHORITY_SOCKET_IMPORTS:
            entry["disposition"] = "mediated"
            entry["authority"] = "none"
            entry["failure_mode"] = "ENOSYS/ENOTSUP (no preopened sockets)"
            return entry
    entry["disposition"] = "denied"
    entry["authority"] = "none"
    entry["failure_mode"] = "rejected at module load"
    return entry


def parse_wasm_imports(wasm_path: Path) -> list[dict[str, str]]:
    """Parse the WebAssembly Import section (section id 2) without external deps."""
    data = wasm_path.read_bytes()
    if data[:4] != b"\x00asm":
        raise ValueError(f"{wasm_path} is not a valid WebAssembly binary")
    pos = 8  # skip magic + version
    n = len(data)

    def read_leb128() -> int:
        nonlocal pos
        result = 0
        shift = 0
        while True:
            b = data[pos]
            pos += 1
            result |= (b & 0x7F) << shift
            if (b & 0x80) == 0:
                return result
            shift += 7

    def read_name() -> str:
        nonlocal pos
        length = read_leb128()
        s = data[pos : pos + length].decode("utf-8")
        pos += length
        return s

    imports: list[dict[str, str]] = []
    kind_names = {0: "func", 1: "table", 2: "memory", 3: "global"}

    while pos < n:
        sec_id = data[pos]
        pos += 1
        sec_size = read_leb128()
        sec_end = pos + sec_size
        if sec_id == 2:  # Import section
            count = read_leb128()
            for _ in range(count):
                mod = read_name()
                name = read_name()
                kind = data[pos]
                pos += 1
                if kind == 0:  # func typeidx
                    read_leb128()
                elif kind == 1:  # table: reftype + limits
                    pos += 1
                    flags = read_leb128()
                    read_leb128()
                    if flags & 1:
                        read_leb128()
                elif kind == 2:  # memory: limits
                    flags = read_leb128()
                    read_leb128()
                    if flags & 1:
                        read_leb128()
                elif kind == 3:  # global: valtype + mut
                    pos += 2
                else:
                    raise ValueError(f"Unsupported WASM import kind {kind}")
                imports.append(
                    classify_wasm_import(mod, name, kind_names.get(kind, str(kind)))
                )
            break
        pos = sec_end
    return imports


def download_verified(url: str, dest: Path, expected_sha256: str) -> Path:
    dest.parent.mkdir(parents=True, exist_ok=True)
    if dest.exists():
        actual = sha256_file(dest)
        if actual == expected_sha256:
            return dest
        dest.unlink()
    tmp = dest.with_suffix(dest.suffix + ".tmp")
    urllib.request.urlretrieve(url, tmp)
    actual = sha256_file(tmp)
    if actual != expected_sha256:
        tmp.unlink(missing_ok=True)
        raise RuntimeError(
            f"SHA-256 mismatch for {url}: expected {expected_sha256}, got {actual}"
        )
    tmp.replace(dest)
    return dest


def record_hash(path: Path) -> tuple[str, int]:
    data = path.read_bytes()
    digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode("ascii")
    return f"sha256={digest}", len(data)


def write_record(packages_root: Path, dist_info_dir: Path, rel_files: list[str]) -> str:
    record_rel = (dist_info_dir.relative_to(packages_root) / "RECORD").as_posix()
    lines = []
    for rel in sorted(set(rel_files)):
        if rel == record_rel:
            continue
        h, size = record_hash(packages_root / rel)
        lines.append(f"{rel},{h},{size}")
    lines.append(f"{record_rel},,")
    (packages_root / record_rel).write_text("\n".join(lines) + "\n", encoding="utf-8")
    return record_rel


def install_wheel(whl_path: Path, packages_root: Path) -> tuple[str, list[str]]:
    installed: list[str] = []
    dist_info_dir: str | None = None
    with zipfile.ZipFile(whl_path, "r") as zf:
        for info in zf.infolist():
            if info.is_dir():
                continue
            rel = info.filename
            # Skip .data directories if any (console scripts are bound via entry_points.txt)
            parts = Path(rel).parts
            if any(p.endswith(".data") for p in parts):
                continue
            if parts[0].endswith(".dist-info"):
                dist_info_dir = parts[0]
            out_path = packages_root / rel
            out_path.parent.mkdir(parents=True, exist_ok=True)
            out_path.write_bytes(zf.read(info.filename))
            installed.append(rel)
    if dist_info_dir is None:
        raise RuntimeError(f"Wheel {whl_path.name} missing .dist-info directory")
    return dist_info_dir, installed


def install_sdist_pure_python(
    sdist_path: Path,
    dist: dict[str, Any],
    packages_root: Path,
) -> tuple[str, list[str]]:
    name = dist["name"]
    version = dist["version"]
    dist_info_name = f"{name.replace('-', '_')}-{version}.dist-info"
    dist_info_dir = packages_root / dist_info_name
    dist_info_dir.mkdir(parents=True, exist_ok=True)

    # Known pure-python source subdirs inside upstream sdists
    sdist_cfg = {
        "PyYAML": ("lib/yaml/", "yaml", "LICENSE"),
        "MarkupSafe": ("src/markupsafe/", "markupsafe", "LICENSE.txt"),
        "Pillow": ("src/PIL/", "PIL", "LICENSE"),
        "lxml": ("src/lxml/", "lxml", "LICENSES.txt"),
        "regex": ("regex_3/", "regex", "LICENSE.txt"),
        "drain3": ("drain3/", "drain3", "LICENSE"),
        "numpy": ("numpy/", "numpy", "LICENSE.txt"),
        "pandas": ("pandas/", "pandas", "LICENSE"),
    }
    if name not in sdist_cfg:
        raise RuntimeError(f"No sdist_pure_python mapping for {name}")
    src_prefix, pkg_name, license_name = sdist_cfg[name]
    strip_build_sources = name in ("numpy", "pandas")
    build_source_suffixes = (
        ".c", ".cpp", ".cc", ".cxx", ".h", ".hpp", ".hh",
        ".pyx", ".pxd", ".pxi", ".pyi", ".src", ".f", ".f90",
        ".tp", ".in", ".meson",
    )

    installed: list[str] = []
    pkg_info_bytes: bytes | None = None
    license_bytes: bytes | None = None
    pandas_version_meson: bytes | None = None

    with tarfile.open(sdist_path, "r:gz") as tf:
        for member in tf.getmembers():
            if not member.isfile():
                continue
            parts = Path(member.name).parts
            if len(parts) < 2:
                continue
            inner = Path(*parts[1:]).as_posix()
            if inner == "PKG-INFO":
                f = tf.extractfile(member)
                assert f is not None
                pkg_info_bytes = f.read()
            elif inner == license_name:
                f = tf.extractfile(member)
                assert f is not None
                license_bytes = f.read()
            elif name == "pandas" and inner == "_version_meson.py":
                f = tf.extractfile(member)
                assert f is not None
                pandas_version_meson = f.read()
            elif inner.startswith(src_prefix):
                sub = inner[len(src_prefix) :]
                if sub.endswith(FORBIDDEN_SUFFIXES):
                    continue
                if strip_build_sources:
                    sub_parts = Path(sub).parts
                    if "tests" in sub_parts or "testing" in sub_parts and name == "pandas" and "tests" in sub_parts:
                        continue
                    if "tests" in sub_parts or Path(sub).name == "meson.build" or sub.endswith(build_source_suffixes):
                        continue
                rel = f"{pkg_name}/{sub}"
                out = packages_root / rel
                out.parent.mkdir(parents=True, exist_ok=True)
                f = tf.extractfile(member)
                assert f is not None
                out.write_bytes(f.read())
                installed.append(rel)

    if name == "numpy":
        cfg_rel = "numpy/__config__.py"
        cfg_path = packages_root / cfg_rel
        cfg_path.write_text(
            "# Deterministic WASI build config for Painted Wolf Portable Python\n"
            "from enum import Enum\n"
            "from numpy.core._multiarray_umath import (\n"
            "    __cpu_features__,\n"
            "    __cpu_baseline__,\n"
            "    __cpu_dispatch__,\n"
            ")\n\n"
            "__all__ = ['show']\n"
            "_built_with_meson = True\n\n"
            "class DisplayModes(Enum):\n"
            "    stdout = 'stdout'\n"
            "    dicts = 'dicts'\n\n"
            "CONFIG = {\n"
            "    'Compilers': {'c': {'name': 'clang', 'version': '16.0.0', 'linker': 'ld.lld'}},\n"
            "    'Machine Information': {'host': {'cpu': 'wasm32', 'family': 'wasm32', 'endian': 'little', 'system': 'wasi'}, 'cross-compiled': True},\n"
            "    'Build Dependencies': {\n"
            "        'blas': {'name': 'none', 'found': False},\n"
            "        'lapack': {'name': 'lapack_lite', 'found': True, 'version': '1.26.4', 'detection method': 'internal'},\n"
            "    },\n"
            "}\n\n"
            "def show(mode=DisplayModes.stdout.value):\n"
            "    if mode == DisplayModes.stdout.value:\n"
            "        print(CONFIG)\n"
            "    elif mode == DisplayModes.dicts.value:\n"
            "        return CONFIG\n"
            "    else:\n"
            "        raise AttributeError(f'Invalid mode: {mode!r}')\n",
            encoding="utf-8",
        )
        installed.append(cfg_rel)
    elif name == "pandas" and pandas_version_meson is not None:
        vm_rel = "pandas/_version_meson.py"
        vm_path = packages_root / vm_rel
        vm_path.write_bytes(pandas_version_meson)
        installed.append(vm_rel)

    if pkg_info_bytes is None:
        raise RuntimeError(f"sdist {sdist_path.name} missing PKG-INFO")

    meta_path = dist_info_dir / "METADATA"
    meta_path.write_bytes(pkg_info_bytes)
    installed.append(meta_path.relative_to(packages_root).as_posix())

    wheel_path = dist_info_dir / "WHEEL"
    wheel_path.write_text(
        "Wheel-Version: 1.0\n"
        "Generator: paintedwolf-portable-python\n"
        "Root-Is-Purelib: true\n"
        "Tag: py3-none-any\n",
        encoding="utf-8",
    )
    installed.append(wheel_path.relative_to(packages_root).as_posix())

    top_path = dist_info_dir / "top_level.txt"
    top_path.write_text(f"{pkg_name}\n", encoding="utf-8")
    installed.append(top_path.relative_to(packages_root).as_posix())

    if license_bytes is not None:
        lic_path = dist_info_dir / license_name
        lic_path.write_bytes(license_bytes)
        installed.append(lic_path.relative_to(packages_root).as_posix())

    rec_rel = write_record(packages_root, dist_info_dir, installed)
    installed.append(rec_rel)
    return dist_info_name, installed


def install_wasi_pure_python_rpds(
    script_dir: Path,
    dist: dict[str, Any],
    packages_root: Path,
) -> tuple[str, list[str], str]:
    src_root = script_dir / "rpds_wasi"
    version = dist["version"]
    dist_info_name = f"rpds_py-{version}.dist-info"
    dist_info_dir = packages_root / dist_info_name
    dist_info_dir.mkdir(parents=True, exist_ok=True)

    installed: list[str] = []
    for p in sorted((src_root / "rpds").rglob("*")):
        if not p.is_file():
            continue
        rel = p.relative_to(src_root).as_posix()
        out = packages_root / rel
        out.parent.mkdir(parents=True, exist_ok=True)
        out.write_bytes(p.read_bytes())
        installed.append(rel)

    meta_path = dist_info_dir / "METADATA"
    meta_path.write_text(
        "Metadata-Version: 2.1\n"
        "Name: rpds-py\n"
        f"Version: {version}\n"
        "Summary: Painted Wolf WASI-compatible pure-Python rpds implementation (API-compatible with rpds-py for referencing/jsonschema)\n"
        "Author: Painted Wolf Code\n"
        "License: MIT\n"
        "Requires-Python: >=3.9\n"
        "\n"
        "Independent pure-Python implementation of HashTrieMap, HashTrieSet, List, and Queue\n"
        "authored by Painted Wolf Code for wasm32-wasi where native Rust wheels cannot load.\n"
        "API-compatible with upstream rpds-py for referencing and jsonschema.\n",
        encoding="utf-8",
    )
    installed.append(meta_path.relative_to(packages_root).as_posix())

    wheel_path = dist_info_dir / "WHEEL"
    wheel_path.write_text(
        "Wheel-Version: 1.0\n"
        "Generator: paintedwolf-portable-python\n"
        "Root-Is-Purelib: true\n"
        "Tag: py3-none-any\n",
        encoding="utf-8",
    )
    installed.append(wheel_path.relative_to(packages_root).as_posix())

    top_path = dist_info_dir / "top_level.txt"
    top_path.write_text("rpds\n", encoding="utf-8")
    installed.append(top_path.relative_to(packages_root).as_posix())

    lic_src = src_root / "LICENSE"
    lic_path = dist_info_dir / "LICENSE"
    lic_path.write_bytes(lic_src.read_bytes())
    installed.append(lic_path.relative_to(packages_root).as_posix())

    rec_rel = write_record(packages_root, dist_info_dir, installed)
    installed.append(rec_rel)

    source_sha = sha256_tree(src_root)
    return dist_info_name, installed, source_sha


def assert_no_host_binaries(root: Path) -> None:
    bad: list[str] = []
    for p in root.rglob("*"):
        if p.is_file() and p.name.endswith(FORBIDDEN_SUFFIXES):
            bad.append(p.relative_to(root).as_posix())
    if bad:
        raise RuntimeError(
            f"Forbidden native/source files found in guest packages tree: {bad}"
        )


def pack_deterministic_tar_gz(src_dir: Path, out_tar_gz: Path) -> None:
    out_tar_gz.parent.mkdir(parents=True, exist_ok=True)
    files = sorted(src_dir.rglob("*"))
    with tarfile.open(out_tar_gz, "w:gz", compresslevel=6, format=tarfile.PAX_FORMAT) as tf:
        for p in files:
            rel = p.relative_to(src_dir).as_posix()
            ti = tf.gettarinfo(p, arcname=rel)
            ti.uid = 0
            ti.gid = 0
            ti.uname = ""
            ti.gname = ""
            ti.mtime = 0
            if ti.isfile():
                with p.open("rb") as f:
                    tf.addfile(ti, f)
            else:
                tf.addfile(ti)


def compile_checked_hash_bytecode(
    image_dir: Path,
    packages_dir: Path,
    lib_dir: Path,
    python312_bin: str | None,
    include_stdlib: bool,
) -> dict[str, Any]:
    """Compile deterministic PEP 552 checked-hash CPython 3.12 .pyc files.

    Keeps all .py sources and package resources intact. Uses guest-relative
    dfile paths (/packages/... and /usr/local/lib/...) so output is 100%
    reproducible across macOS and Linux build hosts.
    """
    py_exe = python312_bin
    if not py_exe:
        for cand in ("python3.12", sys.executable):
            found = shutil.which(cand) if cand == "python3.12" else cand
            if not found:
                continue
            probe = subprocess.run(
                [found, "-c", "import importlib.util, sys; print(f'{sys.version_info[0]}.{sys.version_info[1]}:{importlib.util.MAGIC_NUMBER.hex()}')"],
                capture_output=True,
                text=True,
                check=False,
            )
            if probe.returncode == 0 and probe.stdout.strip() == f"3.12:{CPYTHON_312_MAGIC_HEX}":
                py_exe = found
                break
    if not py_exe:
        raise RuntimeError(
            "Generating CPython 3.12 checked-hash .pyc requires a CPython 3.12 interpreter "
            "(pass --pyc-python /path/to/python3.12)"
        )

    helper = """
import importlib.util, json, pathlib, py_compile, sys
assert sys.version_info[:2] == (3, 12), f"expected CPython 3.12, got {sys.version}"
assert importlib.util.MAGIC_NUMBER.hex() == "cb0d0d0a", f"unexpected magic {importlib.util.MAGIC_NUMBER.hex()}"
roots = json.loads(sys.argv[1])
compiled = 0
for host_root, guest_prefix in roots:
    root = pathlib.Path(host_root)
    for py_file in sorted(root.rglob("*.py")):
        rel = py_file.relative_to(root).as_posix()
        dfile = f"{guest_prefix}/{rel}"
        py_compile.compile(
            str(py_file),
            dfile=dfile,
            doraise=True,
            invalidation_mode=py_compile.PycInvalidationMode.CHECKED_HASH,
        )
        compiled += 1
print(compiled)
"""
    roots = [[str(packages_dir), "/packages"]]
    if include_stdlib:
        roots.append([str(lib_dir), "/usr/local/lib"])

    res = subprocess.run(
        [py_exe, "-I", "-c", helper, json.dumps(roots)],
        capture_output=True,
        text=True,
        check=False,
    )
    if res.returncode != 0:
        raise RuntimeError(f"CPython 3.12 checked-hash bytecode compilation failed:\n{res.stderr}")

    pyc_rels = sorted(
        p.relative_to(image_dir).as_posix()
        for p in image_dir.rglob("*.pyc")
        if p.is_file()
    )
    if not pyc_rels:
        raise RuntimeError("No .pyc files were produced by checked-hash bytecode compilation")

    # Verify every generated .pyc header has CPython 3.12 magic + checked-hash bitfield (0x03).
    for rel in pyc_rels:
        hdr = (image_dir / rel).read_bytes()[:8]
        if len(hdr) < 8 or hdr[:4].hex() != CPYTHON_312_MAGIC_HEX:
            raise RuntimeError(f"Invalid CPython 3.12 magic in {rel}")
        bitfield = struct.unpack("<I", hdr[4:8])[0]
        if (bitfield & 0x3) != 0x3:
            raise RuntimeError(f"Expected checked-hash bitfield 0x3 in {rel}, got 0x{bitfield:x}")

    pyc_tree_sha = sha256_tree(image_dir, pyc_rels)
    return {
        "invalidation_mode": "checked-hash",
        "cpython_version": "3.12",
        "magic_hex": CPYTHON_312_MAGIC_HEX,
        "pyc_count": len(pyc_rels),
        "pyc_tree_sha256": pyc_tree_sha,
    }


def main() -> None:
    ap = argparse.ArgumentParser(description="Build Portable Python CPython/WASI curated image")
    ap.add_argument("--lock", type=Path, default=Path(__file__).resolve().parent / "lock.json")
    ap.add_argument("--cache-dir", type=Path, required=True)
    ap.add_argument("--out-dir", type=Path, required=True)
    ap.add_argument(
        "--with-pyc",
        action="store_true",
        help="Generate deterministic CPython 3.12 checked-hash .pyc bytecode for packages",
    )
    ap.add_argument(
        "--with-stdlib-pyc",
        action="store_true",
        help="Also generate checked-hash .pyc bytecode for stdlib (implies --with-pyc)",
    )
    ap.add_argument(
        "--pyc-python",
        type=str,
        default=None,
        help="Path to CPython 3.12 executable used for checked-hash .pyc compilation",
    )
    ap.add_argument(
        "--metered-wasm-sha256",
        type=str,
        default="",
        help="Pinned SHA-256 of the InstrumentWasmFuel-metered python.wasm",
    )
    ap.add_argument(
        "--interpreter-archive",
        type=Path,
        default=None,
        help="Optional local path to verified CPython/WASI guest archive (e.g. python-3.12.0-wasi-py10.tar.gz)",
    )
    ap.add_argument(
        "--interpreter-wasm",
        type=Path,
        default=None,
        help="Optional override path to a rebuilt CPython/WASI python.wasm (e.g. PY10 with built-in Pillow/lxml)",
    )
    ap.add_argument(
        "--import-inventory-sha256",
        type=str,
        default="",
        help="Optional override for import_inventory_sha256 paired with --interpreter-wasm",
    )
    args = ap.parse_args()

    script_dir = Path(__file__).resolve().parent
    lock = json.loads(args.lock.read_text(encoding="utf-8"))

    image_dir = args.out_dir / "image"
    if image_dir.exists():
        shutil.rmtree(image_dir)
    bin_dir = image_dir / "bin"
    lib_dir = image_dir / "lib"
    packages_dir = image_dir / "packages"
    bin_dir.mkdir(parents=True)
    lib_dir.mkdir(parents=True)
    packages_dir.mkdir(parents=True)

    # 1. Fetch & extract CPython WASI interpreter + stdlib
    interp = lock["interpreter"]
    if args.interpreter_archive is not None:
        interp_tar = args.interpreter_archive
        got_tar_sha = sha256_file(interp_tar)
        if got_tar_sha != interp["source_sha256"]:
            raise RuntimeError(
                f"SHA-256 mismatch for {interp_tar}: expected {interp['source_sha256']}, got {got_tar_sha}"
            )
    else:
        interp_tar = download_verified(
            interp["source_url"],
            args.cache_dir / interp["filename"],
            interp["source_sha256"],
        )
    print(f"[interpreter] verified {interp_tar.name} ({interp['source_sha256'][:16]}...)")

    wasm_out = bin_dir / "python.wasm"
    with tarfile.open(interp_tar, "r:gz") as tf:
        for member in tf.getmembers():
            if not member.isfile():
                continue
            name = member.name.lstrip("./")
            if name in ("bin/python.wasm", "bin/python-3.12.0.wasm"):
                f = tf.extractfile(member)
                assert f is not None
                wasm_out.write_bytes(f.read())
                os.chmod(wasm_out, 0o755)
            elif name.startswith("usr/local/lib/") or name.startswith("lib/"):
                sub = name[len("usr/local/lib/") :] if name.startswith("usr/local/lib/") else name[len("lib/") :]
                if not sub or "/__pycache__/" in sub or sub.endswith(".pyc"):
                    continue
                out = lib_dir / sub
                out.parent.mkdir(parents=True, exist_ok=True)
                f = tf.extractfile(member)
                assert f is not None
                out.write_bytes(f.read())

    if args.interpreter_wasm is not None:
        shutil.copyfile(args.interpreter_wasm, wasm_out)
        os.chmod(wasm_out, 0o755)
        print(f"[interpreter] using override wasm {args.interpreter_wasm}")

    if not wasm_out.is_file():
        raise RuntimeError("Failed to extract python.wasm from interpreter archive")

    wasm_sha256 = sha256_file(wasm_out)
    stdlib_sha256 = sha256_tree(lib_dir)
    wasm_imports = parse_wasm_imports(wasm_out)
    print(f"[interpreter] python.wasm sha256={wasm_sha256[:16]}... imports={len(wasm_imports)}")
    print(f"[interpreter] stdlib sha256={stdlib_sha256[:16]}...")

    # 2. Install all locked distributions
    manifest_dists: list[dict[str, Any]] = []
    for dist in lock["distributions"]:
        kind = dist["source_kind"]
        if kind == "wheel":
            whl = download_verified(
                dist["source_url"],
                args.cache_dir / "packages" / dist["filename"],
                dist["source_sha256"],
            )
            dist_info_rel, installed = install_wheel(whl, packages_dir)
            src_url = dist["source_url"]
            src_sha = dist["source_sha256"]
        elif kind == "sdist_pure_python":
            sdist = download_verified(
                dist["source_url"],
                args.cache_dir / "packages" / dist["filename"],
                dist["source_sha256"],
            )
            dist_info_rel, installed = install_sdist_pure_python(sdist, dist, packages_dir)
            src_url = dist["source_url"]
            src_sha = dist["source_sha256"]
        elif kind == "wasi_pure_python":
            dist_info_rel, installed, src_sha = install_wasi_pure_python_rpds(
                script_dir, dist, packages_dir
            )
            src_url = dist["source_url"]
        else:
            raise RuntimeError(f"Unknown source_kind: {kind}")

        tree_sha = sha256_tree(packages_dir, installed)
        manifest_dists.append(
            {
                "name": dist["name"],
                "normalized_name": dist["normalized_name"],
                "version": dist["version"],
                "public": dist["public"],
                "imports": dist["imports"],
                "backend": dist["backend"],
                "features": dist["features"],
                "console_scripts": dist["console_scripts"],
                "has_package_data": dist["has_package_data"],
                "dist_info_path": f"packages/{dist_info_rel}",
                "license": dist["license"],
                "source_url": src_url,
                "source_sha256": src_sha,
                "tree_sha256": tree_sha,
            }
        )
        print(f"[package] {dist['name']}=={dist['version']} ({kind}) files={len(installed)}")

    # 2b. Apply minimal WASI compatibility patches (e.g. pytest get_user_id on wasi)
    applied_patches = apply_wasi_package_patches(packages_dir, record_hash)
    patched_rels = {p["target"].removeprefix("packages/") for p in applied_patches}
    for p in applied_patches:
        print(f"[patch] {p['target']}: {p['reason']}")
    if patched_rels:
        # Recompute tree_sha256 for every distribution whose RECORD contains a patched file
        for md in manifest_dists:
            rec_path = packages_dir / md["dist_info_path"].removeprefix("packages/") / "RECORD"
            rel_files = [
                line.split(",", 1)[0]
                for line in rec_path.read_text(encoding="utf-8").splitlines()
                if line.strip()
            ]
            if any(rf in patched_rels for rf in rel_files):
                md["tree_sha256"] = sha256_tree(packages_dir, rel_files)

    # 2c. Install pinned redistributable Unicode fonts (e.g. DejaVuSans for fpdf2)
    bundled_fonts = install_bundled_fonts(lock, args.cache_dir, packages_dir, download_verified, sha256_file)
    notices_path = write_third_party_notices(image_dir, manifest_dists, bundled_fonts)

    # 3. Optional deterministic checked-hash CPython 3.12 bytecode generation
    bytecode_spec: dict[str, Any] | None = None
    if args.with_pyc or args.with_stdlib_pyc:
        bytecode_spec = compile_checked_hash_bytecode(
            image_dir=image_dir,
            packages_dir=packages_dir,
            lib_dir=lib_dir,
            python312_bin=args.pyc_python,
            include_stdlib=args.with_stdlib_pyc,
        )
        stdlib_sha256 = sha256_tree(lib_dir)
        print(
            f"[bytecode] checked-hash CPython 3.12 pyc_count={bytecode_spec['pyc_count']} "
            f"pyc_tree={bytecode_spec['pyc_tree_sha256'][:16]}..."
        )

    # 3b. Verify ZERO host extension binaries in packages/
    assert_no_host_binaries(packages_dir)
    packages_tree_sha256 = sha256_tree(packages_dir)
    print(f"[verify] zero forbidden native/source files in packages/ (tree={packages_tree_sha256[:16]}...)")

    metered_wasm_sha256 = args.metered_wasm_sha256.strip().lower()
    if not metered_wasm_sha256 and wasm_sha256 == PINNED_CPYTHON_WASM_SHA256:
        metered_wasm_sha256 = PINNED_METERED_WASM_SHA256

    # 4. Compute deterministic image_id & write image-manifest.json
    id_hash = hashlib.sha256(
        f"{wasm_sha256}\n{stdlib_sha256}\n{packages_tree_sha256}\n".encode("ascii")
    ).hexdigest()[:16]
    image_id = f"pp-cpython-{interp['version']}-wasi-{id_hash}"
    source_date_epoch = os.environ.get("SOURCE_DATE_EPOCH")
    if source_date_epoch is not None and source_date_epoch.strip():
        built_at = dt.datetime.fromtimestamp(
            int(source_date_epoch.strip()), tz=dt.timezone.utc
        ).strftime("%Y-%m-%dT%H:%M:%SZ")
    else:
        built_at = lock.get("built_at") or "2026-11-26T00:00:00Z"

    manifest: dict[str, Any] = {
        "schema_version": "portable-python.image-manifest/v1",
        "image_id": image_id,
        "built_at": built_at,
        "target": interp["target"],
        "interpreter": {
            "name": interp["name"],
            "version": interp["version"],
            "wasm_path": "bin/python.wasm",
            "wasm_sha256": wasm_sha256,
            "metered_wasm_sha256": metered_wasm_sha256,
            "stdlib_path": "lib",
            "stdlib_sha256": stdlib_sha256,
            "build_inputs": {
                "release_tag": interp["release_tag"],
                "wasi_sdk": interp["wasi_sdk"],
                "source_url": interp["source_url"],
                "source_sha256": interp["source_sha256"],
            },
            "patches": applied_patches,
        },
        "packages_root": "packages",
        "packages_tree_sha256": packages_tree_sha256,
        "distributions": manifest_dists,
        "bundled_fonts": bundled_fonts,
        "notices_path": notices_path,
        "stdlib_capabilities": STDLIB_CAPABILITIES,
        "wasm_imports": wasm_imports,
        "import_inventory_sha256": args.import_inventory_sha256.strip().lower() or PINNED_IMPORT_INVENTORY_SHA256,
    }
    if bytecode_spec is not None:
        manifest["bytecode"] = bytecode_spec

    manifest_text = json.dumps(manifest, indent=2) + "\n"
    (image_dir / "image-manifest.json").write_text(manifest_text, encoding="utf-8")
    (args.out_dir / "image-manifest.json").write_text(manifest_text, encoding="utf-8")

    # 5. Pack deterministic tarball & write SHA256SUMS
    tarball_path = args.out_dir / "portable-python-image.tar.gz"
    pack_deterministic_tar_gz(image_dir, tarball_path)

    sums = [
        f"{sha256_file(tarball_path)}  {tarball_path.name}",
        f"{sha256_file(args.out_dir / 'image-manifest.json')}  image-manifest.json",
    ]
    (args.out_dir / "SHA256SUMS").write_text("\n".join(sums) + "\n", encoding="utf-8")

    print(f"[done] image_id={image_id}")
    print(f"[done] tarball={tarball_path} ({tarball_path.stat().st_size} bytes)")


if __name__ == "__main__":
    main()
