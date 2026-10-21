"""Admit opt-in repository setup separately from application generations."""

import hashlib
import os
from pathlib import Path
import stat

from .generations import CATALOG
from .repositories import digest
from ...release_admission import document


def public_key(path: Path) -> bytes:
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, "rb") as stream:
        before = os.fstat(stream.fileno())
        if not stat.S_ISREG(before.st_mode) or before.st_size > 65536:
            raise ValueError("repository setup key must be a bounded regular file")
        data = stream.read(65537)
        after = os.fstat(stream.fileno())
        current = path.stat(follow_symlinks=False)
        identity = lambda row: (row.st_dev, row.st_ino, row.st_size, row.st_mtime_ns, row.st_ctime_ns)
        if len(data) != before.st_size or identity(before) != identity(after) or identity(after) != identity(current):
            raise ValueError("repository setup key changed during admission")
        return data


def verify(root: Path, selection: dict, *, base_url: str | None) -> list[dict]:
    generations = [entry["manifest"] for entry in selection["repositories"]["generations"]
                   if entry["manifest"]["family"] in {"deb", "rpm"}]
    if not generations:
        return []
    if not base_url:
        raise ValueError("repository setup requires the independently configured production origin")
    from ...repository_setup_admission import admit_setup
    from ...repository_setup_identity import package_identity
    from ...openpgp import selection as select_key
    _, key = select_key(CATALOG, False)
    trusted_key = public_key(key)
    expected = {digest(manifest) for manifest in generations}
    parent = root / "repository-setup"
    if parent.is_symlink() or {path.name for path in parent.iterdir()} != expected:
        raise ValueError("setup bundles must cover exactly the selected native generations")
    result = []
    for manifest in generations:
        identity = digest(manifest)
        directory = parent / identity
        filename = package_identity(selection["version"], manifest["family"], selection["channel"],
                                    selection["signing_generation"])["file"]
        names = {filename, "setup.json", "SHA256SUMS", "SHA256SUMS.asc", "package-key.asc"}
        if (directory.is_symlink() or {path.name for path in directory.iterdir()} != names
                or public_key(directory / "package-key.asc") != trusted_key):
            raise ValueError("setup bundle differs from the selected control package or protected key")
        report = admit_setup(directory / filename, directory / "SHA256SUMS", directory / "SHA256SUMS.asc", CATALOG,
            family=manifest["family"], source_commit=selection["source_commit"], version=selection["version"],
            channel=selection["channel"], repository_generation=identity,
            inventory_sha256=manifest["inventory_sha256"], base_url=base_url,
            signing_generation=selection["signing_generation"], key_fingerprint=selection["key_fingerprint"],
            rehearsal=False)
        retained, _ = document(directory / "setup.json")
        # The builder record is compared only after independent payload and signature admission.
        if digest(retained) != digest(report):
            raise ValueError("retained setup record differs from its freshly admitted control package")
        result.append({**report, "public_key_sha256": hashlib.sha256(trusted_key).hexdigest()})
    return result
