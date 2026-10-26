"""Exercise real native makepkg construction and final-byte admission with a sealed tiny fixture."""

import argparse
import hashlib
import json
from pathlib import Path
import platform
import shutil
import struct
import subprocess
import tempfile

from .arch import recipe, stream_digest, write_payload
from .arch_stage import extract
from .arch_substrate import build, rootfs
from release_semver import pacman_package_version, parse, parse_release_build

ROOT = Path(__file__).resolve().parents[2]


def rehearse(work):
    architecture = platform.machine()
    if platform.system() != "Linux" or architecture not in {"x86_64", "aarch64"}:
        raise ValueError("construction rehearsal requires native Linux amd64 or arm64")
    tree, recipe_dir = work / "fixture-tree", work / "recipe"
    recipe_dir.mkdir()
    executable = tree / "opt/painted-wolf-code/bin/pw"
    executable.parent.mkdir(parents=True)
    shutil.copyfile("/usr/bin/true", executable)
    executable.chmod(0o755)
    header = executable.read_bytes()[:20]
    machine = struct.unpack("<H", header[18:20])[0]
    if header[:6] != b"\x7fELF\x02\x01" or machine != {"x86_64": 62, "aarch64": 183}[architecture]:
        raise ValueError("fixture executable is not native ELF64")
    document = tree / "usr/share/paintedwolf-fixture/Unicode λ with spaces"
    document.parent.mkdir(parents=True)
    document.write_text("explicit nonapplication fixture\n")
    link = document.parent / "native-link"
    link.symlink_to("../../../opt/painted-wolf-code/bin/pw")
    resources = []
    for name in ["usr/share/man/man1/fixture.1", "usr/lib/fixture.a", "usr/lib/fixture.la"]:
        path = tree / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("sealed nonapplication resource\n")
        resources.append(path)
    sealed = {path.relative_to(tree).as_posix(): stream_digest(path) for path in [executable, document, *resources]}
    product = (ROOT / "VERSION").read_text().strip()
    revision = parse_release_build((ROOT / "RELEASE_BUILD").read_text().strip())
    payload = recipe_dir / "fixture-payload.tar.xz"
    write_payload(tree, payload, 1234567890)
    (recipe_dir / "PKGBUILD").write_text(recipe(ROOT, payload, architecture, product))
    hook = ROOT / "packaging/linux/arch/painted-wolf-code.install"
    if hook.exists():
        shutil.copyfile(hook, recipe_dir / hook.name)
    arm = json.loads((ROOT / "packaging/linux/arch/substrates.json").read_text())["aarch64"]
    authenticated_arm = rootfs(ROOT, arm)
    substrate = build(ROOT, recipe_dir, architecture, 1234567890)
    package = recipe_dir / ("painted-wolf-code-" + pacman_package_version(parse(product)) + "-" + str(revision)
                            + "-" + architecture + ".pkg.tar.zst")
    destination = work / "extracted"
    fields = extract(package, destination, ROOT, architecture, product)
    for relative, expected in sealed.items():
        if stream_digest(destination / relative) != expected:
            raise ValueError("native package changed a sealed fixture payload")
    if (destination / link.relative_to(tree)).readlink() != link.readlink():
        raise ValueError("native package changed the selected fixture symlink")
    if (destination / executable.relative_to(tree)).stat().st_mode & 0o777 != 0o755:
        raise ValueError("native package changed executable mode")
    return {"format_version": 1, "rehearsal": True, "application_qualified": False,
            "published": False, "source_commit": subprocess.check_output(
                ["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
            "source_tree": subprocess.check_output(["git", "rev-parse", "HEAD^{tree}"], cwd=ROOT, text=True).strip(),
            "architecture": architecture, "substrate": substrate,
            "authenticated_arm_rootfs_sha256": stream_digest(authenticated_arm), "package_sha256": stream_digest(package),
            "package_bytes": package.stat().st_size, "native_version": fields["pkgver"][0],
            "sealed_fixture": sealed,
            "cases": ["actual-native-makepkg", "authenticated-ARM-rootfs-preparation", "pinned-official-substrate", "signed-toolchain-preparation",
                      "offline-unprivileged-packaging", "native-ELF-bytes-preserved", "final-PKGINFO-admission",
                      "exact-dependency-catalog", "native-BUILDINFO-and-MTREE", "unicode-modes-and-links-preserved", "manual-static-and-libtool-resources-preserved"]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--receipt", type=Path, required=True)
    args = parser.parse_args()
    if args.receipt.exists() or args.receipt.is_symlink():
        parser.error("construction receipt requires a new output")
    with tempfile.TemporaryDirectory(prefix="paintedwolf-pacman-construction-") as temporary:
        report = rehearse(Path(temporary))
    args.receipt.parent.mkdir(parents=True, exist_ok=True)
    with args.receipt.open("x") as output:
        output.write(json.dumps(report, indent=2, sort_keys=True) + "\n")


if __name__ == "__main__":
    main()
