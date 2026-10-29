"""Disposable signed two-path closures shared by local and real-R2 rehearsals."""
from contextlib import contextmanager
from dataclasses import dataclass
import json
import os
from pathlib import Path
import platform
import tempfile
import urllib.parse
import uuid

from .adapter import construct, nix


@dataclass(frozen=True)
class CacheFixture:
    work: Path
    system: str
    source: str
    output: str
    keys: list[str]
    key_files: list[Path]
    cache: Path
    report: dict


def local_store(root: Path) -> str:
    return "local?" + urllib.parse.urlencode({"root": str(root)})


@contextmanager
def cache_fixture():
    nonce = uuid.uuid4().hex
    system = {"x86_64": "x86_64-linux", "aarch64": "aarch64-linux"}.get(platform.machine())
    if system is None or platform.system() != "Linux":
        raise ValueError("Nix cache rehearsal requires a supported native Linux architecture")
    with tempfile.TemporaryDirectory(prefix="paintedwolf-nix-cache-rehearsal-") as directory:
        work = Path(directory)
        keys, files = [], []
        for generation in (1, 2):
            key = work / f"key-{generation}"
            secret = nix("key", "generate-secret", "--key-name", f"paintedwolf-rehearsal-{nonce}-{generation}")
            descriptor = os.open(key, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(descriptor, "w") as destination:
                destination.write(secret + "\n")
            keys.append(nix("key", "convert-secret-to-public", stdin=secret))
            files.append(key)
        source = local_store(work / "source")
        # Nix's built-in builder creates an input-addressed output referencing a
        # separate manifest, without a shell, network input, or package download.
        expression = ('builtins.derivation { name = "paintedwolf-cache-rehearsal-' + nonce + '"; '
                      'system = "' + system + '"; builder = "builtin:buildenv"; derivations = ""; '
                      'manifest = builtins.toFile "cache-rehearsal-manifest" "disposable fixture"; }')
        built = json.loads(nix("build", "--store", source, "--option", "build-users-group", "", "--no-link", "--json",
                               "--expr", expression, "--option", "max-jobs", "1", "--option", "cores", "1"))
        output = built[0]["outputs"]["out"]
        request = {"schema_version": 1, "mode": "rehearsal", "version": "0.0.0-rehearsal", "application_commit": "0" * 40,
                   "flake_lock_sha256": "0" * 64, "system": system, "outputs": [output],
                   "cache_prefix": f"release-system-tests/{nonce}/nix-cache"}
        policy = {"schema_version": 1, "mode": "rehearsal", "public_keys": [keys[0]]}
        cache = work / "cache"
        report = construct(request, policy, files[0], cache, source_store=source)
        yield CacheFixture(work, system, source, output, keys, files, cache, report)
