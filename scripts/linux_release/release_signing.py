"""Sign one staged Linux release set, rebind its digests, and check exact-source qualification."""

from __future__ import annotations

import argparse
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

from . import candidate_inventory
from .pacman_repository import binary_signature
from .signing import CATALOG, digest, sign

# RPM carries its signature in the package header; the other forms use detached signatures.
DETACHED = ("deb", "pkg.tar.zst")


def require(source_commit: str):
    from ci_policy.verification_verdict import require_qualification
    return require_qualification(os.environ["GITHUB_REPOSITORY"], source_commit)


@contextmanager
def private_key():
    """Materialize the protected key once, owner-only, for every signature of this set."""
    value = os.environ.pop("LINUX_PACKAGE_SIGNING_KEY", None)
    if not value:
        raise ValueError("LINUX_PACKAGE_SIGNING_KEY is required for Linux release signing")
    with tempfile.TemporaryDirectory(prefix="paintedwolf-linux-release-key-") as directory:
        path = Path(directory) / "private.asc"
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w") as output:
            output.write(value)
        yield path


def rpm_payload(path: Path) -> tuple[str, str]:
    """The identity and payload the staging audit admitted; signing may change only the header signature."""
    identity = subprocess.check_output(["rpm", "-qp", "--nosignature", "--qf", "%{NAME} %{VERSION}-%{RELEASE} %{ARCH}",
                                        str(path)], text=True, timeout=60).strip()
    payload = subprocess.run(["rpm2cpio", str(path)], check=True, capture_output=True, timeout=300).stdout
    return identity, hashlib.sha256(payload).hexdigest()


def sign_staged(directory: Path, platform: str, architecture: str, version: str, source_commit: str,
                catalog: Path = CATALOG, private: Path | None = None, *, payload=rpm_payload, rehearsal=False) -> dict:
    if type(rehearsal) is not bool:
        raise ValueError("Linux release signing requires an explicit trust mode")
    stem = f"painted-wolf-code_v{version}_{platform}"
    packages = {extension: directory / f"{stem}.{extension}" for extension in ("deb", "rpm", "pkg.tar.zst")}
    for path in packages.values():
        if path.is_symlink() or not path.is_file() or not (path.parent / (path.name + ".sha256")).is_file():
            raise ValueError(f"staged Linux release set is incomplete: {path.name}")
        if (path.parent / (path.name + ".sig")).exists():
            raise ValueError(f"staged Linux package is already signed: {path.name}")
    rpm = packages["rpm"]
    audited = payload(rpm)
    receipts = [sign("rpm", rpm, rpm, catalog, private, rehearsal)]
    if payload(rpm) != audited:
        raise ValueError("RPM signing changed the audited package identity or payload")
    for extension in DETACHED:
        subject = packages[extension]
        binary_signature(subject, subject.with_name(subject.name + ".sig"), catalog, private, rehearsal)
        receipts.append({"kind": "detached", "subject_sha256": digest(subject),
                         "output_sha256": digest(subject.with_name(subject.name + ".sig"))})
    for path in packages.values():
        (path.parent / (path.name + ".sha256")).write_text(digest(path) + "\n")
    inventory = directory / "candidate-packages.json"
    inventory.unlink(missing_ok=True)
    inventory.write_text(json.dumps(candidate_inventory.inventory(directory, platform, architecture, version,
                                                                  source_commit), sort_keys=True, indent=2) + "\n")
    return {"format_version": 1, "platform": platform, "version": version, "source_commit": source_commit,
            "packages": {path.name: digest(path) for path in packages.values()}, "signatures": receipts}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, required=True)
    for name in ["platform", "architecture", "version", "source-commit"]:
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    try:
        with private_key() as private:
            result = sign_staged(args.directory, args.platform, args.architecture, args.version,
                                 args.source_commit, private=private)
        print(json.dumps(result, sort_keys=True))
    except (OSError, ValueError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Linux release signing failed: {exc}\n")


if __name__ == "__main__":
    main()
