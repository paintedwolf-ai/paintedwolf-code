"""Committed signed pacman generations and the native tools that rebuild them.

The fixtures under `testdata/pacman-repositories/` are real `assemble()` output signed by a
non-expiring rehearsal key whose private half was discarded. Regenerate them on an Arch-family
host (`repo-add --include-sigs` needs pacman 6.1 or later) as an unprivileged user:
`python3 -m scripts.verification_tests.distribution.pacman_repository_fixtures`. Assembly itself
needs that pacman, so `release:rehearse:pacman-repositories` qualifies it on Arch; these tests
admit, publish and generate recipes from its committed output.
"""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile

from scripts.linux_release.pacman_repository import assemble
from scripts.linux_release.repository_objects import encoded
from scripts.linux_release.signing import digest
from scripts.verification_tests.distribution import repository_fixture_support as support
from scripts.verification_tests.distribution.repository_fixture_support import native_tools, signing_key

TESTDATA = Path(__file__).resolve().parent / "testdata/pacman-repositories"
PACKAGE = "paintedwolf-repository-fixture"
ARCHITECTURES = ["x86_64", "aarch64"]


def generation(name: str, work: Path) -> tuple:
    return support.generation(TESTDATA, name, work)


def trust(work: Path, name="keys.json", rehearsal=True) -> Path:
    return support.trust(TESTDATA, work, name, rehearsal)


def package_name(version: str, architecture="x86_64") -> str:
    return f"{PACKAGE}-{version}-1-{architecture}.pkg.tar.zst"


def inputs(work: Path) -> Path:
    directory = work / "inputs"
    shutil.copytree(TESTDATA / "inputs", directory)
    return directory


def manifest(directory: Path, version="0.0.1", architecture="x86_64", **changes) -> dict:
    path = directory / package_name(version, architecture)
    row = {"file": path.name, "sha256": digest(path), "native_version": version + "-1"}
    return {"format_version": 1, "source_commit": "f" * 40, "version": version, "architecture": architecture,
            "packages": [{**row, **changes}]}


def build(work: Path, version: str, architecture: str) -> Path:
    """A tiny package whose payload names its version, built for the selected architecture."""
    source = work / f"{version}-{architecture}"
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
    configuration = source / "makepkg.conf"
    configuration.write_text(Path("/etc/makepkg.conf").read_text() + f"\nCARCH={architecture}\nPKGEXT=.pkg.tar.zst\n")
    subprocess.run(["makepkg", "--config", str(configuration), "--nodeps", "--noconfirm", "--force", "--nosign"],
                   cwd=source, check=True, stdout=subprocess.DEVNULL)
    return source / package_name(version, architecture)


def regenerate(destination=TESTDATA):
    native_tools("makepkg", "repo-add", "bsdtar", "gpg")
    with tempfile.TemporaryDirectory(prefix="paintedwolf-pacman-fixtures-") as temporary:
        work = Path(temporary)
        keys = work / "keys"
        catalog = signing_key(keys, "fixture", "keys.json")
        signing_key(keys, "other", "other-keys.json")
        output = work / "output"
        (output / "inputs").mkdir(parents=True)
        for name in ["keys.json", "fixture-public.asc", "other-keys.json", "other-public.asc"]:
            shutil.copyfile(keys / name, output / name)
        for version, architecture in [("0.0.1", "x86_64"), ("0.0.2", "x86_64"), ("0.0.1", "aarch64")]:
            package = build(work, version, architecture)
            shutil.copyfile(package, output / "inputs" / package.name)
            target = output / f"pacman-{version}-{architecture}"
            receipt = assemble(manifest(output / "inputs", version, architecture), output / "inputs", target,
                               "preview", catalog, keys / "fixture-secret.asc", True, PACKAGE)
            (target / "generation.json").write_bytes(encoded(receipt))
        if destination.exists():
            shutil.rmtree(destination)
        shutil.copytree(output, destination, symlinks=True)
    for path in destination.rglob("*"):
        if not path.is_symlink():
            os.chmod(path, 0o755 if path.is_dir() else 0o644)


if __name__ == "__main__":
    regenerate()
