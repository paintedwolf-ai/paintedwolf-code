"""Construct signed standard file caches from explicit already-built closures."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import tempfile
import urllib.parse

from .admission import STORE_PATH, closure_projection, inventory, validate_request


def nix(*arguments: str, stdin: str | None = None) -> str:
    environment = {key: value for key, value in os.environ.items() if key not in {"NIX_CONFIG", "NIX_REMOTE", "NIX_USER_CONF_FILES"}}
    result = subprocess.run(["nix", "--extra-experimental-features", "nix-command", "--option", "substituters", "",
                             "--option", "extra-substituters", "", "--option", "builders", "", *arguments],
                            input=stdin, capture_output=True, text=True, env=environment, timeout=1800)
    if result.returncode:
        # Key conversion failures can include secret input in diagnostics.
        raise ValueError(f"Nix {arguments[0]} failed with status {result.returncode}")
    return result.stdout.strip()


def path_info(store: str, outputs: list[str]) -> dict:
    return json.loads(nix("path-info", "--json", "--json-format", "1", "--recursive", "--store", store,
                          "--stdin", stdin="\n".join(outputs) + "\n"))


def verify(cache: Path, outputs: list[str], keys: list[str]) -> None:
    nix("store", "verify", "--store", cache.as_uri(), "--recursive", "--sigs-needed", "1",
        "--trusted-public-keys", " ".join(keys), "--option", "extra-trusted-public-keys", "", "--stdin",
        stdin="\n".join(outputs) + "\n")


def signing_identity(key_file: Path, cache: Path) -> str:
    if not key_file.is_absolute() or key_file.is_symlink():
        raise ValueError("Nix signing key must be an explicit regular private file")
    key_file = key_file.resolve(strict=True)
    info = key_file.stat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError("Nix signing key requires owner-only file permissions")
    if key_file.is_relative_to(Path("/nix/store")) or key_file.is_relative_to(cache):
        raise ValueError("Nix signing key cannot be placed in the store or cache")
    if info.st_size > 4096:
        raise ValueError("Nix signing key exceeds its size bound")
    return nix("key", "convert-secret-to-public", stdin=key_file.read_text()).strip()


def construct(request: dict, policy: dict, key_file: Path, cache: Path, *, source_store: str = "daemon") -> dict:
    validate_request(request, policy)
    if not cache.is_absolute() or cache.exists() or cache.is_symlink():
        raise ValueError("Nix cache construction requires a new absolute destination")
    cache = cache.parent.resolve(strict=True) / cache.name
    if request["mode"] == "production" and source_store != "daemon":
        raise ValueError("production cache construction requires the explicit local daemon store")
    public = signing_identity(key_file, cache)
    if public not in policy["public_keys"]:
        raise ValueError("Nix signing identity is outside the selected public trust policy")
    outputs = request["outputs"]
    source = closure_projection(path_info(source_store, outputs), outputs)
    if any(not re.fullmatch(STORE_PATH, source[path]["derivation"] or "") for path in outputs):
        raise ValueError("Nix application outputs require their build derivation identity")
    temporary = Path(tempfile.mkdtemp(prefix=".nix-cache-stage-", dir=cache.parent))
    try:
        query = urllib.parse.urlencode({"secret-key": str(key_file), "compression": "xz", "parallel-compression": "false"})
        nix("copy", "--from", source_store, "--to", temporary.as_uri() + "?" + query, "--stdin", stdin="\n".join(outputs) + "\n")
        verify(temporary, outputs, policy["public_keys"])
        copied = closure_projection(path_info(temporary.as_uri(), outputs), outputs)
        for path in source:
            if path not in copied or any(source[path][field] != copied[path][field] for field in ("nar_hash", "nar_bytes", "references")):
                raise ValueError("Nix cache differs from its selected source closure")
        if set(source) != set(copied):
            raise ValueError("Nix cache changed the selected closure")
        objects = inventory(temporary, source, policy["public_keys"])
        closure_digest = hashlib.sha256(json.dumps(source, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        report = {"schema_version": 1, "status": "passed", "request": request, "public_keys": policy["public_keys"],
                  "nix_version": nix("--version"), "closure_sha256": closure_digest, "closure": source, "objects": objects,
                  "upstream_dependencies": [], "dependency_policy": "complete-closure-in-this-cache"}
        temporary.rename(cache)
        return report
    finally:
        if temporary.exists():
            shutil.rmtree(temporary)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--request", type=Path, required=True)
    parser.add_argument("--trust-policy", type=Path, required=True)
    parser.add_argument("--signing-key-file", type=Path, required=True)
    parser.add_argument("--cache", type=Path, required=True)
    parser.add_argument("--report", type=Path, required=True)
    args = parser.parse_args()
    request, policy = [json.loads(path.read_text()) for path in (args.request, args.trust_policy)]
    report = construct(request, policy, args.signing_key_file, args.cache)
    args.report.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")


if __name__ == "__main__":
    main()
