"""Build and measure native generation candidates through the task scheduler."""

import argparse
import datetime
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
from artifact_paths import artifact_root, bin_dir, compiler_cache_dir
from sources import ensure_source
import swift_toolchain
import vulkan_tools
from provenance import record_build, verified_build, file_digest

PINS = json.loads(Path(__file__).with_name("sources.json").read_text())


def paths(backend):
    if backend == "reference-swift":
        return Path(__file__).parent / "swift-reference", compiler_cache_dir(ROOT) / "generation-swift"
    if backend == "reference-llama":
        pin = PINS["llama.cpp"]
        source = bin_dir(ROOT) / "generation-sources" / pin["sha256"]
        build = compiler_cache_dir(ROOT) / "generation" / pin["revision"] / platform.system()
        return source, build
    return ROOT / "lycaon/internal/generation/native", compiler_cache_dir(ROOT) / "generation-rust"


def run(command, **kwargs):
    subprocess.run([str(part) for part in command], check=True, **kwargs)


def prepare(args):
    if args.install_build_tools:
        if args.backend in {"reference-swift", "reference-rust"} and sys.platform == "darwin":
            run(["xcodebuild", "-downloadComponent", "MetalToolchain"])
        elif args.backend in {"reference-llama", "native"} and sys.platform == "linux":
            run(["sudo", "-n", "apt-get", "update"])
            run(["sudo", "-n", "apt-get", "install", "-y", "--no-install-recommends",
                 "cmake", "ninja-build", "libvulkan-dev"])
        else:
            raise ValueError("build tool preparation requires Swift/macOS or Vulkan/Linux")
    if args.backend == "reference-swift":
        source, build = paths(args.backend)
        compiler = swift_toolchain.prepare(bin_dir(ROOT))
        run([compiler, "package", "--package-path", source, "--scratch-path", build, "resolve"])
        return
    if args.backend == "reference-llama":
        if sys.platform == "linux":
            vulkan_tools.prepare(bin_dir(ROOT))
        print(ensure_source(PINS["llama.cpp"], bin_dir(ROOT) / "generation-sources"))
    elif args.backend == "reference-mlx":
        if sys.platform != "darwin" or platform.machine() != "arm64":
            raise ValueError("the MLX comparison requires Apple silicon")
        project = Path(__file__).parent
        if args.model:
            if not args.output or not args.artifact_identity:
                raise ValueError("conversion requires --output and --artifact-identity (source SHA256)")
            run(["uv", "run", "--frozen", "--no-sync", "--project", project, "python",
                 project / "convert_mlx.py", "--source", args.model, "--output", args.output,
                 "--source-sha256", args.artifact_identity,
                 *(["--precision", args.precision] if args.precision else [])], env=python_environment())
            return
        if args.refresh_lock:
            run(["uv", "lock", "--project", project])
            retained = artifact_root(ROOT) / "generation" / "uv.lock"
            retained.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(project / "uv.lock", retained)
            print(f"Generated dependency lock: {retained}", flush=True)
        run(["uv", "sync", "--frozen", "--project", project], env=python_environment())
    else:
        if args.backend == "native" and sys.platform == "linux":
            vulkan_tools.prepare(bin_dir(ROOT))
            ensure_source(PINS["llama.cpp"], bin_dir(ROOT) / "generation-sources")
        source, _ = paths(args.backend)
        if args.refresh_lock:
            run(["cargo", "generate-lockfile", "--manifest-path", source / "Cargo.toml"])
        run(["cargo", "fetch", "--locked", "--manifest-path", source / "Cargo.toml"])


def python_environment():
    environment = dict(os.environ)
    environment["UV_PROJECT_ENVIRONMENT"] = str(compiler_cache_dir(ROOT) / "generation-python")
    return environment


def swift_shaders(output):
    dependency = output / "checkouts/mlx-swift/Source/Cmlx"
    shaders = output / "metal"
    shaders.mkdir(parents=True, exist_ok=True)
    run(["cmake", "-S", dependency / "mlx", "-B", shaders,
         "-DCMAKE_BUILD_TYPE=Release", "-DCMAKE_OSX_DEPLOYMENT_TARGET=14.0",
         "-DMLX_BUILD_TESTS=OFF", "-DMLX_BUILD_EXAMPLES=OFF", "-DMLX_METAL_JIT=ON",
         "-DMLX_METAL_PATH=" + str(shaders),
         "-DFETCHCONTENT_SOURCE_DIR_METAL_CPP=" + str(dependency / "metal-cpp"),
         "-DFETCHCONTENT_SOURCE_DIR_JSON=" + str(dependency / "json"),
         "-DFETCHCONTENT_SOURCE_DIR_FMT=" + str(dependency / "fmt")])
    run(["cmake", "--build", shaders, "--parallel", os.environ.get("PW_TEST_WORKERS", "1"),
         "--target", "mlx-metallib"])
    return shaders / "mlx.metallib"


