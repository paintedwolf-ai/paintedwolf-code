"""Rehearse Linux cryptographic admission locally with a disposable signing key."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

from .openpgp import ephemeral, selection
from .signing import CATALOG, digest, sign, verify


def rejected(action):
    try:
        action()
    except ValueError:
        return
    raise ValueError("an invalid signing case was admitted")


def fixture_rpm(work):
    top = work / "rpmbuild"
    for name in ["BUILD", "RPMS", "SOURCES", "SPECS", "SRPMS", "BUILDROOT"]:
        (top / name).mkdir(parents=True, exist_ok=True)
    spec = top / "SPECS/fixture.spec"
    spec.write_text("""Name: paintedwolf-signing-fixture
Version: 0.0.1
Release: 1
Summary: Disposable Linux signing fixture
License: MIT
BuildArch: noarch
%description
Disposable signing fixture; not a Painted Wolf Code application package.
%install
mkdir -p %{buildroot}/usr/share/paintedwolf-signing-fixture
printf 'fixture\\n' > %{buildroot}/usr/share/paintedwolf-signing-fixture/value
%files
/usr/share/paintedwolf-signing-fixture/value
""")
    subprocess.run(["rpmbuild", "--define", f"_topdir {top}", "-bb", str(spec)], check=True)
    files = list((top / "RPMS").rglob("*.rpm"))
    if len(files) != 1:
        raise ValueError("fixture construction did not produce one RPM")
    return files


def rehearse(work: Path, candidates: Path | None = None, architectures=()):
    # Production credentials are never passed to fixture tools or imported.
    os.environ.pop("LINUX_PACKAGE_SIGNING_KEY", None)
    catalog = ephemeral(work / "key")
    wrong = ephemeral(work / "wrong-key")
    selected, _ = selection(catalog, True)
    production, _ = selection(CATALOG)
    if selected["fingerprint"] == production["fingerprint"]:
        raise ValueError("rehearsal must not use a production signing identity")
    rejected(lambda: selection(catalog))
    rejected(lambda: selection(CATALOG, True))
    private = catalog.parent / "rehearsal-secret.asc"
    release = work / "Release"
    release.write_text("Origin: Painted Wolf Code signing rehearsal\nSuite: fixture\n")
    receipts = []
    for kind, filename in [("release", "InRelease"), ("detached", "Release.gpg")]:
        signature = work / filename
        receipts.append(sign(kind, release, signature, catalog, private, True))
        verify(kind, release, signature, catalog, True)
        rejected(lambda: verify(kind, release, signature, wrong, True))
        rejected(lambda: verify(kind, release, signature, CATALOG))
        changed = work / "changed-Release"
        changed.write_bytes(release.read_bytes() + b"tampered\n")
        rejected(lambda: verify(kind, changed, signature, catalog, True))
    checksums = work / "SHA256SUMS"
    packages = sorted(candidates.rglob("*.rpm")) if candidates else fixture_rpm(work)
    if not packages:
        raise ValueError("rehearsal requires candidate RPMs")
    observed = set()
    lines = []
    for index, package in enumerate(packages):
        architecture = subprocess.check_output(["rpm", "-qp", "--nomanifest", "--qf", "%{ARCH}", str(package)], text=True).strip()
        observed.add(architecture)
        destination = work / f"signed-{index}.rpm"
        receipts.append(sign("rpm", package, destination, catalog, private, True))
        verify("rpm", destination, None, catalog, True)
        rejected(lambda: verify("rpm", destination, None, wrong, True))
        rejected(lambda: verify("rpm", destination, None, CATALOG))
        unsigned = work / f"unsigned-{index}.rpm"
        shutil.copyfile(destination, unsigned)
        subprocess.run(["rpmsign", "--delsign", str(unsigned)], check=True)
        rejected(lambda: verify("rpm", unsigned, None, catalog, True))
        damaged = work / f"damaged-{index}.rpm"
        data = bytearray(destination.read_bytes())
        data[-1] ^= 1
        damaged.write_bytes(data)
        rejected(lambda: verify("rpm", damaged, None, catalog, True))
        lines.append(f"{digest(destination)}  {destination.name}\n")
    if not set(architectures).issubset(observed):
        raise ValueError("candidate RPM architecture coverage is incomplete")
    checksums.write_text("".join(lines))
    signature = work / "SHA256SUMS.asc"
    receipts.append(sign("detached", checksums, signature, catalog, private, True))
    verify("detached", checksums, signature, catalog, True)
    return {"format_version": 1, "rehearsal": True, "production_admitted": False,
            "architectures": sorted(observed), "signatures": receipts,
            "cases": ["correct-key", "wrong-key", "production-rejects-fixture", "changed-content",
                      "unsigned-rpm", "changed-rpm-payload", "isolated-keyring", "sha512-signatures"]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate-dir", type=Path)
    parser.add_argument("--require-architectures", default="")
    parser.add_argument("--receipt", type=Path, required=True)
    args = parser.parse_args()
    for executable in ["gpg", "gpgv", "gpgconf", "rpm", "rpmkeys", "rpmsign", "rpmbuild"]:
        if shutil.which(executable) is None:
            parser.error(f"{executable} is required")
    try:
        with tempfile.TemporaryDirectory(prefix="paintedwolf-linux-signing-rehearsal-") as directory:
            result = rehearse(Path(directory), args.candidate_dir,
                              tuple(filter(None, args.require_architectures.split(","))))
        args.receipt.parent.mkdir(parents=True, exist_ok=True)
        args.receipt.write_text(json.dumps(result, indent=2) + "\n")
        print(f"Linux signing rehearsal passed: {args.receipt}")
    except (OSError, ValueError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Linux signing rehearsal failed: {exc}\n")


if __name__ == "__main__":
    main()
