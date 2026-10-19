"""Construct explicit opt-in repository setup packages from admitted native generations."""

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
from urllib.parse import urlsplit

from scripts.release_semver import linux_package_version, parse
from .openpgp import selection
from .repository_objects import encoded, immutable
from .repository_operations import identity
from .repository_publication import configuration
from .repository_validation import admit
from .signing import digest, sign, verify

PACKAGE = "painted-wolf-code-repo"


def build(manifest, directory, catalog, destination, base_url, private=None, rehearsal=False):
    if private is None:
        raise ValueError("setup signing requires an explicit private key file")
    url = urlsplit(base_url)
    if (url.scheme != "https" or not url.hostname or url.username or url.password or url.query or url.fragment
            or any(character.isspace() for character in base_url)):
        raise ValueError("repository setup requires an explicit HTTPS origin")
    admit(manifest, directory, catalog, rehearsal)
    selected, public = selection(catalog, rehearsal)
    version = linux_package_version(parse(manifest["version"]))
    if destination.exists() or destination.is_symlink():
        raise ValueError("repository setup requires a new output directory")
    destination.mkdir(parents=True)
    family, channel = manifest["family"], manifest["channel"]
    config = configuration(manifest, base_url.rstrip("/") + f"/channels/{family}/{channel}", selected["fingerprint"])
    config_path = ("etc/apt/sources.list.d/painted-wolf-code.sources" if family == "deb"
                   else "etc/yum.repos.d/painted-wolf-code.repo")
    key_path = ("etc/apt/keyrings/painted-wolf-code.asc" if family == "deb"
                else "etc/pki/rpm-gpg/painted-wolf-code.asc")
    paths = {config_path: config.encode(), key_path: public.read_bytes()}
    with tempfile.TemporaryDirectory(prefix="paintedwolf-repository-setup-") as temporary:
        work = Path(temporary)
        tree = work / "payload"
        for name, data in paths.items():
            target = tree / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
            target.chmod(0o644)
        if family == "deb":
            control = tree / "DEBIAN"
            control.mkdir()
            (control / "control").write_text(
                f"Package: {PACKAGE}\nVersion: {version}\nArchitecture: all\nSection: admin\nPriority: optional\n"
                "Maintainer: Painted Wolf Code\nDescription: Explicit Painted Wolf Code repository setup\n"
                f" Installs the reviewed {channel} repository and public signing key.\n")
            (control / "conffiles").write_text("/" + config_path + "\n")
            artifact = destination / f"{PACKAGE}_{version}_all.deb"
            subprocess.run(["dpkg-deb", "--build", "--root-owner-group", str(tree), str(artifact)], check=True)
        else:
            top = work / "rpmbuild"
            for name in ["BUILD", "RPMS", "SOURCES", "SPECS", "SRPMS", "BUILDROOT"]:
                (top / name).mkdir(parents=True)
            spec = top / "SPECS/setup.spec"
            # A validated semantic version and private temporary paths are the only substitutions.
            spec.write_text(f"""Name: {PACKAGE}
Version: {version}
Release: 1
Summary: Explicit Painted Wolf Code repository setup
License: Apache-2.0
BuildArch: noarch
AutoReqProv: no
%description
Installs the reviewed {channel} repository and public signing key.
%install
mkdir -p %{{buildroot}}
cp -a {tree}/. %{{buildroot}}/
%files
%config(noreplace) /{config_path}
/{key_path}
""")
            subprocess.run(["rpmbuild", "--define", "_topdir " + str(top), "-bb", str(spec)], check=True)
            artifact = destination / f"{PACKAGE}-{version}-1.noarch.rpm"
            shutil.copyfile(next((top / "RPMS").rglob("*.rpm")), artifact)
            sign("rpm", artifact, artifact, catalog, private, rehearsal)
            verify("rpm", artifact, None, catalog, rehearsal)
    checksums = destination / "SHA256SUMS"
    immutable(checksums, (digest(artifact) + "  " + artifact.name + "\n").encode())
    sign("detached", checksums, destination / "SHA256SUMS.asc", catalog, private, rehearsal)
    verify("detached", checksums, destination / "SHA256SUMS.asc", catalog, rehearsal)
    immutable(destination / "package-key.asc", public.read_bytes())
    receipt = {"format_version": 1, "rehearsal": rehearsal, "published": False, "family": family,
               "channel": channel, "version": manifest["version"], "source_commit": manifest["source_commit"],
               "repository_generation": identity(manifest), "inventory_sha256": manifest["inventory_sha256"],
               "signing_generation": selected["generation"], "key_fingerprint": selected["fingerprint"],
               "package": {"file": artifact.name, "sha256": digest(artifact), "bytes": artifact.stat().st_size},
               "installed_files": [{"path": name, "sha256": hashlib.sha256(data).hexdigest()}
                                   for name, data in sorted(paths.items())]}
    immutable(destination / "setup.json", encoded(receipt))
    return receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ["directory", "catalog", "destination"]:
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--private", type=Path)
    parser.add_argument("--rehearsal", action="store_true")
    args = parser.parse_args()
    manifest = json.loads((args.directory / "generation.json").read_text())
    print(json.dumps(build(manifest, args.directory, args.catalog, args.destination,
                           args.base_url, args.private, args.rehearsal), indent=2))


if __name__ == "__main__":
    main()
