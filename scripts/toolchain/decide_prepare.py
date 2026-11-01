"""Prepare Bialy dependencies without compiling or changing pinned versions."""
import argparse
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]


def prepare(refresh_lock=False):
    manifest = ROOT / "lycaon/internal/decide/native/Cargo.toml"
    if refresh_lock:
        subprocess.run(["cargo", "update", "--workspace", "--manifest-path", str(manifest)], check=True)
    subprocess.run(["cargo", "fetch", "--locked", "--manifest-path", str(manifest)], check=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--refresh-lock", action="store_true")
    prepare(parser.parse_args().refresh_lock)
