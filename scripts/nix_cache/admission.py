"""Release identity, explicit trust, and NAR-before-metadata admission."""
from __future__ import annotations

import base64
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import stat

from scripts.release_semver import parse


STORE_PATH = r"/nix/store/[0123456789abcdfghijklmnpqrsvwxyz]{32}-[A-Za-z0-9+._=-]+"
REHEARSAL_KEY = "paintedwolf-rehearsal-"


def public_key(value: str) -> str:
    name, separator, encoded = value.partition(":")
    if not separator or not re.fullmatch(r"[A-Za-z0-9._-]+", name):
        raise ValueError("Nix trust key has an invalid name")
    try:
        if len(base64.b64decode(encoded, validate=True)) != 32:
            raise ValueError("Nix trust key must contain 32 public bytes")
    except (ValueError, TypeError) as error:
        raise ValueError("Nix trust key has invalid public bytes") from error
    return name


def validate_request(request: dict, policy: dict) -> None:
    fields = {"schema_version", "mode", "version", "application_commit", "flake_lock_sha256", "system", "outputs", "cache_prefix"}
    if not isinstance(request, dict) or set(request) != fields or request["schema_version"] != 1:
        raise ValueError("Nix cache request has an unsupported shape")
    if request["mode"] not in {"production", "rehearsal"}:
        raise ValueError("Nix cache mode must be explicit")
    parse(request["version"])
    for key, length in [("application_commit", 40), ("flake_lock_sha256", 64)]:
        if not isinstance(request[key], str) or not re.fullmatch(r"[a-f0-9]{" + str(length) + "}", request[key]):
            raise ValueError(f"Nix cache request has an invalid {key}")
    if request["system"] not in {"x86_64-linux", "aarch64-linux"}:
        raise ValueError("Nix cache request has an unsupported system")
    outputs = request["outputs"]
    if not isinstance(outputs, list) or not outputs or len(outputs) > 128 or any(
        not isinstance(path, str) or not re.fullmatch(STORE_PATH, path) for path in outputs
    ) or len(set(outputs)) != len(outputs):
        raise ValueError("Nix cache requires explicit unique built store outputs")
    prefix = request["cache_prefix"]
    if request["mode"] == "production":
        if prefix != "nix-cache":
            raise ValueError("production Nix cache requires its dedicated prefix")
    elif not isinstance(prefix, str) or not re.fullmatch(r"release-system-tests/[A-Za-z0-9_-]+/nix-cache", prefix):
        raise ValueError("rehearsal Nix cache requires an isolated test prefix")
    if not isinstance(policy, dict) or set(policy) != {"schema_version", "mode", "public_keys"} or policy["schema_version"] != 1 or policy["mode"] != request["mode"]:
        raise ValueError("Nix cache trust policy differs from the requested mode")
    keys = policy["public_keys"]
    if not isinstance(keys, list) or not keys or any(not isinstance(key, str) for key in keys) or len(keys) != len(set(keys)):
        raise ValueError("Nix cache requires an explicit nonempty public trust set")
    for key in keys:
        name = public_key(key)
        if (request["mode"] == "rehearsal") != name.startswith(REHEARSAL_KEY):
            raise ValueError("Nix rehearsal key identity cannot enter production trust")


def closure_projection(value: dict, outputs: list[str]) -> dict:
    if not isinstance(value, dict) or not value or not set(outputs).issubset(value):
        raise ValueError("Nix closure does not contain every requested output")
    result = {}
    for path, row in value.items():
        if not re.fullmatch(STORE_PATH, path) or not isinstance(row, dict):
            raise ValueError("Nix closure has an invalid store identity")
        references = row.get("references")
        if not isinstance(references, list) or not set(references).issubset(value):
            raise ValueError("Nix closure omits a referenced dependency")
        if not isinstance(row.get("narHash"), str) or not row["narHash"] or not isinstance(row.get("narSize"), int) or row["narSize"] <= 0:
            raise ValueError("Nix closure has an invalid content identity")
        result[path] = {"nar_hash": row["narHash"], "nar_bytes": row["narSize"],
                        "references": sorted(references), "derivation": row.get("deriver")}
    return dict(sorted(result.items()))


