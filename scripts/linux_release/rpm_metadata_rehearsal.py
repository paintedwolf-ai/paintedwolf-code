"""Build and lint production RPM metadata with a tiny nonapplication payload."""

import hashlib
import subprocess

from release_semver import linux_package_version, parse

from . import metadata_fixture
from .metadata_fixture import ROOT, run


def rehearse(work):
    architecture = metadata_fixture.native_architecture()
    lint_version = subprocess.check_output(["rpmlint", "--version"], text=True).strip()
    if lint_version != "2.5.0":
        raise ValueError("this regression requires actual rpmlint 2.5.0")
    assembler = metadata_fixture.assembler()
    tree = metadata_fixture.payload(work / "payload", assembler.BOOTSTRAP["path"])
    version = linux_package_version(parse((ROOT / "VERSION").read_text().strip()))
    outcomes = {}
    for name in ("current", "spelling-error"):
        directory = work / name
        directory.mkdir()
        top = directory / "rpmbuild"
        top.mkdir()
        content = assembler.rpm_spec(ROOT, tree, version, 3, architecture)
        if name == "spelling-error":
            # A misspelling outside the accepted domain vocabulary must still fail the summary.
            summary = assembler.tauri_metadata(ROOT)["shortDescription"]
            content = content.replace(f"Summary: {summary}", f"Summary: {summary} with a securty engine")
        specification = directory / "painted-wolf-code.spec"
        specification.write_text(content)
        built = run(["rpmbuild", "-bb", "--quiet", "--target", architecture,
                     "--define", f"_topdir {top}", "--define", f"_rpmdir {directory}",
                     "--define", "_build_name_fmt %%{NAME}-%%{VERSION}-%%{RELEASE}.%%{ARCH}.rpm",
                     str(specification)])
        if built.returncode:
            raise ValueError("actual tiny RPM construction failed")
        packages = list(directory.glob("*.rpm"))
        if len(packages) != 1:
            raise ValueError("construction did not produce exactly one RPM")
        lint = run(["rpmlint", "--config", str(ROOT / "packaging/linux/rpm/rpmlint.toml"), str(packages[0])])
        if (lint.returncode == 0) != (name == "current"):
            raise ValueError("production metadata must pass and a misspelled summary must fail")
        outcomes[name] = {"rpmlint_exit_code": lint.returncode,
                          "rpm_sha256": hashlib.sha256(packages[0].read_bytes()).hexdigest(),
                          "specification_sha256": hashlib.sha256(specification.read_bytes()).hexdigest()}
    return metadata_fixture.report(architecture, rpmlint_version=lint_version, cases=outcomes)


if __name__ == "__main__":
    metadata_fixture.main("scripts.linux_release.rpm_metadata_rehearsal", __doc__, rehearse)
