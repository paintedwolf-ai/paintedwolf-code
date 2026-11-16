"""Build and lint the production Debian control data with a tiny nonapplication payload."""

import hashlib
import os
import shutil
import subprocess

from release_semver import linux_package_version, parse

from . import metadata_fixture
from .metadata_fixture import ROOT, run

CONFFILE_TAG = "file-in-etc-not-marked-as-conffile"


def rehearse(work):
    architecture = metadata_fixture.native_architecture()
    lint_version = subprocess.check_output(["lintian", "--version"], text=True).strip()
    assembler = metadata_fixture.assembler()
    staged = work / "staged"
    data = metadata_fixture.payload(staged / "data")
    # The fragments only this family ships include its /etc files and its lintian overrides.
    for installed, source in assembler.load_package_config(ROOT).optional_files(ROOT, "deb").items():
        destination = data / installed.lstrip("/")
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, destination)
    assembler.normalise_modes(data)
    if not any((data / "etc").rglob("*")):
        raise ValueError("the fixture needs a packaged /etc file to qualify conffile marking")
    version = linux_package_version(parse((ROOT / "VERSION").read_text().strip()))
    lintian = ["lintian", "--fail-on", "error", "--no-tag-display-limit",
               "--suppress-tags-from-file", str(ROOT / "packaging/linux/deb/lintian-suppress")]
    if os.geteuid() == 0:
        lintian.append("--allow-root")
    outcomes = {}
    for name in ("current", "unmarked-conffile"):
        directory = work / name
        directory.mkdir()
        package = assembler.write_deb(ROOT, staged, directory, version, architecture, directory)
        control = directory / "deb-root/DEBIAN"
        if name == "unmarked-conffile":
            # The historical failure shipped the same tree without declaring its /etc files.
            (control / "conffiles").unlink()
            package.unlink()
            rebuilt = run(["dpkg-deb", "--root-owner-group", "--build", str(directory / "deb-root"), str(package)])
            if rebuilt.returncode:
                raise ValueError("actual tiny Debian package construction failed")
        lint = run([*lintian, str(package)])
        if (lint.returncode == 0) != (name == "current") or (CONFFILE_TAG in lint.stdout) != (name != "current"):
            raise ValueError("production control data must pass and an unmarked /etc file must fail")
        outcomes[name] = {"lintian_exit_code": lint.returncode,
                          "deb_sha256": hashlib.sha256(package.read_bytes()).hexdigest(),
                          "control_sha256": hashlib.sha256((control / "control").read_bytes()).hexdigest()}
    return metadata_fixture.report(architecture, lintian_version=lint_version, cases=outcomes)


if __name__ == "__main__":
    metadata_fixture.main("scripts.linux_release.deb_metadata_rehearsal", __doc__, rehearse)
