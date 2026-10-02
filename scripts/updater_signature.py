"""Version binding carried in updater signature documents.

An updater signature is a base64 minisign document whose trusted comment holds
tab-separated ``key:value`` fields. The ``version`` field is covered by the
signature, and the updater refuses a feed entry whose announced version differs.
Cryptographic verification happens where the artifact bytes are present
(``verify-updater-signature.sh``); this module checks the binding wherever only
the feed is available.
"""
from __future__ import annotations

import base64
import binascii

TRUSTED_COMMENT = "trusted comment: "


def signed_version(document: str) -> str:
    try:
        text = base64.b64decode(document.strip(), validate=True).decode("utf-8")
    except (binascii.Error, UnicodeDecodeError) as exc:
        raise ValueError(f"updater signature is not a base64 UTF-8 document: {exc}") from exc
    comments = [line[len(TRUSTED_COMMENT):] for line in text.splitlines() if line.startswith(TRUSTED_COMMENT)]
    if len(comments) != 1:
        raise ValueError("updater signature has no single trusted comment")
    versions = [field[len("version:"):] for field in comments[0].split("\t") if field.startswith("version:")]
    if len(versions) != 1 or not versions[0]:
        raise ValueError("updater signature is not bound to a version")
    return versions[0]


def require_version(document: str, version: str, label: str) -> None:
    signed = signed_version(document)
    if signed != version:
        raise ValueError(f"{label} is signed for version {signed}, not {version}")


def rehearsal_signature(version: str, file: str = "rehearsal") -> str:
    """An unverifiable signature document for feed rehearsals that never install."""
    payload = base64.b64encode(b"Ed" + bytes(72)).decode()
    text = (
        "untrusted comment: release rehearsal\n"
        f"{payload}\n"
        f"{TRUSTED_COMMENT}timestamp:0\tfile:{file}\tversion:{version}\n"
        f"{base64.b64encode(bytes(64)).decode()}\n"
    )
    return base64.b64encode(text.encode()).decode()


if __name__ == "__main__":
    import sys

    if len(sys.argv) != 3 or sys.argv[1] != "rehearsal":
        print("usage: updater_signature.py rehearsal VERSION", file=sys.stderr)
        raise SystemExit(2)
    print(rehearsal_signature(sys.argv[2]))
