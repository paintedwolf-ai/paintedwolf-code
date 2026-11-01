"""Bind runtime measurements to their adapter sources and compiled executable."""

import hashlib
import json
import subprocess
from pathlib import Path

ROOT = Path(__file__).parent


def file_digest(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def source_identity(backend):
    if backend == "reference-rust":
        native = ROOT.parents[1] / "lycaon/internal/generation/native"
        paths = sorted(native.glob("src/**/*.rs")) + [native / "Cargo.toml", native / "Cargo.lock"]
        sources = {str(path.relative_to(native)): file_digest(path) for path in paths}
        sources["mlx_pin"] = file_digest(ROOT.parents[1] / "third_party/mlx/pin.json")
        vendor = ROOT.parents[1] / "lycaon/internal/decide/native/vendor/mlx-sys"
        for path in sorted(vendor.rglob("*")):
            if path.is_file() and (path.suffix == ".rs" or path.name == "Cargo.toml"):
                sources["mlx-sys/" + str(path.relative_to(vendor))] = file_digest(path)
        sources["mlx-offline.cmake"] = file_digest(ROOT.parent / "toolchain/mlx-offline.cmake")
        sources["runtime.py"] = file_digest(ROOT / "runtime.py")
        return hashlib.sha256(json.dumps(sources, sort_keys=True).encode()).hexdigest()
    directory = ROOT / ("swift-reference" if backend == "reference-swift" else "llama-reference")
    paths = sorted(p for p in directory.rglob("*") if p.is_file() and (
        p.suffix in {".swift", ".cpp", ".h", ".resolved"} or p.name == "CMakeLists.txt"))
    sources = {str(path.relative_to(ROOT)): file_digest(path) for path in paths}
    sources["sources.json"] = file_digest(ROOT / "sources.json")
    return hashlib.sha256(json.dumps(sources, sort_keys=True).encode()).hexdigest()


def swift_identity():
    from artifact_paths import bin_dir
    import swift_toolchain
    return swift_toolchain.identity(bin_dir(ROOT.parents[1]))


def build_identity(backend, directory, binary):
    identity = {"source_identity": source_identity(backend), "binary_sha256": file_digest(binary)}
    if backend in {"reference-swift", "reference-rust"}:
        identity["metallib_sha256"] = file_digest(directory / "metal/mlx.metallib")
        if backend == "reference-swift":
            identity["toolchain"] = swift_identity()
        else:
            identity["toolchain"] = {name: subprocess.check_output([name, "--version"], text=True).strip()
                                     for name in ("rustc", "cargo")}
            identity["sdk"] = subprocess.check_output(["xcrun", "--show-sdk-version"], text=True).strip()
    return identity


def record_build(backend, directory, binary):
    receipt = build_identity(backend, directory, binary)
    (directory / "reference-build.json").write_text(json.dumps(receipt, indent=2) + "\n")


def verified_build(backend, directory, binary):
    receipt = json.loads((directory / "reference-build.json").read_text())
    if receipt != build_identity(backend, directory, binary):
        raise ValueError("reference executable differs from its build sources; rebuild before measuring")
    return receipt