def swift_environment(output):
    return {**os.environ, "PW_REFERENCE_METALLIB": str(output / "metal/mlx.metallib")}


def rust_environment(output):
    return {**os.environ, "CARGO_TARGET_DIR": str(output),
            "MLX_RS_METAL_PATH": str(output / "metal"),
            "PW_REFERENCE_METALLIB": str(output / "metal/mlx.metallib"),
            "MACOSX_DEPLOYMENT_TARGET": "14.0"}


def native_environment(output):
    if sys.platform == "darwin" and platform.machine() == "arm64":
        return rust_environment(output), "mlx"
    if sys.platform == "linux":
        _, library = paths("reference-llama")
        return {**os.environ, "CARGO_TARGET_DIR": str(output),
                "PW_GENERATION_VULKAN_LIB_DIR": str(library),
                "LD_LIBRARY_PATH": str(library)}, "vulkan"
    raise ValueError("native generation requires Apple silicon or Linux Vulkan")


def build(args):
    source, output = paths(args.backend)
    workers = os.environ.get("PW_TEST_WORKERS", "1")
    if args.backend == "reference-swift":
        swift_shaders(output)
        run([swift_toolchain.executable(bin_dir(ROOT)), "build", "--package-path", source, "--scratch-path", output,
             "--configuration", "release", "--jobs", workers, "--product", "generation-swift-reference"])
        record_build(args.backend, output, output / "release/generation-swift-reference")
    elif args.backend == "reference-llama":
        if not (source / ".source-sha256").is_file():
            raise ValueError("run generation:prepare for this candidate first")
        options = ["-DCMAKE_BUILD_TYPE=Release", "-DBUILD_SHARED_LIBS=OFF",
                   "-DLLAMA_BUILD_COMMON=OFF", "-DLLAMA_BUILD_TOOLS=OFF",
                   "-DLLAMA_BUILD_TESTS=OFF", "-DLLAMA_BUILD_EXAMPLES=OFF",
                   "-DGGML_NATIVE=OFF"]
        if sys.platform == "darwin":
            options += ["-DGGML_METAL=ON", "-DGGML_METAL_EMBED_LIBRARY=ON"]
        elif sys.platform == "linux":
            sdk = bin_dir(ROOT) / vulkan_tools.CACHE_NAME
            if not (sdk / "bin/glslc").is_file():
                raise ValueError("prepare the Linux Vulkan tools before building")
            options += ["-DGGML_VULKAN=ON", "-DVulkan_GLSLC_EXECUTABLE=" + str(sdk / "bin/glslc"),
                        "-DVulkan_INCLUDE_DIR=" + str(sdk / "include"), "-DCMAKE_PREFIX_PATH=" + str(sdk)]
        else:
            raise ValueError("generation requires macOS or Linux")
        run(["cmake", "-S", Path(__file__).parent / "llama-reference", "-B", output,
             "-DLLAMA_SOURCE_DIR=" + str(source), *options])
        run(["cmake", "--build", output, "--parallel", workers, "--target", "generation-reference",
             *(["nativegen_vulkan"] if sys.platform == "linux" else [])])
        record_build(args.backend, output, output / "generation-reference")
    elif args.backend == "reference-rust":
        run(["cargo", "build", "--locked", "--release", "--features", "mlx", "--jobs", workers,
             "--bins", "--manifest-path", source / "Cargo.toml"], env=rust_environment(output))
        record_build(args.backend, output, output / "release/nativegen-bench")
    elif args.backend == "native":
        if sys.platform == "linux":
            build(argparse.Namespace(backend="reference-llama"))
        environment, feature = native_environment(output)
        run(["cargo", "build", "--locked", "--release", "--features", feature,
             "--jobs", workers, "--bins", "--manifest-path", source / "Cargo.toml"], env=environment)
        if sys.platform == "linux":
            shutil.copyfile(Path(environment["PW_GENERATION_VULKAN_LIB_DIR"]) / "libnativegen_vulkan.so",
                            output / "release/libnativegen_vulkan.so")
        record_build(args.backend, output, output / "release/nativegen")
    else:
        raise ValueError("the Python reference is prepared rather than compiled")


