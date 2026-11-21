"""Stage the private service guest and its host-specific launch resources."""

import argparse
import fcntl
import gzip
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import stat
import struct
import subprocess
import tempfile
import urllib.request

from initramfs import Entry, pack
from userland import add_postgres, base_userland
from postgres import build_postgres

ROOT = Path(__file__).resolve().parents[2]
LOCK = ROOT / "packaging/service-guest/inputs.json"
HERE = Path(__file__).resolve().parent


def sha(path):
    h = hashlib.sha256()
    with path.open("rb") as f:
        for block in iter(lambda: f.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def acquire(spec, cache):
    digest = spec["sha256"]
    artifact = cache / digest
    if artifact.exists() and sha(artifact) == digest:
        return artifact
    with tempfile.NamedTemporaryFile(dir=cache, delete=False) as tmp:
        candidate = Path(tmp.name)
        try:
            with urllib.request.urlopen(spec["url"], timeout=90) as source:
                shutil.copyfileobj(source, tmp)
            tmp.flush()
            if sha(candidate) != digest:
                raise ValueError(f"download digest mismatch for {spec['url']}")
            os.replace(candidate, artifact)
        finally:
            candidate.unlink(missing_ok=True)
    return artifact


def boot_image(image, arch, output, tree, work, driver_tool):
    modloop = work / "modloop"
    modules = work / "modules"
    modules.mkdir()
    for member, file in [
        ("boot/vmlinuz-virt", output / "kernel"),
        ("boot/modloop-virt", modloop),
    ]:
        with file.open("wb") as f:
            subprocess.run(
                ["bsdtar", "-xOf", str(image), member], stdout=f, check=True
            )
    if arch == "arm64":
        kernel = output / "kernel"
        zboot = kernel.read_bytes()
        if zboot[4:8] != b"zimg" or zboot[24:28] != b"gzip":
            raise ValueError("unexpected arm64 EFI zboot payload")
        start, size = struct.unpack_from("<II", zboot, 8)
        raw = gzip.decompress(zboot[start : start + size])
        if raw[56:60] != b"ARMd":
            raise ValueError("invalid arm64 Linux Image")
        kernel.write_bytes(raw)
    subprocess.run(
        [*driver_tool, str(modloop), str(modules)],
        cwd=ROOT / "lycaon",
        check=True,
    )
    for module in modules.iterdir():
        tree["lib/pw-boot-modules/" + module.name] = Entry(
            stat.S_IFREG | 0o444, module.read_bytes()
        )


def stage_rootfs(tree, dest):
    dest.mkdir()
    for name, entry in sorted(tree.items()):
        p = dest / name
        p.parent.mkdir(parents=True, exist_ok=True)
        if stat.S_ISDIR(entry.mode):
            p.mkdir(exist_ok=True)
        elif stat.S_ISREG(entry.mode):
            p.write_bytes(entry.data)
            p.chmod(entry.mode & 0o555)
    for parent, _, _ in os.walk(dest):
        os.chmod(parent, 0o755)
    # Guest links are installed after every traversal of the host staging path.
    for name, entry in sorted(tree.items()):
        if stat.S_ISLNK(entry.mode):
            (dest / name).symlink_to(os.fsdecode(entry.data))


def assemble(args, work, output):
    lock = json.loads(LOCK.read_text())
    machine = {"arm64": "aarch64", "amd64": "x86_64"}[args.arch]
    host_machine = {"arm64": "aarch64", "aarch64": "aarch64", "x86_64": "x86_64"}[
        platform.machine()
    ]
    host_os = {"Darwin": "macos", "Linux": "linux"}[platform.system()]
    pin = lock["architectures"][args.arch]
    tree = base_userland(acquire(pin["base"], args.cache), machine)
    pg = build_postgres(
        acquire(lock["postgres"], args.cache),
        acquire(lock["compilers"][host_machine + "-" + host_os], args.cache),
        machine,
        tree[f"lib/ld-musl-{machine}.so.1"],
        args.cache,
    )
    add_postgres(tree, pg)
    guest = args.guest_binary or work / "init"
    if args.guest_binary is None:
        subprocess.run(
            [
                "go",
                "build",
                "-trimpath",
                "-buildvcs=false",
                "-ldflags=-s -w -buildid=",
                "-o",
                str(guest),
                "./cmd/pw-service-guest",
            ],
            cwd=ROOT / "lycaon",
            env=os.environ | {"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": args.arch},
            check=True,
        )
    tree["init"] = Entry(stat.S_IFREG | 0o555, guest.read_bytes())
    for mountpoint in (
        "dev",
        "proc",
        "sys",
        "tmp",
        "home/service/.config/paintedwolf/scratch/session",
    ):
        tree[mountpoint] = Entry(stat.S_IFDIR | 0o755)
    tree["work"] = Entry(
        stat.S_IFLNK | 0o777, b"home/service/.config/paintedwolf/scratch/session"
    )
    manifest = {"schema": 1, "architecture": args.arch}
    driver_tool = [args.driver_tool] if args.driver_tool else ["go", "run", "./cmd/service-guest-drivers"]
    boot_image(acquire(pin["boot"], args.cache), args.arch, output, tree, work, driver_tool)
    manifest["kernel_sha256"] = sha(output / "kernel")
    pack(tree, output / "initramfs.gz")
    manifest["initramfs_sha256"] = sha(output / "initramfs.gz")
    if args.system == "linux":
        manifest["qemu_path"] = args.qemu_path or f"/usr/bin/qemu-system-{machine}"
        stage_rootfs(tree, output / "rootfs")
    else:
        helper = output / "pw-service-vm"
        swift = sorted((ROOT / "packaging/macos/private-guest-host").glob("*.swift"))
        subprocess.run(
            [
                "xcrun",
                "swiftc",
                "-parse-as-library",
                "-O",
                "-target",
                machine.replace("aarch64", "arm64") + "-apple-macos14.0",
                "-o",
                str(helper),
                *swift,
            ],
            check=True,
        )
        identity = os.environ.get("APPLE_SIGNING_IDENTITY") if args.release else "-"
        helper.chmod(0o755)
        if not identity or (args.release and identity == "-"):
            raise ValueError("missing Developer ID for private service helper")
        timestamp = ["--timestamp"] if args.release else []
        subprocess.run(
            [
                "codesign",
                "--force",
                "--options",
                "runtime",
                "--identifier",
                "dev.paintedwolf.code.service-vm",
                "--entitlements",
                str(ROOT / "packaging/macos/service-guest-entitlements.plist"),
                *timestamp,
                "--sign",
                identity,
                str(helper),
            ],
            check=True,
        )
        subprocess.run(["codesign", "--verify", "--strict", str(helper)], check=True)
        signature = subprocess.run(
            ["codesign", "-dv", "--verbose=4", str(helper)],
            capture_output=True,
            text=True,
            check=True,
        )
        cdhash = re.search(r"^CDHash=([0-9a-f]{40})$", signature.stderr, re.M)
        if not cdhash:
            raise ValueError("missing private service helper code directory hash")
        (output / "pw-service-vm.cdhash").write_text(cdhash[1] + "\n")
    (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    shutil.copyfile(LOCK, output / "inputs.json")
    output.chmod(0o755)
    for item in output.iterdir():
        if item.is_file() and item.name != "pw-service-vm":
            item.chmod(0o644)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--system", choices=("darwin", "linux"), required=True)
    p.add_argument("--arch", choices=("arm64", "amd64"), required=True)
    p.add_argument("--output", type=Path, required=True)
    p.add_argument(
        "--cache", type=Path, default=Path.home() / ".cache/paintedwolf/service-guest"
    )
    p.add_argument("--release", action="store_true")
    p.add_argument("--guest-binary", type=Path, help="package-built Linux supervisor")
    p.add_argument("--driver-tool", type=Path, help="package-built boot driver extractor")
    p.add_argument("--qemu-path", help="package-owned native QEMU executable")
    args = p.parse_args()
    args.cache = args.cache.resolve()
    dest = args.output.absolute()
    args.cache.mkdir(parents=True, exist_ok=True)
    dest.parent.mkdir(parents=True, exist_ok=True)
    with (
        (args.cache / "build.lock").open("a") as lock,
        tempfile.TemporaryDirectory(dir=dest.parent) as scratch,
    ):
        fcntl.flock(lock, fcntl.LOCK_EX)
        work = Path(scratch)
        stage = work / "stage"
        stage.mkdir()
        assemble(args, work, stage)
        old = work / "previous"
        if dest.exists():
            if not (dest / "manifest.json").is_file():
                raise ValueError(f"unrecognized guest staging directory {dest}")
            dest.rename(old)
        try:
            stage.rename(dest)
        except OSError:
            if old.exists():
                old.rename(dest)
            raise


if __name__ == "__main__":
    main()
