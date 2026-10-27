"""Build, verify and install a generated AUR recipe in isolated makepkg, pacman and yay roots."""

from __future__ import annotations

import os
from pathlib import Path
import shutil
import subprocess

from .aur_recipe import generate
from .openpgp import keyring, selection
from .pacman_installation import PACKAGE, install
from .pacman_repository import binary_signature, package_info
from .yay_rehearsal import install as install_yay


def rehearse(work: Path, receipt: dict, destination: Path, package: Path, catalog: Path, wrong: Path,
             version: str, architecture: str):
    recipe = work / ("aur-" + version)
    generated = generate([(receipt, destination)], recipe, "https://repository.example.invalid", catalog,
                         True, PACKAGE)
    for row in receipt["objects"]:
        if row["path"].endswith((".pkg.tar.zst", ".pkg.tar.zst.sig")):
            shutil.copyfile(destination / row["path"], recipe / row["path"])
    selected_key, public = selection(catalog, True)
    with keyring(public, selected_key["fingerprint"]) as home:
        subprocess.run(["makepkg", "--nodeps", "--noconfirm", "--cleanbuild", "--force", "--nosign"],
                       cwd=recipe, env={**os.environ, "GNUPGHOME": str(home)}, check=True)
        altered = recipe / package.name
        original = altered.read_bytes()
        altered.write_bytes(original + b"changed")
        invalid = subprocess.run(["makepkg", "--verifysource", "--force", "--noconfirm"], cwd=recipe,
                                 env={**os.environ, "GNUPGHOME": str(home)})
        altered.write_bytes(original)
        if invalid.returncode == 0:
            raise ValueError("AUR makepkg accepted changed upstream package bytes")
    wrong_key, wrong_public = selection(wrong, True)
    with keyring(wrong_public, wrong_key["fingerprint"]) as home:
        invalid = subprocess.run(["makepkg", "--verifysource", "--force", "--noconfirm"], cwd=recipe,
                                 env={**os.environ, "GNUPGHOME": str(home)})
        if invalid.returncode == 0:
            raise ValueError("AUR makepkg accepted an unavailable upstream signing key")
    rebuilt = list(recipe.glob(PACKAGE + "-preview-bin-*.pkg.tar.zst"))
    if len(rebuilt) != 1 or package_info(rebuilt[0]) != {
            "pkgname": generated["package"], "pkgver": version + "-1", "arch": architecture}:
        raise ValueError("AUR recipe did not preserve the selected release identity")
    binary_signature(rebuilt[0], rebuilt[0].with_name(rebuilt[0].name + ".sig"), catalog,
                     catalog.parent / "rehearsal-secret.asc", True)
    install_yay(recipe, work / "yay-root", catalog, generated["package"], version)
    install(rebuilt[0], work / "aur-root", catalog, generated["package"])
    if (work / "aur-root/usr/share" / PACKAGE / "value").read_text() != version + "\n":
        raise ValueError("AUR pacman upgrade did not install the selected payload")
    content = subprocess.check_output(["bsdtar", "-xOf", str(rebuilt[0]),
                                       f"usr/share/{PACKAGE}/value"], timeout=60)
    if content != (version + "\n").encode():
        raise ValueError("AUR recipe changed the authenticated upstream payload")
    if "pkgname = " + generated["package"] not in (recipe / ".SRCINFO").read_text():
        raise ValueError("generated SRCINFO differs from the selected AUR package")
