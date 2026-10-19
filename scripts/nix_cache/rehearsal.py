"""Nonpublishing Nix signature and closure rehearsal with disposable local stores."""
from __future__ import annotations

import json
import os
import platform
from pathlib import Path
import shutil
import tempfile
import urllib.parse
import uuid

from .adapter import construct, nix, path_info, verify
from .admission import inventory, validate_request


def must_refuse(operation, label: str) -> None:
    try:
        operation()
    except ValueError:
        return
    raise ValueError(f"Nix cache rehearsal accepted {label}")


def local_store(root: Path) -> str:
    return "local?" + urllib.parse.urlencode({"root": str(root)})


def main() -> None:
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
        wrong_system = "aarch64-linux" if system == "x86_64-linux" else "x86_64-linux"
        must_refuse(lambda: construct({**request, "system": wrong_system}, policy, files[0], work / "wrong-system", source_store=source), "a mislabeled architecture")
        if len(report["closure"]) < 2 or not report["closure"][output]["references"]:
            raise ValueError("Nix cache rehearsal did not construct a dependent closure")
        must_refuse(lambda: verify(cache, [output], [keys[1]]), "a wrong signature key")
        cold = local_store(work / "cold")
        nix("copy", "--from", cache.as_uri(), "--to", cold, "--trusted-public-keys", keys[0],
            "--option", "extra-trusted-public-keys", "", "--stdin", stdin=output + "\n")
        if set(path_info(cold, [output])) != set(report["closure"]):
            raise ValueError("Nix cold store did not receive the complete closure")
        must_refuse(lambda: nix("copy", "--from", cache.as_uri(), "--to", local_store(work / "wrong-key-cold"),
                               "--trusted-public-keys", keys[1], "--option", "extra-trusted-public-keys", "",
                               "--stdin", stdin=output + "\n"), "cold substitution with a wrong key")
        rotated = work / "rotated"
        shutil.copytree(cache, rotated)
        nix("store", "sign", "--store", rotated.as_uri(), "--key-file", str(files[1]), "--recursive", "--stdin", stdin=output + "\n")
        verify(rotated, [output], keys)
        verify(rotated, [output], [keys[1]])
        inventory(rotated, report["closure"], [keys[1]])
        for label in ("altered-nar", "altered-metadata", "missing-nar", "missing-dependency"):
            broken = work / label
            shutil.copytree(cache, broken)
            nar = next(row["name"] for row in report["objects"] if row["phase"] == "nar")
            metadata = Path(output).name.split("-", 1)[0] + ".narinfo"
            if label == "altered-nar":
                (broken / nar).write_bytes(b"invalid NAR")
            elif label == "altered-metadata":
                path = broken / metadata
                path.write_text(path.read_text().replace("NarSize: ", "NarSize: 1", 1))
            elif label == "missing-nar":
                (broken / nar).unlink()
            else:
                dependency = next(path for path in report["closure"] if path != output)
                (broken / (Path(dependency).name.split("-", 1)[0] + ".narinfo")).unlink()
            must_refuse(lambda: verify(broken, [output], [keys[0]]), label)
        production = {**request, "mode": "production", "cache_prefix": "nix-cache"}
        must_refuse(lambda: validate_request(production, policy), "rehearsal policy in production")
        must_refuse(lambda: validate_request(production, {**policy, "mode": "production"}), "rehearsal key identity in production")
        print(json.dumps({"schema_version": 1, "mode": "rehearsal", "status": "passed", "nix_version": report["nix_version"],
                          "system": system, "closure_paths": len(report["closure"]), "objects": len(report["objects"]),
                          "cold_copy": True, "checks": ["wrong-key", "wrong-key-cold-copy", "rotation-overlap", "rotation-new-key",
                                                          "altered-nar", "altered-metadata", "missing-nar", "missing-dependency", "wrong-system", "production-refusal"]}, sort_keys=True))


if __name__ == "__main__":
    main()
