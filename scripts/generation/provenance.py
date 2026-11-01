"""Bind runtime measurements to their adapter sources and compiled executable."""

import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).parent


def file_digest(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def source_identity(backend):
    directory = ROOT / ("swift-reference" if backend == "reference-swift" else "llama-reference")
    paths = sorted(p for p in directory.rglob("*") if p.is_file() and (
        p.suffix in {".swift", ".cpp", ".resolved"} or p.name == "CMakeLists.txt"))
    sources = {str(path.relative_to(ROOT)): file_digest(path) for path in paths}
    sources["sources.json"] = file_digest(ROOT / "sources.json")
    return hashlib.sha256(json.dumps(sources, sort_keys=True).encode()).hexdigest()


def build_identity(backend, directory, binary):
    identity = {"source_identity": source_identity(backend), "binary_sha256": file_digest(binary)}
    if backend == "reference-swift":
        identity["metallib_sha256"] = file_digest(directory / "metal/mlx.metallib")
    return identity


def record_build(backend, directory, binary):
    receipt = build_identity(backend, directory, binary)
    (directory / "reference-build.json").write_text(json.dumps(receipt, indent=2) + "\n")


def verified_build(backend, directory, binary):
    receipt = json.loads((directory / "reference-build.json").read_text())
    if receipt != build_identity(backend, directory, binary):
        raise ValueError("reference executable differs from its build sources; rebuild before measuring")
    return receipt
