"""Sign Linux artifacts with an isolated catalog-selected OpenPGP identity."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import sys
import tempfile

from .openpgp import ephemeral, keyring, run, selection, verify_rpm, verify_signature

ROOT = Path(__file__).resolve().parents[2]
CATALOG = ROOT / "packaging/linux/signing/keys.json"


def private_material(path: Path | None) -> bytes:
    value = os.environ.pop("LINUX_PACKAGE_SIGNING_KEY", None)
    if path is not None:
        if value is not None:
            raise ValueError("select either a private key file or the signing environment")
        with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), "rb") as source:
            info = os.fstat(source.fileno())
            if (not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077
                    or info.st_uid != os.geteuid() or info.st_size > 1 << 20):
                raise ValueError("private key file must be bounded, regular and private to its owner")
            return source.read()
    if not value:
        raise ValueError("LINUX_PACKAGE_SIGNING_KEY is required for signing")
    return value.encode()


def digest(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as source:
        while chunk := source.read(1 << 20):
            value.update(chunk)
    return value.hexdigest()


def sign(kind: str, subject: Path, destination: Path, catalog: Path,
         private: Path | None = None, rehearsal: bool = False) -> dict:
    selected, public = selection(catalog, rehearsal)
    fingerprint = selected["fingerprint"]
    if subject.is_symlink() or not subject.is_file() or destination.is_symlink():
        raise ValueError("signing requires regular local inputs and outputs")
    if kind != "rpm" and subject.resolve() == destination.resolve():
        raise ValueError("a metadata signature cannot replace its subject")
    original_digest = digest(subject)
    destination.parent.mkdir(parents=True, exist_ok=True)
    with keyring(public, fingerprint, private_material(private)) as home:
        with tempfile.TemporaryDirectory(prefix=".linux-sign-", dir=destination.parent) as directory:
            temporary = Path(directory) / destination.name
            if kind == "rpm":
                shutil.copyfile(subject, temporary)
                options = {"_gpg_name": fingerprint, "_gpg_path": str(home),
                           "_gpg_digest_algo": "sha512", "__gpg": shutil.which("gpg"),
                           "_openpgp_sign": "gpg", "_openpgp_sign_id": fingerprint}
                arguments = ["rpmsign"]
                for name, value in options.items():
                    arguments.extend(["--define", f"{name} {value}"])
                run([*arguments, "--resign", str(temporary.resolve())], home)
                verify_rpm(temporary, public, fingerprint)
            else:
                operation = "--clearsign" if kind == "release" else "--detach-sign"
                run(["gpg", "--batch", "--no-tty", "--yes", "--local-user", fingerprint,
                     "--digest-algo", "SHA512", "--armor", "--output", str(temporary.resolve()),
                     operation, str(subject.resolve())], home)
                verify_signature(home, fingerprint, temporary, None if kind == "release" else subject)
                if kind == "release" and run(["gpg", "--batch", "--decrypt", str(temporary)], home) != subject.read_bytes():
                    raise ValueError("clear signing changed the Release content")
            temporary.chmod(0o644)
            temporary.replace(destination)
    return {"format_version": 1, "rehearsal": rehearsal, "generation": selected["generation"],
            "fingerprint": fingerprint, "digest_algorithm": "sha512", "kind": kind,
            "subject_sha256": original_digest, "output_sha256": digest(destination)}


def verify(kind: str, subject: Path, signature: Path | None, catalog: Path, rehearsal=False):
    selected, public = selection(catalog, rehearsal)
    if kind == "rpm":
        verify_rpm(subject, public, selected["fingerprint"])
    else:
        if signature is None:
            raise ValueError("a signature path is required")
        with keyring(public, selected["fingerprint"]) as home:
            verify_signature(home, selected["fingerprint"], signature, None if kind == "release" else subject)
            if kind == "release" and run(["gpg", "--batch", "--decrypt", str(signature)], home) != subject.read_bytes():
                raise ValueError("InRelease differs from the selected Release")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=["sign", "verify", "ephemeral-key"])
    parser.add_argument("--kind", choices=["rpm", "release", "detached"], default="detached")
    parser.add_argument("--catalog", type=Path, default=CATALOG)
    parser.add_argument("--rehearsal", action="store_true")
    parser.add_argument("--private-key", type=Path)
    parser.add_argument("--subject", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.operation == "ephemeral-key":
            if not args.rehearsal:
                raise ValueError("ephemeral generation is confined to explicit rehearsals")
            print(ephemeral(args.output))
        elif args.subject is None:
            raise ValueError("subject is required")
        elif args.operation == "sign":
            print(json.dumps(sign(args.kind, args.subject, args.output, args.catalog,
                                  args.private_key, args.rehearsal), sort_keys=True))
        else:
            verify(args.kind, args.subject, args.output, args.catalog, args.rehearsal)
    except (OSError, ValueError) as exc:
        print(f"Linux signing: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
