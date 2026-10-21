"""Isolated OpenPGP signing and verification for Linux release artifacts."""

from __future__ import annotations

import base64
from contextlib import contextmanager
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

FINGERPRINT = re.compile(r"[0-9A-F]{40}\Z")


def run(arguments, home: Path, data=None) -> bytes:
    environment = {"PATH": os.environ["PATH"], "HOME": str(home), "GNUPGHOME": str(home), "LC_ALL": "C"}
    result = subprocess.run(arguments, input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            env=environment, timeout=120)
    if result.returncode:
        raise ValueError(f"{Path(arguments[0]).name} failed with exit status {result.returncode}")
    return result.stdout


def primary_fingerprints(records: bytes) -> list[str]:
    fingerprints = []
    primary = False
    for row in records.decode("utf-8").splitlines():
        columns = row.split(":")
        if columns[0] in {"pub", "sec", "sub", "ssb"}:
            primary = columns[0] in {"pub", "sec"}
        elif columns[0] == "fpr" and primary:
            if len(columns) < 10 or not FINGERPRINT.fullmatch(columns[9]):
                raise ValueError("invalid primary key fingerprint record")
            fingerprints.append(columns[9])
            primary = False
    return fingerprints


def selection(catalog_path: Path, rehearsal: bool = False) -> tuple[dict, Path]:
    catalog = json.loads(catalog_path.read_text())
    if catalog.get("format_version") != 1 or catalog.get("rehearsal", False) != rehearsal:
        raise ValueError("key catalog trust mode does not match the operation")
    generation = catalog["signing_generation"]
    rows = [row for row in catalog["generations"] if row["generation"] == generation]
    if len(rows) != 1 or type(generation) is not int or generation < 1:
        raise ValueError("key catalog must select one positive generation")
    row = rows[0]
    if not FINGERPRINT.fullmatch(row["fingerprint"]):
        raise ValueError("key catalog requires a full uppercase fingerprint")
    name = row["public_key"]
    if Path(name).name != name or name in {"", ".", ".."}:
        raise ValueError("public key must be a sibling catalog file")
    return row, catalog_path.parent / name


@contextmanager
def isolated_home():
    with tempfile.TemporaryDirectory(prefix="paintedwolf-linux-key-") as directory:
        home = Path(directory)
        home.chmod(0o700)
        try:
            yield home
        finally:
            run(["gpgconf", "--homedir", str(home), "--kill", "gpg-agent"], home)


@contextmanager
def keyring(public: Path, fingerprint: str, private: bytes | None = None):
    with isolated_home() as home:
        run(["gpg", "--batch", "--no-tty", "--import", str(public.resolve())], home)
        actual = primary_fingerprints(run(["gpg", "--batch", "--with-colons", "--fingerprint", "--list-keys"], home))
        if actual != [fingerprint]:
            raise ValueError("public key differs from the catalog or contains extra primary keys")
        if private is not None:
            run(["gpg", "--batch", "--no-tty", "--import"], home, private)
            actual = primary_fingerprints(run(["gpg", "--batch", "--with-colons", "--fingerprint", "--list-secret-keys"], home))
            if actual != [fingerprint]:
                raise ValueError("private key differs from the selected public key")
        run(["gpg", "--batch", "--yes", "--output", str(home / "trusted.gpg"), "--export", fingerprint], home)
        yield home


def verify_signature(home: Path, fingerprint: str, signature: Path, subject: Path | None = None) -> None:
    arguments = ["gpgv", "--homedir", str(home), "--status-fd", "1", "--keyring", str(home / "trusted.gpg"), str(signature)]
    if subject is not None:
        arguments.append(str(subject))
    status = run(arguments, home)
    valid = [line.removeprefix("[GNUPG:] VALIDSIG ").split()
             for line in status.decode().splitlines() if line.startswith("[GNUPG:] VALIDSIG ")]
    if len(valid) != 1:
        raise ValueError("signature must contain one verified signer")
    record = valid[0]
    if (len(record) not in {9, 10} or record[-1 if len(record) == 10 else 0] != fingerprint
            or record[4] != "4" or record[6] not in {"1", "3"} or record[7] != "10"):
        raise ValueError("signature does not use the selected v4 RSA key and SHA-512")


