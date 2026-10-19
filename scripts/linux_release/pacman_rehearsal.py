"""Rehearse native makepkg and signed pacman repository formats without publication."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile

from .openpgp import ephemeral, keyring, selection, run
from .pacman_repository import assemble, package_info, binary_signature
from .aur_recipe import generate
from .pacman_validation import admit
from .signing import digest

PACKAGE = "paintedwolf-repository-fixture"


def rejected(action):
    try:
        action()
    except (ValueError, OSError, KeyError):
        return
    raise ValueError("invalid pacman generation was admitted")


def install(package: Path, root: Path, catalog: Path, expected: str):
    root.mkdir(exist_ok=True)
    for name in ["db", "cache", "hooks"]:
        (root / name).mkdir(exist_ok=True)
    config = root / "pacman.conf"
    config.write_text("[options]\nArchitecture = " + platform.machine()
                      + "\nSigLevel = Required DatabaseRequired\nLocalFileSigLevel = Required\n")
    selected, public = selection(catalog, True)
    with keyring(public, selected["fingerprint"]) as home:
        run(["gpg", "--batch", "--import-ownertrust"], home,
            (selected["fingerprint"] + ":6:\n").encode())
        run(["gpg", "--batch", "--check-trustdb"], home)
        try:
            subprocess.run(["sudo", "-n", "pacman", "--config", str(config), "--root", str(root),
                            "--dbpath", str(root / "db"), "--cachedir", str(root / "cache"),
                            "--hookdir", str(root / "hooks"), "--gpgdir", str(home),
                            "--logfile", str(root / "pacman.log"), "-U", "--noconfirm", str(package)], check=True)
        finally:
            subprocess.run(["sudo", "-n", "chown", "-R", f"{os.getuid()}:{os.getgid()}", "--", str(root)], check=True)
    owned = subprocess.check_output(["pacman", "--root", str(root), "--dbpath", str(root / "db"),
                                     "-Qoq", "--", str(root / "usr/share" / PACKAGE / "value")], text=True).strip()
    if owned != expected:
        raise ValueError("installed fixture file has the wrong package owner")


def rehearse(work: Path):
    os.environ.pop("LINUX_PACKAGE_SIGNING_KEY", None)
    architecture = platform.machine()
    catalog = ephemeral(work / "key")
    wrong = ephemeral(work / "wrong")
    results = []
    for version in ["0.0.1", "0.0.2"]:
        source = work / version
        source.mkdir()
        (source / "PKGBUILD").write_text(f"""pkgname={PACKAGE}
pkgver={version}
pkgrel=1
pkgdesc='Disposable repository fixture'
arch=('{architecture}')
license=('MIT')
options=('!debug' '!strip')
package() {{
  install -d "$pkgdir/usr/share/{PACKAGE}"
  printf '%s\\n' '{version}' > "$pkgdir/usr/share/{PACKAGE}/value"
}}
""")
        subprocess.run(["makepkg", "--nodeps", "--noconfirm", "--cleanbuild", "--force"],
                       cwd=source, check=True)
        packages = list(source.glob("*.pkg.tar.zst"))
        if len(packages) != 1:
            raise ValueError("native fixture construction is ambiguous")
        package = packages[0]
        manifest = {"format_version": 1, "source_commit": "f" * 40, "version": version,
                    "architecture": architecture, "packages": [{"file": package.name,
                    "sha256": digest(package), "native_version": version + "-1"}]}
        destination = work / ("repository-" + version)
        receipt = assemble(manifest, source, destination, "preview", catalog,
                           catalog.parent / "rehearsal-secret.asc", True, PACKAGE)
        admit(receipt, destination, catalog, True)
        rejected(lambda: admit(receipt, destination, wrong, True))
        rejected(lambda: admit(receipt, destination, catalog))
        rejected(lambda: admit({**receipt, "channel": "stable"}, destination, catalog, True))
        selected = destination / package.name
        raw = selected.read_bytes()
        selected.write_bytes(raw + b"changed")
        rejected(lambda: admit(receipt, destination, catalog, True))
        selected.write_bytes(raw)
        absent = selected.with_name(".withheld-" + selected.name)
        selected.rename(absent)
        rejected(lambda: admit(receipt, destination, catalog, True))
        absent.rename(selected)
        admit(receipt, destination, catalog, True)
        install(destination / package.name, work / "native-root", catalog, PACKAGE)
        if (work / "native-root/usr/share" / PACKAGE / "value").read_text() != version + "\n":
            raise ValueError("native pacman upgrade did not install the selected payload")
        recipe = work / ("aur-" + version)
        generated = generate([(receipt, destination)], recipe, "https://repository.example.invalid", catalog,
                             True, PACKAGE)
        for row in receipt["objects"]:
            if row["path"].endswith((".pkg.tar.zst", ".pkg.tar.zst.sig")):
                shutil.copyfile(destination / row["path"], recipe / row["path"])
        selected_key, public = selection(catalog, True)
        with keyring(public, selected_key["fingerprint"]) as home:
            subprocess.run(["makepkg", "--nodeps", "--noconfirm", "--cleanbuild", "--force"],
                           cwd=recipe, env={**os.environ, "GNUPGHOME": str(home)}, check=True)
        rebuilt = list(recipe.glob(PACKAGE + "-preview-bin-*.pkg.tar.zst"))
        if len(rebuilt) != 1 or package_info(rebuilt[0]) != {
                "pkgname": generated["package"], "pkgver": version + "-1", "arch": architecture}:
            raise ValueError("AUR recipe did not preserve the selected release identity")
        binary_signature(rebuilt[0], rebuilt[0].with_name(rebuilt[0].name + ".sig"), catalog,
                         catalog.parent / "rehearsal-secret.asc", True)
        install(rebuilt[0], work / "aur-root", catalog, generated["package"])
        if (work / "aur-root/usr/share" / PACKAGE / "value").read_text() != version + "\n":
            raise ValueError("AUR pacman upgrade did not install the selected payload")
        content = subprocess.check_output(["bsdtar", "-xOf", str(rebuilt[0]),
                                           f"usr/share/{PACKAGE}/value"], timeout=60)
        if content != (version + "\n").encode():
            raise ValueError("AUR recipe changed the authenticated upstream payload")
        if "pkgname = " + generated["package"] not in (recipe / ".SRCINFO").read_text():
            raise ValueError("generated SRCINFO differs from the selected AUR package")
        results.append({"version": version, "objects": len(receipt["objects"])})
    return {"format_version": 1, "rehearsal": True, "application_qualified": False,
            "published": False, "generations": results,
            "cases": ["native-makepkg", "signed-database-and-files", "embedded-package-signature",
                      "wrong-key", "production-refuses-fixture", "changed-package", "missing-object",
                      "channel-mismatch", "retained-generations", "pinned-aur-makepkg",
                      "aur-input-signature-and-checksum", "aur-srcinfo", "aur-payload-identity",
                      "native-installed-upgrade", "aur-installed-upgrade", "actual-pacman-file-ownership"]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--receipt", type=Path, required=True)
    args = parser.parse_args()
    try:
        with tempfile.TemporaryDirectory(prefix="paintedwolf-pacman-rehearsal-") as temporary:
            result = rehearse(Path(temporary))
        args.receipt.parent.mkdir(parents=True, exist_ok=True)
        args.receipt.write_text(json.dumps(result, indent=2) + "\n")
        print(f"Pacman repository rehearsal passed: {args.receipt}")
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Pacman repository rehearsal failed: {exc}\n")


if __name__ == "__main__":
    main()
