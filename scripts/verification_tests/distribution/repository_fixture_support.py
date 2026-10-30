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


def halt_distribution(bad, good, channels=("stable", "preview")):
    from scripts.release_distribution_contract.halt_plan import validate
    from scripts.release_distribution_contract.public_history import load
    baseline = load()['releases']
    facts = {'last_good_legacy': good in baseline, 'bad_legacy': bad in baseline,
             'last_good_manifest_sha256': baseline[good]['updater_manifest_sha256'] if good in baseline else 'a' * 64 if good else None,
             'last_good_dmg_sha256': baseline[good]['dmg_sha256'] if good in baseline else None,
             'distribution_sha256': None if good is None or good in baseline else 'c' * 64}
    return validate({"bad_versions": [bad], "channels": [
        {"channel": channel, "bad": bad, "last_good": good, **facts} for channel in channels]})


def generation_prefixes(objects):
    return sorted({key.removesuffix("generation.json") for key in objects
                   if key.startswith("linux/generations/") and key.endswith("/generation.json")})
