"""Exercise correctly signed but unauthorized setup package contents without installing them."""

from pathlib import Path
import shutil
import subprocess

from .repository_setup_admission import admit_setup
from .repository_setup_archives import command, cpio
from .signing import digest, sign


def refuse(package, catalog, private, authority, expected_error):
    checksums = package.parent / "SHA256SUMS"
    checksums.write_text(digest(package) + "  " + package.name + "\n")
    signature = package.parent / "SHA256SUMS.asc"
    sign("detached", checksums, signature, catalog, private, True)
    try:
        admit_setup(package, checksums, signature, catalog, **authority, rehearsal=True)
    except ValueError as error:
        if expected_error not in str(error):
            raise ValueError("hostile setup package failed before its intended semantic check") from error
    else:
        raise ValueError("setup admission accepted a correctly signed hostile package")


def rehearse(work, artifact, catalog, private, authority):
    if authority["family"] == "deb":
        for kind, error in [("script", "unsafe mode"), ("payload", "payload"), ("mode", "unsafe mode"),
                            ("conffile", "conffiles"), ("relationship", "relationships")]:
            stage = work / ("hostile-deb-" + kind)
            tree = stage / "tree"
            subprocess.run(["dpkg-deb", "--raw-extract", str(artifact), str(tree)], check=True)
            config = tree / "etc/apt/sources.list.d/painted-wolf-code.sources"
            if kind == "script":
                (tree / "DEBIAN/postinst").write_text("#!/bin/sh\nexit 0\n")
                (tree / "DEBIAN/postinst").chmod(0o755)
            elif kind == "payload":
                (tree / "etc/unowned").write_text("extra\n")
                (tree / "etc/unowned").chmod(0o644)
            elif kind == "mode":
                config.chmod(0o666)
            elif kind == "conffile":
                (tree / "DEBIAN/conffiles").write_text("/etc/apt/keyrings/painted-wolf-code.asc\n")
            else:
                control = tree / "DEBIAN/control"
                control.write_text(control.read_text() + "Conflicts: unrelated-application\n")
            package = stage / artifact.name
            subprocess.run(["dpkg-deb", "--build", "--root-owner-group", str(tree), str(package)], check=True)
            refuse(package, catalog, private, authority, error)
        return
    payload = cpio(command(["rpm2cpio", str(artifact)]))
    for kind, error in [("script", "scripts"), ("flags", "configuration flags"),
                        ("relationship", "scripts or triggers")]:
        top = work / ("hostile-rpm-" + kind)
        tree = top / "payload"
        for path, data in payload.items():
            target = tree / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
            target.chmod(0o644)
        for directory in [tree, *tree.rglob("*")]:
            if directory.is_dir():
                directory.chmod(0o755)
        for name in ["BUILD", "RPMS", "SOURCES", "SPECS", "SRPMS", "BUILDROOT"]:
            (top / name).mkdir(parents=True, exist_ok=True)
        version = artifact.name.removeprefix("painted-wolf-code-repo-").removesuffix("-1.noarch.rpm")
        relation = "Obsoletes: unrelated-application\n" if kind == "relationship" else ""
        script = "\n%post\nexit 0\n" if kind == "script" else ""
        config = "%config(noreplace) " if kind != "flags" else ""
        spec = top / "SPECS/setup.spec"
        spec.write_text(f"""Name: painted-wolf-code-repo
Version: {version}
Release: 1
Summary: Isolated hostile setup fixture
License: Apache-2.0
BuildArch: noarch
AutoReqProv: no
{relation}%description
Isolated hostile setup fixture, never installed.
%install
mkdir -p %{{buildroot}}
cp -a {tree}/. %{{buildroot}}/
%files
{config}/etc/yum.repos.d/painted-wolf-code.repo
/etc/pki/rpm-gpg/painted-wolf-code.asc
{script}""")
        subprocess.run(["rpmbuild", "--define", "_topdir " + str(top), "-bb", str(spec)], check=True)
        package = top / artifact.name
        shutil.copyfile(next((top / "RPMS").rglob("*.rpm")), package)
        sign("rpm", package, package, catalog, private, True)
        refuse(package, catalog, private, authority, error)
