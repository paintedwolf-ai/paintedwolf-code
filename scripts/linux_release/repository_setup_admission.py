"""Re-evaluate repository control packages against independently selected release authority."""

import hashlib
import os
from pathlib import Path
import re
import stat
import tempfile
from urllib.parse import urlsplit

from scripts.release_semver import linux_package_version, parse
from .openpgp import selection
from .repository_publication import configuration
from .repository_setup import PACKAGE
from .repository_setup_archives import deb, rpm
from .signing import digest, verify


def snapshot(source, destination, maximum):
    descriptor = os.open(source, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, "rb") as stream:
        before = os.fstat(stream.fileno())
        if not stat.S_ISREG(before.st_mode) or before.st_size > maximum:
            raise ValueError("setup admission requires a bounded regular file")
        data = stream.read(maximum + 1)
        after = os.fstat(stream.fileno())
        if (len(data) != before.st_size or (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns,
                                          before.st_ctime_ns) !=
                (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns)):
            raise ValueError("setup input changed during admission")
    destination.write_bytes(data)


def admit_setup(package: Path, checksums: Path, signature: Path, catalog: Path, *, family,
                source_commit, version, channel, repository_generation, inventory_sha256,
                base_url, signing_generation, key_fingerprint, rehearsal=False):
    """The controller supplies authority facts after verifying retained server artifact provenance."""
    if (family not in {"deb", "rpm"} or channel not in {"stable", "preview"}
            or not isinstance(source_commit, str) or not re.fullmatch("[0-9a-f]{40}", source_commit)
            or any(not isinstance(value, str) or not re.fullmatch("[0-9a-f]{64}", value)
                   for value in [repository_generation, inventory_sha256])):
        raise ValueError("setup admission requires explicit source and repository identities")
    url = urlsplit(base_url)
    if (url.scheme != "https" or not url.hostname or url.username or url.password or url.query or url.fragment
            or any(character.isspace() for character in base_url)):
        raise ValueError("setup admission requires an explicit HTTPS origin")
    selected, public = selection(catalog, rehearsal)
    if (type(signing_generation) is not int or selected["generation"] != signing_generation
            or selected["fingerprint"] != key_fingerprint):
        raise ValueError("setup signer differs from independently selected trust")
    native_version = linux_package_version(parse(version))
    filename = (f"{PACKAGE}_{native_version}_all.deb" if family == "deb"
                else f"{PACKAGE}-{native_version}-1.noarch.rpm")
    if package.name != filename:
        raise ValueError("setup filename differs from its selected native identity")
    config = ("etc/apt/sources.list.d/painted-wolf-code.sources" if family == "deb"
              else "etc/yum.repos.d/painted-wolf-code.repo")
    key = ("etc/apt/keyrings/painted-wolf-code.asc" if family == "deb"
           else "etc/pki/rpm-gpg/painted-wolf-code.asc")
    expected = {config: configuration({"family": family, "channel": channel},
                base_url.rstrip("/") + f"/channels/{family}/{channel}", key_fingerprint).encode(),
                key: public.read_bytes()}
    with tempfile.TemporaryDirectory(prefix="paintedwolf-setup-admission-") as temporary:
        work = Path(temporary)
        # Freeze trust inputs too: all crypto and payload comparisons use the same selected key.
        retained_catalog = work / "keys.json"
        snapshot(catalog, retained_catalog, 65536)
        frozen, frozen_public = selection(retained_catalog, rehearsal)
        if frozen != selected:
            raise ValueError("setup catalog changed during admission")
        if frozen_public.name in {"keys.json", filename, "SHA256SUMS", "SHA256SUMS.asc"}:
            raise ValueError("setup trust filename conflicts with retained admission inputs")
        snapshot(public, frozen_public, 65536)
        if frozen_public.read_bytes() != expected[key]:
            raise ValueError("setup public trust changed during admission")
        retained = work / filename
        snapshot(package, retained, 2 * 1024 * 1024)
        snapshot(checksums, work / "SHA256SUMS", 4096)
        snapshot(signature, work / "SHA256SUMS.asc", 16384)
        sha = digest(retained)
        if (work / "SHA256SUMS").read_bytes() != (sha + "  " + filename + "\n").encode():
            raise ValueError("setup checksums differ from the exact selected package")
        verify("detached", work / "SHA256SUMS", work / "SHA256SUMS.asc", retained_catalog, rehearsal)
        if family == "rpm":
            verify("rpm", retained, None, retained_catalog, rehearsal)
            rpm(retained, native_version, expected, config)
        else:
            deb(retained, native_version, expected, config)
        size = retained.stat().st_size
    return {"format_version": 1, "family": family, "source_commit": source_commit, "version": version,
            "channel": channel, "repository_generation": repository_generation,
            "inventory_sha256": inventory_sha256, "signing_generation": signing_generation,
            "key_fingerprint": key_fingerprint, "rehearsal": rehearsal, "published": False,
            "package": {"file": filename, "sha256": sha, "bytes": size},
            "installed_files": [{"path": path, "sha256": hashlib.sha256(data).hexdigest()}
                                for path, data in sorted(expected.items())]}
