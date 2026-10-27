"""Committed signed apt/dnf generations and the native tools that rebuild them.

The fixtures under `testdata/native-repositories/` are real `assemble()` output signed by
a non-expiring rehearsal key whose private half was discarded. Regenerate them on Linux
with the native tools: `python3 -m scripts.verification_tests.distribution.native_repository_fixtures`.
"""

import os
from pathlib import Path
import shutil
import tempfile

from scripts.linux_release.native_repositories import assemble
from scripts.linux_release.repository_objects import encoded
from scripts.linux_release.signing import digest
from scripts.verification_tests.distribution import repository_fixture_support as support
from scripts.verification_tests.distribution.repository_fixture_support import native_tools, signing_key

TESTDATA = Path(__file__).resolve().parent / "testdata/native-repositories"
PACKAGE = "paintedwolf-repository-fixture"
NATIVE = {"deb": f"{PACKAGE}_0.0.1-1_amd64.deb", "rpm": f"{PACKAGE}-0.0.1-1.x86_64.rpm"}


def generation(name: str, work: Path) -> tuple[dict, Path]:
    return support.generation(TESTDATA, name, work)


def trust(work: Path, name="keys.json", rehearsal=True) -> Path:
    return support.trust(TESTDATA, work, name, rehearsal)


def inputs(work: Path) -> Path:
    directory = work / "inputs"
    shutil.copytree(TESTDATA / "inputs", directory)
    return directory


def manifest(family: str, directory: Path, version="0.0.1", **changes) -> dict:
    path = directory / NATIVE[family]
    row = {"file": path.name, "sha256": digest(path), "architecture": "amd64", "native_version": "0.0.1-1"}
    return {"format_version": 1, "source_commit": "f" * 40, "version": version,
            "packages": [{**row, **changes}]}


def packages(work: Path, version: str) -> Path:
    from scripts.linux_release.repository_rehearsal import fixtures
    output = work / ("packages-" + version)
    output.mkdir()
    built = fixtures(output, version)
    selected = output / "selected"
    selected.mkdir()
    for family in ["deb", "rpm"]:
        shutil.copyfile(built[family], selected / built[family].name)
    return selected


def regenerate(destination=TESTDATA):
    native_tools("dpkg-deb", "apt-ftparchive", "rpmbuild", "rpmsign", "createrepo_c", "gpg")
    with tempfile.TemporaryDirectory(prefix="paintedwolf-repository-fixtures-") as temporary:
        work = Path(temporary)
        keys = work / "keys"
        catalog = signing_key(keys, "fixture", "keys.json")
        signing_key(keys, "other", "other-keys.json")
        output = work / "output"
        output.mkdir()
        for name in ["keys.json", "fixture-public.asc", "other-keys.json", "other-public.asc"]:
            shutil.copyfile(keys / name, output / name)
        for version in ["0.0.1", "0.0.2"]:
            selected = packages(work, version)
            if version == "0.0.1":
                shutil.copytree(selected, output / "inputs")
            for family in ["deb", "rpm"]:
                [source] = selected.glob("*." + family)
                row = {"file": source.name, "sha256": digest(source), "architecture": "amd64",
                       "native_version": version + "-1"}
                target = output / f"{family}-{version}"
                receipt = assemble(family, {"format_version": 1, "source_commit": "f" * 40, "version": version,
                                            "packages": [row]}, selected, target, "preview", catalog,
                                   keys / "fixture-secret.asc", True, PACKAGE)
                (target / "generation.json").write_bytes(encoded(receipt))
        if destination.exists():
            shutil.rmtree(destination)
        shutil.copytree(output, destination)
    for path in destination.rglob("*"):
        os.chmod(path, 0o755 if path.is_dir() else 0o644)


if __name__ == "__main__":
    regenerate()
