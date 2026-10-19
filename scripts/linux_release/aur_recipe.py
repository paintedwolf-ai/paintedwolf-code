"""Generate a release-pinned AUR recipe from verified native pacman generations."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import shlex
import subprocess
from urllib.parse import urlsplit

from .openpgp import selection
from .pacman_validation import admit


def quoted_array(name: str, values: list[str]) -> str:
    return name + "=(" + " ".join(shlex.quote(value) for value in values) + ")\n"


def metadata(package: Path) -> tuple[dict, bytes | None]:
    raw = subprocess.check_output(["bsdtar", "-xOf", str(package), ".PKGINFO"], timeout=60, text=True)
    fields = {}
    for line in raw.splitlines():
        if line.startswith("#") or not line.strip():
            continue
        key, separator, value = line.partition(" = ")
        if not separator:
            raise ValueError("invalid native metadata framing")
        fields.setdefault(key, []).append(value)
    names = subprocess.check_output(["bsdtar", "-tf", str(package)], timeout=60, text=True).splitlines()
    install = (subprocess.check_output(["bsdtar", "-xOf", str(package), ".INSTALL"], timeout=60)
               if ".INSTALL" in names else None)
    return fields, install


def generate(generations: list[tuple[dict, Path]], destination: Path, base_url: str, catalog: Path,
             rehearsal=False, package="painted-wolf-code") -> dict:
    origin = urlsplit(base_url)
    if origin.scheme != "https" or not origin.netloc or origin.query or origin.fragment or origin.username:
        raise ValueError("AUR sources require an HTTPS origin without credentials or query")
    if not generations:
        raise ValueError("AUR recipe needs selected native generations")
    selected, _ = selection(catalog, rehearsal)
    first = generations[0][0]
    if not re.fullmatch(r"[0-9A-Za-z.+~-]+", first["version"]):
        raise ValueError("invalid AUR release version")
    architectures, rows, common, lifecycle = set(), [], None, None
    for receipt, directory in generations:
        admit(receipt, directory, catalog, rehearsal)
        if any(receipt[name] != first[name] for name in ["version", "source_commit", "channel"]):
            raise ValueError("AUR architectures differ in release identity")
        architecture = receipt["architecture"]
        if architecture in architectures:
            raise ValueError("AUR architecture is repeated")
        architectures.add(architecture)
        packages = [row for row in receipt["objects"] if row["path"].endswith(".pkg.tar.zst")]
        if len(packages) != 1:
            raise ValueError("AUR release needs exactly one native package per architecture")
        row = packages[0]
        info, install = metadata(directory / row["path"])
        if info.get("pkgname") != [package]:
            raise ValueError("AUR input is not the selected application package")
        comparable = {name: info.get(name, []) for name in
                      ["pkgname", "pkgver", "pkgdesc", "url", "license", "depend", "optdepend",
                       "provides", "conflict", "replaces", "backup"]}
        if common is None:
            common, lifecycle = comparable, install
        elif common != comparable or lifecycle != install:
            raise ValueError("AUR inputs disagree on shared package metadata or lifecycle")
        identity = hashlib.sha256((json.dumps(receipt, sort_keys=True, indent=2) + "\n").encode()).hexdigest()
        rows.append((architecture, row, base_url.rstrip("/") + "/generations/" + identity))
    if not rehearsal and architectures != {"x86_64", "aarch64"}:
        raise ValueError("production AUR recipe requires both supported architectures")
    native = common["pkgver"]
    if len(native) != 1 or "-" not in native[0]:
        raise ValueError("AUR input needs one native package version and release")
    version, release = native[0].rsplit("-", 1)
    epoch = None
    if ":" in version:
        epoch, version = version.split(":", 1)
        if not epoch.isdecimal():
            raise ValueError("invalid native package epoch")
    if not re.fullmatch(r"[0-9A-Za-z.+_]+", version) or not re.fullmatch(r"[1-9][0-9]*(?:\.[0-9]+)?", release):
        raise ValueError("native version cannot be represented by a pinned AUR recipe")
    name = package + ("-preview" if first["channel"] == "preview" else "") + "-bin"
    text = ("# Generated from authenticated native release inputs; review before AUR publication.\n"
            f"pkgname={shlex.quote(name)}\npkgver={shlex.quote(version)}\npkgrel={release}\n"
            "pkgdesc='Painted Wolf Code'\nurl='https://paintedwolf.ai'\n")
    if epoch is not None:
        text += "epoch=" + epoch + "\n"
    text += quoted_array("arch", sorted(architectures))
    text += quoted_array("validpgpkeys", [selected["fingerprint"]])
    text += quoted_array("options", ["!strip", "!debug"])
    for output, source in [("license", "license"), ("depends", "depend"), ("optdepends", "optdepend"),
                           ("backup", "backup"), ("replaces", "replaces")]:
        text += quoted_array(output, common[source])
    text += quoted_array("provides", [package + "=" + native[0], *common["provides"]])
    text += quoted_array("conflicts", sorted({package, package + "-preview", *common["conflict"]}))
    if lifecycle is not None:
        text += "install=painted-wolf-code.install\n"
    text += quoted_array("noextract", [row["path"] for _, row, _ in sorted(rows)])
    for architecture, row, url in sorted(rows):
        filename = row["path"]
        text += quoted_array("source_" + architecture, [url + "/" + filename, url + "/" + filename + ".sig"])
        text += quoted_array("sha256sums_" + architecture, [row["sha256"], "SKIP"])
    text += """package() {
  local archive
  archive="${source_x86_64[0]##*/}"
  if [[ "$CARCH" == aarch64 ]]; then archive="${source_aarch64[0]##*/}"; fi
  bsdtar -xf "$srcdir/$archive" -C "$pkgdir" \\
    --exclude=.PKGINFO --exclude=.BUILDINFO --exclude=.MTREE --exclude=.INSTALL
}
"""
    destination.mkdir(parents=True, exist_ok=False)
    (destination / "PKGBUILD").write_text(text)
    if lifecycle is not None:
        (destination / "painted-wolf-code.install").write_bytes(lifecycle)
    srcinfo = subprocess.check_output(["makepkg", "--printsrcinfo"], cwd=destination)
    (destination / ".SRCINFO").write_bytes(srcinfo)
    return {"format_version": 1, "source_commit": first["source_commit"], "version": first["version"],
            "channel": first["channel"], "package": name, "rehearsal": rehearsal,
            "aur_published": False, "architectures": sorted(architectures)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--generation", type=Path, action="append", required=True)
    parser.add_argument("--destination", type=Path, required=True)
    parser.add_argument("--catalog", type=Path, required=True)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--rehearsal", action="store_true")
    args = parser.parse_args()
    try:
        generations = [(json.loads((path / "generation.json").read_text()), path) for path in args.generation]
        print(json.dumps(generate(generations, args.destination, args.base_url, args.catalog, args.rehearsal), indent=2))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"AUR recipe generation failed: {exc}\n")


if __name__ == "__main__":
    main()
