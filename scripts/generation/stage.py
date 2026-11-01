#!/usr/bin/env python3
"""Build the local generation runtime and stage its authenticated model seed."""

import argparse
import os
from pathlib import Path
import tempfile

ROOT = Path(__file__).resolve().parents[2]
import runtime
from staging import stage_runtime, stage_seed


def stage(output, resources, release):
    bundle = os.environ.get("BIALY_GENERATION_BUNDLE")
    authority = os.environ.get("BIALY_GENERATION_AUTHORITY")
    if bool(bundle) != bool(authority) or (release and not bundle):
        raise ValueError("generation packaging requires BIALY_GENERATION_BUNDLE and BIALY_GENERATION_AUTHORITY together")
    _, build = runtime.paths("native")
    _, backend = runtime.native_environment(build)
    runtime.build(argparse.Namespace(backend="native"))
    stage_runtime(build, build / "release/nativegen", output, resources, backend)
    if bundle:
        with tempfile.TemporaryDirectory(prefix="generation-verifier-", dir=build) as temporary:
            verifier = Path(temporary) / "model-bundle"
            runtime.run(["go", "build", "-trimpath", "-o", verifier, "./cmd/model-bundle"], cwd=ROOT / "lycaon")
            stage_seed(verifier, Path(bundle), Path(authority), resources, backend)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--resources", type=Path, required=True)
    parser.add_argument("--release", action="store_true")
    args = parser.parse_args()
    stage(args.output, args.resources, args.release)


if __name__ == "__main__":
    main()
