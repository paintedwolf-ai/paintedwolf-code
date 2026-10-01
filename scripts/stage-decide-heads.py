#!/usr/bin/env python3
"""Validate and stage a complete decision release from a local artifact directory."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import struct
import tempfile

REQUIRED = {"turn-load", "unit-rank", "code-rank"}
ALLOWED = REQUIRED | {"guide-load", "web-rank"}


def metadata(path):
    with path.open("rb") as source:
        prefix = source.read(8)
        if len(prefix) != 8:
            raise ValueError(f"{path.name}: missing safetensors header")
        length = struct.unpack("<Q", prefix)[0]
        if length > 1024 * 1024:
            raise ValueError(f"{path.name}: oversized safetensors header")
        return json.loads(source.read(length)).get("__metadata__", {})


def stage(manifest_path, source, destination):
    manifest = json.loads(manifest_path.read_text())
    heads = manifest.get("heads", {})
    if manifest.get("version") != 1 or not REQUIRED <= heads.keys() <= ALLOWED:
        raise ValueError("manifest must pin turn-load, unit-rank and code-rank; guide-load and web-rank are optional")
    if not manifest.get("release") or not manifest.get("backbone", {}).get("model"):
        raise ValueError("manifest must identify the release and backbone model")
    preload = manifest.get("preload", {})
    if not preload.get("catalog_revision") or not isinstance(preload.get("options"), dict) or not preload["options"]:
        raise ValueError("manifest must pin the preload catalog and option texts")
    if preload.get("encoding") not in ("joint", "independent") or any(not isinstance(preload.get(k), int) or preload[k] <= 0 for k in ("max_len", "head_tokens")):
        raise ValueError("manifest must identify the preload encoding and token budgets")
    destination.parent.mkdir(parents=True, exist_ok=True)
    # Verify the copied bytes before replacing any existing staged release.
    with tempfile.TemporaryDirectory(prefix=".decide-heads-", dir=destination.parent) as tmp:
        prepared = Path(tmp) / "heads"
        prepared.mkdir()
        for name, pin in heads.items():
            expected = pin.get("sha256", "")
            if not re.fullmatch(r"[0-9a-f]{64}", expected):
                raise ValueError(f"{name}: invalid pinned SHA256")
            artifact = source / f"{name}.safetensors"
            if not artifact.is_file():
                raise ValueError(f"{artifact}: required head missing; restore the release bundle or set BIALY_HEADS_DIR")
            copied = prepared / artifact.name
            shutil.copyfile(artifact, copied)
            actual = hashlib.sha256(copied.read_bytes()).hexdigest()
            if actual != expected:
                raise ValueError(f"{name}: SHA256 {actual} does not match release {expected}")
            header = metadata(copied)
            if header.get("format") != "pw-decide-head/1" or header.get("model") != manifest["backbone"]["model"]:
                raise ValueError(f"{name}: head format or backbone differs from the release")
            if name == "turn-load":
                if header.get("corpus") != preload["catalog_revision"]:
                    raise ValueError("turn-load: training corpus differs from the release vocabulary")
                if header.get("tool_encoding", "joint") != preload["encoding"] or header.get("max_len") != str(preload["max_len"]) or header.get("head_max_len") != str(preload["head_tokens"]):
                    raise ValueError("turn-load: encoding differs from the release vocabulary")
            if header.get("label") != pin.get("label"):
                raise ValueError(f"{name}: head label differs from the release")
        (prepared / "release.json").write_text(json.dumps(manifest, indent=2) + "\n")
        if destination.exists():
            shutil.rmtree(destination)
        shutil.move(str(prepared), str(destination))
    return manifest["release"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("manifest", "source", "destination"):
        parser.add_argument("--" + name, type=Path, required=True)
    args = parser.parse_args()
    try:
        release = stage(args.manifest, args.source, args.destination)
    except (ValueError, OSError) as error:
        parser.exit(1, f"stage-decide-heads: {error}\n")
    print(f"stage-decide-heads: verified {release} at {args.destination}")


if __name__ == "__main__":
    main()
