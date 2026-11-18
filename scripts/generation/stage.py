#!/usr/bin/env python3
"""Build the local generation runtime and optionally stage its authenticated model seed."""

import argparse
import os
from pathlib import Path
import tempfile
import shutil

ROOT = Path(__file__).resolve().parents[2]
import runtime
from staging import publish_file, stage_runtime, stage_seed


def stage(output, resources, model=None):
    bundle = os.environ.get("BIALY_GENERATION_BUNDLE")
    authority = os.environ.get("BIALY_GENERATION_AUTHORITY")
    if model and not (bundle and authority):
        raise ValueError("generation packaging requires BIALY_GENERATION_BUNDLE and BIALY_GENERATION_AUTHORITY together")
    _, build = runtime.paths("native")
    _, backend = runtime.native_environment(build)
    runtime.build(argparse.Namespace(backend="native"))
    stage_runtime(build, build / "release/nativegen", output, resources, backend)
    if not model:
        (resources / "offline-model").unlink(missing_ok=True)
        models = resources / "models"
        if models.exists():
            shutil.rmtree(models)
        if authority:
            publish_file(Path(authority), resources / "release-keys.json")
        else:
            (resources / "release-keys.json").unlink(missing_ok=True)
    else:
        with tempfile.TemporaryDirectory(prefix="generation-verifier-", dir=build) as temporary:
            verifier = Path(temporary) / "model-bundle"
            runtime.run(["go", "build", "-trimpath", "-o", verifier, "./cmd/model-bundle"], cwd=ROOT / "lycaon")
            stage_seed(verifier, Path(bundle), Path(authority), resources, backend, f"gemma4-{model}-paintedwolf")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--resources", type=Path, required=True)
    parser.add_argument("--with-local-model", choices=("e4b", "12b", "26b"))
    args = parser.parse_args()
    stage(args.output, args.resources, args.with_local_model)


if __name__ == "__main__":
    main()
