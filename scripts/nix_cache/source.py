"""Verify an existing native application against an immutable Git flake, without cache trust."""
from __future__ import annotations

import copy
import base64
import hashlib
import json
from pathlib import Path
import re
import subprocess
import urllib.parse

from .adapter import nix, path_info
from .admission import STORE_PATH, closure_projection, derivation_projection, validate_source_request


def git(source: Path, *arguments) -> bytes:
    result = subprocess.run(["git", "-c", "core.fsmonitor=false", "-c", "gc.autoDetach=false",
        "-c", "maintenance.autoDetach=false", "-C", str(source), *arguments], capture_output=True, timeout=60)
    if result.returncode:
        raise ValueError("Nix source verification requires its exact local Git revision")
    if len(result.stdout) > 2 << 20:
        raise ValueError("Nix source metadata exceeds its byte bound")
    return result.stdout


def describe_verified_source(request: dict, source: Path, *, source_store: str = "daemon", rehearsal: bool = False) -> dict:
    validate_source_request(request)
    selected = copy.deepcopy(request)
    if (not isinstance(source, Path) or not source.is_absolute() or source.is_symlink() or not source.is_dir()
            or type(rehearsal) is not bool or not isinstance(source_store, str)):
        raise ValueError("Nix source verification requires an explicit physical Git checkout")
    if source_store != "daemon":
        parsed = urllib.parse.parse_qs(source_store.removeprefix("local?"), strict_parsing=True)
        if (not rehearsal or not source_store.startswith("local?") or set(parsed) != {"root"}
                or len(parsed["root"]) != 1 or not Path(parsed["root"][0]).is_absolute()):
            raise ValueError("Nix source verification uses the local daemon or an explicit disposable rehearsal store")
    commit = selected["application_commit"]
    if git(source, "rev-parse", commit + "^{commit}").decode().strip() != commit:
        raise ValueError("Nix source revision differs from its selected application")
    tree = git(source, "rev-parse", commit + "^{tree}").decode().strip()
    if not re.fullmatch("[0-9a-f]{40}", tree):
        raise ValueError("Nix source tree has an invalid identity")
    if (hashlib.sha256(git(source, "show", commit + ":flake.lock")).hexdigest() != selected["flake_lock_sha256"]
            or git(source, "show", commit + ":VERSION").decode().strip() != selected["version"]):
        raise ValueError("Nix source version or lock differs from the selected committed bytes")
    reference = source.resolve().as_uri().replace("file:", "git+file:", 1) + "?rev=" + commit
    evaluated = json.loads(nix("eval", "--extra-experimental-features", "flakes", "--json", "--no-write-lock-file",
        "--store", source_store, reference + "#packages." + selected["system"] + ".default",
        "--apply", "p: { output = toString p; derivation = p.drvPath; }"))
    if (not isinstance(evaluated, dict) or set(evaluated) != {"output", "derivation"}
            or selected["outputs"] != [evaluated["output"]]):
        raise ValueError("Nix output differs from the exact committed application flake")
    outputs = selected["outputs"]
    nix("store", "verify", "--store", source_store, "--recursive", "--no-trust", "--stdin", stdin="\n".join(outputs) + "\n")
    closure = closure_projection(path_info(source_store, outputs), outputs)
    for row in closure.values():
        value = row["nar_hash"]
        if (not value.startswith("sha256-") or len(base64.b64decode(value[7:], validate=True)) != 32
                or base64.b64encode(base64.b64decode(value[7:], validate=True)).decode() != value[7:]):
            raise ValueError("verified Nix source requires canonical SHA-256 NAR identities")
    derivation = closure[outputs[0]]["derivation"]
    if (not isinstance(derivation, str) or not re.fullmatch(STORE_PATH, derivation)
            or derivation != evaluated["derivation"]):
        raise ValueError("Nix output derivation differs from the exact source evaluation")
    derivations = derivation_projection(json.loads(nix("derivation", "show", "--store", source_store, "--stdin",
        stdin=derivation + "\n")), closure, selected)
    digest = hashlib.sha256(json.dumps(closure, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    return {"schema_version": 1, "status": "passed", "request": selected, "source_tree": tree,
            "closure_sha256": digest, "closure": closure, "derivations": derivations,
            "nix_version": nix("--version"), "content_verified": True, "installed_qualified": False,
            "publisher_or_custody_authority": False}