def test(args):
    if args.backend == "reference-llama":
        run([sys.executable, "-m", "unittest", "test_vulkan_tools", "test_provenance",
             "test_swift_toolchain", "test_native_bench"], cwd=Path(__file__).parent)
        return
    if args.backend == "reference-swift":
        source, output = paths(args.backend)
        swift_shaders(output)
        run([swift_toolchain.executable(bin_dir(ROOT)), "test", "--package-path", source, "--scratch-path", output,
             "--configuration", "release", "--jobs", os.environ.get("PW_TEST_WORKERS", "1")],
            env=swift_environment(output))
        return
    if args.backend == "reference-mlx":
        run(["uv", "run", "--frozen", "--no-sync", "--project", Path(__file__).parent,
             "python", "-m", "unittest", "discover", "-s", Path(__file__).parent,
             "-p", "test_*.py"], env=python_environment())
        return
    if args.backend == "reference-rust":
        source, output = paths(args.backend)
        run(["cargo", "test", "--locked", "--release", "--features", "mlx",
             "--jobs", os.environ.get("PW_TEST_WORKERS", "1"), "--manifest-path", source / "Cargo.toml"],
            env=rust_environment(output))
        return
    source, output = paths("native")
    run([sys.executable, "-m", "unittest", "test_native_bench"], cwd=Path(__file__).parent)
    environment, feature = native_environment(output)
    run(["cargo", "test", "--locked", "--release", "--features", feature,
         "--jobs", os.environ.get("PW_TEST_WORKERS", "1"), "--manifest-path", source / "Cargo.toml"],
        env=environment)


