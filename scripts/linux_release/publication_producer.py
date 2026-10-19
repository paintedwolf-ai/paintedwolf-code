"""Retain complete campaign inputs and reverify them before artifact upload."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shutil
import stat

from .publication_bundle import MAX_BYTES, MAX_FILES
from .publication_controller import SelectedCampaign
from .publication_source import facts
from .release_admission import document
from .support_matrix import CATALOG

ROOT_FILES = {"final-artifacts.json", "campaign.json", "repositories.json"}
ROOT_DIRS = {"packages", "evidence", "generations", "nix-cache", "repository-setup"}


def copy_inputs(source: Path, destination: Path) -> None:
    """Copy bounded regular files through pinned directory descriptors."""
    if (source.is_symlink() or not source.is_dir() or destination.exists() or destination.is_symlink()
            or destination.resolve().is_relative_to(source.resolve())):
        raise ValueError("campaign production requires a fresh independent destination")
    count, total = 0, 0
    destination.mkdir(mode=0o700)
    try:
        for parent, directories, files, descriptor in os.fwalk(source, follow_symlinks=False):
            relative = Path(parent).relative_to(source)
            if relative == Path("."):
                if set(files) != ROOT_FILES or not set(directories) <= ROOT_DIRS:
                    raise ValueError("campaign input root contains missing or unexpected entries")
            output = destination / relative
            output.mkdir(mode=0o700, exist_ok=True)
            for name in directories:
                count += 1
                if count > MAX_FILES:
                    raise ValueError("campaign exceeds its entry count bound")
                if not stat.S_ISDIR(os.stat(name, dir_fd=descriptor, follow_symlinks=False).st_mode):
                    raise ValueError("campaign directories must not be links or special files")
            for name in files:
                count += 1
                if count > MAX_FILES:
                    raise ValueError("campaign exceeds its file count bound")
                fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=descriptor)
                with os.fdopen(fd, "rb") as incoming:
                    before = os.fstat(incoming.fileno())
                    if not stat.S_ISREG(before.st_mode) or before.st_size > 16 << 30:
                        raise ValueError("campaign requires bounded regular files")
                    total += before.st_size
                    if total > MAX_BYTES or shutil.disk_usage(destination).free < before.st_size + (1 << 30):
                        raise ValueError("campaign exceeds its size or available disk bound")
                    target = output / name
                    with target.open("xb") as outgoing:
                        remaining = before.st_size
                        while remaining:
                            block = incoming.read(min(remaining, 1 << 20))
                            if not block:
                                raise ValueError("campaign file changed during retention")
                            outgoing.write(block)
                            remaining -= len(block)
                        if incoming.read(1):
                            raise ValueError("campaign file grew during retention")
                    target.chmod(0o600)
                    after = os.fstat(incoming.fileno())
                    current = os.stat(name, dir_fd=descriptor, follow_symlinks=False)
                    identity = lambda row: (row.st_dev, row.st_ino, row.st_size, row.st_mtime_ns, row.st_ctime_ns)
                    if identity(before) != identity(after) or identity(after) != identity(current):
                        raise ValueError("campaign file changed or was replaced during retention")
    except BaseException:
        shutil.rmtree(destination)
        raise


def produce(source: Path, destination: Path, immutable: dict, producer: dict, *, catalog,
            signing_generation: int, key_fingerprint: str) -> dict:
    """Only freshly admitted copied inputs receive the producer binding."""
    copy_inputs(source, destination)
    try:
        with (destination / "producer.json").open("x") as stream:
            json.dump({"format_version": 1, **producer,
                       "source_commit": immutable["source_commit"], "source_tree": immutable["source_tree"]}, stream)
            stream.write("\n")
        selected = SelectedCampaign(destination, producer, immutable, catalog, signing_generation, key_fingerprint)
        _, report = selected.authorize()
        return report
    except BaseException:
        shutil.rmtree(destination)
        raise


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inputs", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--source-repository", type=Path, required=True)
    parser.add_argument("--source-commit", required=True)
    args = parser.parse_args(argv)
    try:
        from .openpgp import selection
        from .publication_generations import CATALOG as signing_catalog
        if (os.environ.get("GITHUB_EVENT_NAME") != "workflow_dispatch"
                or os.environ.get("GITHUB_REF") != "refs/heads/main"
                or os.environ.get("GITHUB_WORKFLOW_REF") !=
                f"{os.environ['GITHUB_REPOSITORY']}/.github/workflows/release.yml@refs/heads/main"):
            raise ValueError("campaign retention requires the protected main release controller")
        producer = {"repository_id": int(os.environ["GITHUB_REPOSITORY_ID"]),
                    "run_id": int(os.environ["GITHUB_RUN_ID"]),
                    "run_attempt": int(os.environ["GITHUB_RUN_ATTEMPT"]),
                    "controller_commit": os.environ["GITHUB_SHA"]}
        if any(producer[key] < 1 for key in ["repository_id", "run_id", "run_attempt"]):
            raise ValueError("campaign producer IDs must be positive")
        source = facts(args.source_repository, args.source_commit)
        if args.output.resolve().is_relative_to(args.source_repository.resolve()):
            raise ValueError("campaign retention must not write into the immutable source checkout")
        signer, _ = selection(signing_catalog, False)
        catalog, _ = document(CATALOG)
        produce(args.inputs, args.output, source, producer, catalog=catalog,
                signing_generation=signer["generation"], key_fingerprint=signer["fingerprint"])
        print(f"Complete Linux campaign inputs retained: {args.output}")
    except (OSError, ValueError, KeyError, TypeError, ImportError) as exc:
        parser.exit(1, f"Linux campaign retention refused: {exc}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
