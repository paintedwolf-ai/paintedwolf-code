"""Native tool requirements and non-expiring rehearsal keys for committed repository fixtures."""

import json
from pathlib import Path
import shutil
import sys
import unittest

from scripts.linux_release.openpgp import isolated_home, primary_fingerprints, run


def native_tools(*names):
    """Linux hosts must provide every native tool; other hosts cannot read these formats."""
    if sys.platform != "linux":
        raise unittest.SkipTest("native Linux package formats require a Linux host")
    missing = [name for name in names if shutil.which(name) is None]
    if missing:
        raise AssertionError("Linux distribution tests require native tools: " + ", ".join(missing))


def signing_key(destination: Path, name: str, catalog: str) -> Path:
    """A rehearsal catalog whose key never expires, so committed signatures stay verifiable."""
    destination.mkdir(mode=0o700, parents=True, exist_ok=True)
    with isolated_home() as home:
        run(["gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "", "--quick-generate-key",
             f"Painted Wolf repository fixture {name} <fixture@example.invalid>", "rsa3072", "sign", "never"], home)
        [fingerprint] = primary_fingerprints(run(["gpg", "--batch", "--with-colons", "--fingerprint",
                                                  "--list-secret-keys"], home))
        (destination / f"{name}-public.asc").write_bytes(run(["gpg", "--batch", "--armor", "--export", fingerprint], home))
        secret = destination / f"{name}-secret.asc"
        secret.write_bytes(run(["gpg", "--batch", "--armor", "--export-secret-keys", fingerprint], home))
        secret.chmod(0o600)
    (destination / catalog).write_text(json.dumps({
        "format_version": 1, "rehearsal": True, "signing_generation": 1,
        "generations": [{"generation": 1, "fingerprint": fingerprint, "public_key": f"{name}-public.asc"}]}, indent=2) + "\n")
    return destination / catalog


def trust(testdata: Path, work: Path, name="keys.json", rehearsal=True) -> Path:
    """Copy a committed public-only catalog; production mode selects the same key as production trust."""
    work.mkdir(parents=True, exist_ok=True)
    catalog = json.loads((testdata / name).read_text())
    for row in catalog["generations"]:
        shutil.copyfile(testdata / row["public_key"], work / row["public_key"])
    (work / name).write_text(json.dumps({**catalog, "rehearsal": rehearsal}))
    return work / name


def generation(testdata: Path, name: str, work: Path) -> tuple:
    directory = work / name
    shutil.copytree(testdata / name, directory)
    return json.loads((directory / "generation.json").read_text()), directory