def bench(args):
    if not args.model or not args.model.exists():
        raise ValueError("benchmark requires an existing local --model")
    if not args.artifact_identity:
        raise ValueError("benchmark requires a recorded --artifact-identity")
    destination = artifact_root(ROOT) / "generation-bench" / datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
    destination.mkdir(parents=True)
    state, prompt = 0, [2]
    for _ in range(args.prompt - 1):
        state = (1664525 * state + 1013904223) & 0xffffffff
        prompt.append(256 + state % (262144 - 256))
    if args.messages:
        rendered = destination / "rendered-prompt.json"
        run(["uv", "run", "--frozen", "--no-sync", "--project", Path(__file__).parent,
             "python", Path(__file__).with_name("prompt.py"), args.model, args.messages, rendered],
            env=python_environment())
        prompt = json.loads(rendered.read_text())["prompt_tokens"]
        if (not prompt or len(prompt) + args.tokens > 65536 or
                any(type(token) is not int or not 0 <= token < 262144 for token in prompt)):
            raise ValueError("rendered prompt exceeds its bounds")
        shutil.copyfile(args.messages, destination / "messages.json")
    if args.prompt_file:
        if args.prompt_file.stat().st_size > 1 << 20:
            raise ValueError("rendered prompt file exceeds its bound")
        prompt = json.loads(args.prompt_file.read_text())["prompt_tokens"]
        if (not isinstance(prompt, list) or not prompt or len(prompt) + args.tokens > 65536 or
                any(type(token) is not int or not 0 <= token < 262144 for token in prompt)):
            raise ValueError("rendered prompt exceeds its bounds")
        shutil.copyfile(args.prompt_file, destination / "rendered-prompt.json")
    specification = {"prompt_tokens": prompt, "generation_tokens": args.tokens,
                     "trials": args.trials, "ple": args.ple, "prefill_chunk": args.prefill_chunk, "kv_bits": args.kv_bits,
                     "verify_last_logits": args.verify_last_logits}
    input_path = destination / "input.json"
    input_path.write_text(json.dumps(specification) + "\n")
    build_receipt = None
    if args.backend in {"reference-llama", "reference-swift"}:
        _, build = paths(args.backend)
        binary = build / "generation-reference" if args.backend == "reference-llama" else build / "release/generation-swift-reference"
        build_receipt = verified_build(args.backend, build, binary)
        command = [str(binary), str(args.model.resolve()), str(input_path)]
        environment = swift_environment(build) if args.backend == "reference-swift" else os.environ
    elif args.backend == "reference-rust":
        _, build = paths(args.backend)
        binary = build / "release/nativegen-bench"
        build_receipt = verified_build(args.backend, build, binary)
        command = [str(binary), str(args.model.resolve()), str(input_path)]
        environment = rust_environment(build)
    elif args.backend == "reference-mlx":
        command = ["uv", "run", "--frozen", "--no-sync", "--project", str(Path(__file__).parent),
                   "python", str(Path(__file__).with_name("mlx_bench.py")),
                   "--model", str(args.model.resolve()), "--input", str(input_path)]
        environment = python_environment()
    else:
        _, build = paths(args.backend)
        binary = build / "release/nativegen"
        build_receipt = verified_build(args.backend, build, binary)
        command = [sys.executable, str(Path(__file__).with_name("native_bench.py")),
                   "--binary", str(binary), "--model", str(args.model.resolve()),
                   "--input", str(input_path), "--artifact-identity", args.artifact_identity,
                   "--checkpoint-tokens", str(args.checkpoint_tokens)]
        if sys.platform == "darwin":
            command += ["--metallib", str(build / "metal/mlx.metallib")]
        elif args.device_pci:
            command += ["--device-pci", args.device_pci]
        else:
            raise ValueError("native Vulkan benchmark requires --device-pci")
        environment = os.environ
    metadata = {"backend": args.backend, "source_pins": PINS, "command": command,
                "build_receipt": build_receipt,
                "orchestrator_sha256": file_digest(Path(__file__)),
                "python_sources": {name: file_digest(Path(__file__).with_name(name))
                                   for name in ("native_bench.py", "mlx_bench.py", "mapped_ple.py", "prompt.py", "pyproject.toml", "uv.lock")},
                "artifact_identity": args.artifact_identity, "platform": platform.platform(),
                "machine": platform.machine(), "ple": args.ple, "input": str(input_path),
                "load_average_before": os.getloadavg(),
                "cache_condition": "uncontrolled OS page cache; warmup is not a cold-storage guarantee",
                "scope": "isolated synthetic throughput; not application or minimum-hardware qualification"}
    (destination / "run.json").write_text(json.dumps(metadata, indent=2) + "\n")
    with (destination / "stdout.log").open("w") as stdout, (destination / "stderr.log").open("w") as stderr:
        result = subprocess.run(command, env=environment, stdout=stdout, stderr=stderr)
    metadata["exit_code"] = result.returncode
    metadata["load_average_after"] = os.getloadavg()
    (destination / "run.json").write_text(json.dumps(metadata, indent=2) + "\n")
    print(destination)
    if result.returncode:
        raise RuntimeError("benchmark failed; inspect its retained logs")
    if args.backend == "native":
        for line in (destination / "stdout.log").read_text().splitlines():
            record = json.loads(line)
            print(json.dumps({key: value for key, value in record.items() if key not in {"steps", "tokens"}}))
    else:
        print((destination / "stdout.log").read_text())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["prepare", "build", "test", "bench"])
    parser.add_argument("--backend", choices=["native", "reference-llama", "reference-mlx", "reference-swift", "reference-rust"], default="native")
    parser.add_argument("--refresh-lock", action="store_true")
    parser.add_argument("--install-build-tools", action="store_true")
    parser.add_argument("--model", type=Path)
    prompts = parser.add_mutually_exclusive_group()
    prompts.add_argument("--messages", type=Path)
    prompts.add_argument("--prompt-file", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--artifact-identity")
    parser.add_argument("--device-pci")
    parser.add_argument("--checkpoint-tokens", type=int, default=0)
    parser.add_argument("--verify-last-logits", action="store_true")
    parser.add_argument("--precision", type=Path)
    parser.add_argument("--prompt", type=int, default=512)
    parser.add_argument("--tokens", type=int, default=128)
    parser.add_argument("--prefill-chunk", type=int, default=256)
    parser.add_argument("--kv-bits", type=int, choices=[8, 16], default=16)
    parser.add_argument("--trials", type=int, default=3)
    parser.add_argument("--ple", choices=["resident", "mapped"], default="mapped")
    args = parser.parse_args()
    if args.backend == "reference-rust" and (sys.platform != "darwin" or platform.machine() != "arm64"):
        parser.error("the Rust MLX candidate requires Apple silicon")
    if args.kv_bits != 16 and args.backend != "reference-swift":
        parser.error("quantized global KV is currently measured only by the Swift MLX candidate")
    if not 0 <= args.checkpoint_tokens < 65536 or (args.checkpoint_tokens and args.backend != "native"):
        parser.error("prefix checkpoints require the native backend and fewer than 65536 tokens")
    if args.backend == "native" and args.ple != "mapped":
        parser.error("the production runner uses mapped PLE")
    if args.verify_last_logits and args.backend != "reference-rust":
        parser.error("last-row logit comparison requires the Rust reference backend")
    if not 1 <= args.prefill_chunk <= 256 or not 1 <= args.prompt or not 1 <= args.tokens <= 4096 or args.prompt + args.tokens > 65536 or not 1 <= args.trials <= 20:
        parser.error("benchmark dimensions exceed their bounds")
    {"prepare": prepare, "build": build, "test": test, "bench": bench}[args.action](args)


if __name__ == "__main__":
    main()
