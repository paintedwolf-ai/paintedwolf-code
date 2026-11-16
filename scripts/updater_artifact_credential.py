#!/usr/bin/env python3
"""Prove the updater artifact key signs for the release's signing generation."""
from __future__ import annotations

import os
from pathlib import Path
import subprocess
import sys
import tempfile

from feed_signing import verify
from update_keys import generation, load_registry

SECRET = "TAURI_SIGNING_PRIVATE_KEY"


def check(registry: dict) -> int:
    """Sign a temporary probe and verify it with the committed artifact key."""
    if not os.environ.get(SECRET):
        raise ValueError(f"{SECRET} is required to sign updater archives")
    number = registry["signing_generation"]
    public_key = generation(registry, number)["public_key"]
    env = {**os.environ, SECRET + "_PASSWORD": os.environ.get(SECRET + "_PASSWORD", "")}
    with tempfile.TemporaryDirectory(prefix="artifact-credential-") as directory:
        probe = Path(directory) / "probe"
        probe.write_text("credential probe\n")
        # Signer diagnostics can contain credentials.
        result = subprocess.run(["bun", "run", "tauri", "signer", "sign", str(probe.resolve())],
                                cwd=Path(__file__).resolve().parent.parent / "lycaon-den", env=env, capture_output=True)
        if result.returncode:
            raise ValueError(f"{SECRET} cannot sign; check the key and its password")
        try:
            verify(probe, Path(str(probe) + ".sig").read_text(), public_key)
        except ValueError:
            raise ValueError(f"{SECRET} does not match the artifact key of generation {number}") from None
    return number


def main() -> None:
    try:
        print(f"updater artifact key signs for generation {check(load_registry())}")
    except (OSError, ValueError, KeyError) as error:
        print(f"error: {error}", file=sys.stderr)
        raise SystemExit(1) from None


if __name__ == "__main__":
    main()
