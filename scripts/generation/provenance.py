"""Bind runtime measurements to their adapter sources and compiled executable."""

import hashlib
import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).parent


def file_digest(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def source_identity(backend):
    if backend in {"reference-rust", "native"}:
        native = ROOT.parents[1] / "lycaon/internal/generation/native"
        paths = sorted(native.glob("src/**/*.rs")) + sorted(native.glob("vulkan/*")) + [native / "Cargo.toml", native / "Cargo.lock", native / "build.rs"]
        sources = {str(path.relative_to(native)): file_digest(path) for path in paths}
        for path in sorted((native.parent / "templates").glob("*.jinja")):
            sources["templates/" + path.name] = file_digest(path)
        sources["mlx_pin"] = file_digest(ROOT.parents[1] / "third_party/mlx/pin.json")
        for path in sorted((ROOT.parents[1] / "third_party/mlx").glob("*.patch")):
            sources["mlx-patch/" + path.name] = file_digest(path)
        vendor = ROOT.parents[1] / "lycaon/internal/decide/native/vendor/mlx-sys"
        for path in sorted(vendor.rglob("*")):
            if path.is_file() and (path.suffix in {".rs", ".c", ".cpp", ".h", ".hpp", ".cmake"} or path.name in {"Cargo.toml", "CMakeLists.txt"}):
                sources["mlx-sys/" + str(path.relative_to(vendor))] = file_digest(path)
        sources["mlx-offline.cmake"] = file_digest(ROOT.parent / "toolchain/mlx-offline.cmake")
        sources["runtime.py"] = file_digest(ROOT / "runtime.py")
        sources["provenance.py"] = file_digest(ROOT / "provenance.py")
        sources["sources.json"] = file_digest(ROOT / "sources.json")
        sources["sources.py"] = file_digest(ROOT / "sources.py")
        for path in sorted((ROOT / "llama-reference").glob("*.patch")):
            sources[str(path.relative_to(ROOT))] = file_digest(path)
        sources["llama-reference/CMakeLists.txt"] = file_digest(ROOT / "llama-reference/CMakeLists.txt")
        return hashlib.sha256(json.dumps(sources, sort_keys=True).encode()).hexdigest()
    directory = ROOT / ("swift-reference" if backend == "reference-swift" else "llama-reference")
    paths = sorted(p for p in directory.rglob("*") if p.is_file() and (
        p.suffix in {".swift", ".cpp", ".h", ".resolved", ".patch"} or p.name == "CMakeLists.txt"))
    sources = {str(path.relative_to(ROOT)): file_digest(path) for path in paths}
    sources["sources.json"] = file_digest(ROOT / "sources.json")
    return hashlib.sha256(json.dumps(sources, sort_keys=True).encode()).hexdigest()


def swift_identity():
    from artifact_paths import bin_dir
    import swift_toolchain
    return swift_toolchain.identity(bin_dir(ROOT.parents[1]))


def build_identity(backend, directory, binary):
    identity = {"source_identity": source_identity(backend), "binary_sha256": file_digest(binary)}
    if backend == "reference-llama" and (directory / "CMakeCache.txt").is_file():
        identity["cmake_cache_sha256"] = file_digest(directory / "CMakeCache.txt")
    if backend == "reference-swift":
        identity["metallib_sha256"] = file_digest(directory / "metal/mlx.metallib")
        identity["toolchain"] = swift_identity()
    if backend in {"reference-rust", "native"}:
        identity["toolchain"] = {name: subprocess.check_output([name, "--version"], text=True).strip()
                                 for name in ("rustc", "cargo")}
        if sys.platform == "darwin":
            identity["metallib_sha256"] = file_digest(directory / "metal/mlx.metallib")
            identity["sdk"] = {name: subprocess.check_output(["xcrun", "--sdk", "macosx", flag], text=True).strip()
                               for name, flag in (("version", "--show-sdk-version"), ("path", "--show-sdk-path"))}
        if backend == "native" and sys.platform == "linux":
            identity["adapter_sha256"] = file_digest(binary.parent / "libnativegen_vulkan.so")
    return identity


def record_build(backend, directory, binary):
    receipt = build_identity(backend, directory, binary)
    receipts = directory / "build-receipts"
    receipts.mkdir(exist_ok=True)
    (receipts / (backend + ".json")).write_text(json.dumps(receipt, indent=2) + "\n")


def verified_build(backend, directory, binary):
    receipt = json.loads((directory / "build-receipts" / (backend + ".json")).read_text())
    if receipt != build_identity(backend, directory, binary):
        raise ValueError("runtime executable differs from its build sources; rebuild before measuring")
    return receipt
