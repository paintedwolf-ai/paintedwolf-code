"""Authenticated dataset ancestry checked before decoding any training rows."""
import hashlib
import hmac
import json
import os
from pathlib import Path
import stat

from common import digest
from checkpoint import atomic_json


ALLOWED_SOURCES = {"synthetic", "permitted_repository", "factory_capture", "factory_review"}


def key_bytes(path=None):
    path = Path(path or os.environ["PW_TRAINING_SOURCE_KEY"])
    if path.is_symlink() or not stat.S_ISREG(path.stat().st_mode) or path.stat().st_mode & 0o077:
        raise ValueError("source authority key must be a private regular file")
    key = path.read_bytes()
    if len(key) < 32:
        raise ValueError("source authority key is too short")
    return key


def signature(payload, key):
    return hmac.new(key, digest(payload).encode(), hashlib.sha256).hexdigest()


def check_ancestry(sources, identifiers):
    checked, visiting = set(), set()
    def visit(identifier):
        if identifier in visiting:
            raise ValueError("cyclic source ancestry")
        if identifier in checked:
            return
        source = sources.get(identifier)
        if not source or source.get("source_class") not in ALLOWED_SOURCES or source.get("training_allowed") is not True:
            raise ValueError("source ancestry is not permitted for training")
        if source["source_class"] == "permitted_repository" and not source.get("permission_sha256"):
            raise ValueError("repository source has no permission receipt")
        parents = source.get("parents", [])
        if source["source_class"].startswith("factory_") and not parents:
            raise ValueError("derived source has no registered parent")
        visiting.add(identifier)
        for parent in parents:
            visit(parent)
        visiting.remove(identifier)
        checked.add(identifier)
    if not identifiers:
        raise ValueError("row has no registered source")
    for identifier in identifiers:
        visit(identifier)


def seal(paths, approved, sources, authority, out, key_path=None):
    if not authority.get("run_id") or not authority.get("lineage_sha256") or not authority.get("host_commit"):
        raise ValueError("source authority is incomplete")
    files = {}
    for path in map(Path, paths):
        raw = path.read_bytes()
        rows = [json.loads(line) for line in raw.splitlines() if line.strip()]
        bindings = {}
        for row in rows:
            checksum = digest(row)
            binding = approved.get(checksum)
            if not binding or binding.get("root") != row.get("split_group"):
                raise ValueError("row has no factory admission")
            if binding.get("partition") not in {"train", "validation", "development", "system_test"}:
                raise ValueError("row partition is not registered")
            check_ancestry(sources, binding.get("sources", []))
            bindings[checksum] = binding
        if path.name in files:
            raise ValueError("duplicate source manifest filename")
        files[path.name] = {"sha256": hashlib.sha256(raw).hexdigest(), "rows": bindings}
    payload = {"schema": "pw-training-source/1", "authority": authority, "sources": sources, "files": files}
    atomic_json(out, {"payload": payload, "signature": signature(payload, key_bytes(key_path))})
    return payload


def verify_file(path, manifest_path=None, key_path=None, expected_partition=None):
    path = Path(path)
    manifest_path = Path(manifest_path or os.environ.get("PW_TRAINING_SOURCE_MANIFEST") or path.parent / "source.json")
    envelope = json.loads(manifest_path.read_text())
    payload = envelope["payload"]
    if payload.get("schema") != "pw-training-source/1" or not hmac.compare_digest(envelope["signature"], signature(payload, key_bytes(key_path))):
        raise ValueError("source manifest authentication failed")
    entry = payload["files"].get(path.name)
    raw = path.read_bytes()
    if not entry or entry["sha256"] != hashlib.sha256(raw).hexdigest():
        raise ValueError("dataset is outside its authenticated source manifest")
    rows = [json.loads(line) for line in raw.splitlines() if line.strip()]
    for row in rows:
        binding = entry["rows"].get(digest(row))
        if not binding or binding["root"] != row.get("split_group"):
            raise ValueError("row source binding changed")
        if expected_partition is not None and binding["partition"] != expected_partition:
            raise ValueError("dataset partition does not match its registered use")
        check_ancestry(payload["sources"], binding["sources"])
    return rows, payload


def derive(paths, inputs, out, key_path=None):
    approved, sources, authority = {}, {}, None
    for path in inputs:
        _, payload = verify_file(path, key_path=key_path)
        if authority is not None and payload["authority"] != authority:
            raise ValueError("cannot combine different source authorities")
        authority = payload["authority"]
        sources.update(payload["sources"])
        for entry in payload["files"].values():
            approved.update(entry["rows"])
    return seal(paths, approved, sources, authority, out, key_path)


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--request", type=Path, required=True)
    parser.add_argument("--key", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    request = json.loads(args.request.read_text())
    seal(request["files"], request["approved"], request["sources"], request["authority"], args.out, args.key)