def require_sha512_packet(raw: str) -> None:
    packet = base64.b64decode("".join(raw.split()), validate=True)
    if len(packet) < 6 or packet[0] & 0x80 == 0:
        raise ValueError("invalid RPM signature packet")
    if packet[0] & 0x40:
        tag = packet[0] & 0x3F
        first = packet[1]
        if first < 192:
            offset, length = 2, first
        elif first < 224:
            offset, length = 3, ((first - 192) << 8) + packet[2] + 192
        elif first == 255:
            offset, length = 6, int.from_bytes(packet[2:6], "big")
        else:
            raise ValueError("partial RPM signature packet is unsupported")
    else:
        tag = (packet[0] >> 2) & 15
        kind = packet[0] & 3
        if kind == 3:
            raise ValueError("unbounded RPM signature packet is unsupported")
        width = 1 << kind
        offset, length = 1 + width, int.from_bytes(packet[1:1 + width], "big")
    body = packet[offset:]
    if tag != 2 or length != len(body) or len(body) < 4 or body[0] != 4 or body[2] not in {1, 3} or body[3] != 10:
        raise ValueError("RPM signature must use v4 RSA and SHA-512")


def verify_rpm(package: Path, public: Path, fingerprint: str) -> None:
    if package.is_symlink() or not package.is_file():
        raise ValueError("RPM verification requires a regular local package")
    with keyring(public, fingerprint) as home:
        database = home / "rpmdb"
        database.mkdir()
        options = ["--dbpath", str(database), "--define", "_keyring rpmdb"]
        run(["rpm", *options, "--initdb"], home)
        run(["rpmkeys", *options, "--import", str(public.resolve())], home)
        tags = ["RSAHEADER", "DSAHEADER", "SIGGPG", "SIGPGP"]
        query = "|".join("%{" + tag + ":base64}" for tag in tags)
        output = run(["rpm", *options, "-qp", "--nomanifest", "--qf", query, str(package.resolve())], home)
        fields = output.decode().split("|")
        if len(fields) != len(tags):
            raise ValueError("RPM signature query returned an unexpected shape")
        signatures = [field for field in fields if field.strip() != "(none)"]
        if not signatures:
            raise ValueError("RPM has no OpenPGP signature")
        for signature in signatures:
            require_sha512_packet(signature)
        run(["rpmkeys", *options, "--checksig", "--verbose", str(package.resolve())], home)


def ephemeral(destination: Path) -> Path:
    destination.mkdir(mode=0o700, parents=True, exist_ok=False)
    with isolated_home() as home:
        run(["gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "", "--quick-generate-key",
             "Painted Wolf Linux rehearsal <rehearsal@example.invalid>", "rsa2048", "sign", "1d"], home)
        fingerprints = primary_fingerprints(run(["gpg", "--batch", "--with-colons", "--fingerprint", "--list-secret-keys"], home))
        if len(fingerprints) != 1:
            raise ValueError("rehearsal generation did not produce exactly one key")
        fingerprint = fingerprints[0]
        for secret, name in [(False, "rehearsal-public.asc"), (True, "rehearsal-secret.asc")]:
            data = run(["gpg", "--batch", "--armor", "--export-secret-keys" if secret else "--export", fingerprint], home)
            path = destination / name
            path.write_bytes(data)
            path.chmod(0o600 if secret else 0o644)
    catalog = destination / "keys.json"
    catalog.write_text(json.dumps({"format_version": 1, "rehearsal": True, "signing_generation": 1,
                                  "generations": [{"generation": 1, "fingerprint": fingerprint,
                                                   "public_key": "rehearsal-public.asc"}]}, indent=2) + "\n")
    return catalog
