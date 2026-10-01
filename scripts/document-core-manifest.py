#!/usr/bin/env python3
"""Bind the embedded document core to its reviewed Rust sources and build recipe."""
import argparse
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CORE = ROOT / "lycaon/internal/documentcore"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def manifest(binary):
    sources = [CORE / "native/Cargo.toml", CORE / "native/Cargo.lock",
               ROOT / "scripts/build-document-core.sh", Path(__file__).resolve()]
    sources += sorted((CORE / "native/src").rglob("*.rs"))
    return {"target": "wasm32-wasip1", "sha256": digest(binary),
            "sources": {str(path.relative_to(ROOT)): digest(path) for path in sources}}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["check", "generate"])
    parser.add_argument("--binary", type=Path, default=CORE / "core.wasm")
    parser.add_argument("--output", type=Path, default=CORE / "core.manifest.json")
    args = parser.parse_args()
    current = manifest(args.binary)
    if args.mode == "generate":
        args.output.write_text(json.dumps(current, indent=2, sort_keys=True) + "\n")
    elif not args.output.exists() or json.loads(args.output.read_text()) != current:
        raise SystemExit("Embedded document core is stale. Rebuild with ./task setup-dev -- --documents")


if __name__ == "__main__":
    main()