def derivation_projection(value: dict, closure: dict, request: dict) -> dict:
    if not isinstance(value, dict) or set(value) != {"version", "derivations"} or value["version"] != 4 or not isinstance(value["derivations"], dict):
        raise ValueError("Nix derivation document requires supported schema 4")
    derivations = value["derivations"]
    result = {}
    for output in request["outputs"]:
        path = closure[output]["derivation"]
        row = derivations.get(Path(path).name)
        if not isinstance(row, dict) or row.get("version") != 4 or row.get("system") != request["system"]:
            raise ValueError("Nix output derivation differs from the selected system or supported schema")
        outputs = row.get("outputs")
        if not isinstance(outputs, dict) or not any(
            isinstance(spec, dict) and spec.get("path") in {output, Path(output).name} for spec in outputs.values()
        ):
            raise ValueError("Nix derivation does not declare the selected application output")
        digest = hashlib.sha256(json.dumps(row, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        result[path] = {"sha256": digest, "system": row["system"], "outputs": outputs}
    return result


def object_record(cache: Path, relative: str, phase: str) -> dict:
    path = PurePosixPath(relative)
    if path.is_absolute() or str(path) != relative or ".." in path.parts or not path.parts:
        raise ValueError("Nix cache object must have a normalized relative name")
    current = cache
    for part in path.parts:
        current = current / part
        if current.is_symlink():
            raise ValueError("Nix cache objects cannot traverse symlinks")
    if not stat.S_ISREG(current.stat().st_mode):
        raise ValueError("Nix cache object must be a regular file")
    digest = hashlib.sha256()
    with current.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return {"name": relative, "phase": phase, "bytes": current.stat().st_size, "sha256": digest.hexdigest(),
            "content_type": "application/octet-stream" if phase == "nar" else "text/plain; charset=utf-8",
            "cache_control": "public, max-age=31536000, immutable" if phase == "nar" else "no-cache, no-store, must-revalidate"}


def inventory(cache: Path, closure: dict, keys: list[str]) -> list[dict]:
    content, metadata = {}, []
    key_names = {public_key(key) for key in keys}
    for store_path in closure:
        name = Path(store_path).name.split("-", 1)[0] + ".narinfo"
        record = object_record(cache, name, "narinfo")
        fields, signatures = {}, []
        for line in (cache / name).read_text().splitlines():
            field, separator, value = line.partition(": ")
            if not separator:
                raise ValueError("Nix-generated narinfo has an invalid field")
            if field == "Sig":
                signatures.append(value.partition(":")[0])
            elif field in fields:
                raise ValueError("Nix-generated narinfo repeats a field")
            else:
                fields[field] = value
        if fields.get("StorePath") != store_path or not key_names.intersection(signatures):
            raise ValueError("Nix cache metadata lacks its selected identity or signature")
        url = fields.get("URL", "")
        if not url.startswith("nar/") or "?" in url or "#" in url or "%" in url:
            raise ValueError("Nix cache NAR URL must remain in its dedicated content directory")
        content[url] = object_record(cache, url, "nar")
        if str(content[url]["bytes"]) != fields.get("FileSize"):
            raise ValueError("Nix cache compressed object size differs from metadata")
        metadata.append(record)
    info = object_record(cache, "nix-cache-info", "cache-info")
    records = [content[name] for name in sorted(content)] + sorted(metadata, key=lambda row: row["name"]) + [info]
    paths = list(cache.rglob("*"))
    if any(path.is_symlink() for path in paths):
        raise ValueError("Nix cache cannot contain symlinks")
    if {path.relative_to(cache).as_posix() for path in paths if path.is_file()} != {row["name"] for row in records}:
        raise ValueError("Nix cache contains objects outside its admitted closure")
    return records
