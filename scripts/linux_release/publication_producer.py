"""Retain complete campaign inputs and reverify them before artifact upload."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shutil
import tempfile

from .installed_artifact_bundle import retain_tree
from .publication_controller import SelectedCampaign
from .publication_source import facts
from .installed_provenance import controller_context
from .publication_authority import PREFIX, repository_origin
from .release_admission import document
from .support_matrix import CATALOG

ROOT_FILES = {"final-artifacts.json", "campaign.json", "repositories.json"}
ROOT_DIRS = {"packages", "evidence", "generations", "nix-cache", "repository-setup"}


def produce(source: Path, destination: Path, immutable: dict, producer: dict, *, catalog,
            signing_generation: int, key_fingerprint: str, repository_base_url=None) -> dict:
    """Only freshly admitted copied inputs receive the producer binding."""
    retain_tree(source, destination, root_files=ROOT_FILES, root_dirs=ROOT_DIRS)
    try:
        with (destination / "producer.json").open("x") as stream:
            json.dump({"format_version": 1, **producer,
                       "source_commit": immutable["source_commit"], "source_tree": immutable["source_tree"]}, stream)
            stream.write("\n")
        selected = SelectedCampaign(destination, producer, immutable, catalog, signing_generation, key_fingerprint,
                                    repository_base_url=repository_base_url)
        _, report = selected.authorize()
        return report
    except BaseException:
        shutil.rmtree(destination)
        raise


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--qualification-run-id", type=int, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--source-repository", type=Path, required=True)
    parser.add_argument("--source-commit", required=True)
    args = parser.parse_args(argv)
    try:
        producer = controller_context(os.environ)
        from .openpgp import selection
        from .publication_generations import CATALOG as signing_catalog
        source = facts(args.source_repository, args.source_commit)
        if args.output.resolve().is_relative_to(args.source_repository.resolve()):
            raise ValueError("campaign retention must not write into the immutable source checkout")
        signer, _ = selection(signing_catalog, False)
        catalog, _ = document(CATALOG)
        from scripts.ci_policy.github import api
        from .publication_raw_campaign import materialize
        with tempfile.TemporaryDirectory(prefix=".linux-qualification-", dir=args.output.absolute().parent) as temporary:
            inputs, provenance = materialize(api, os.environ["GITHUB_REPOSITORY"], producer["repository_id"],
                                             args.qualification_run_id, source, catalog, Path(temporary))
            produce(inputs, args.output, source, producer, catalog=catalog,
                    signing_generation=signer["generation"], key_fingerprint=signer["fingerprint"],
                    repository_base_url=repository_origin(os.environ["LINUX_REPOSITORY_ORIGIN"]) + "/" + PREFIX)
            try:
                with (args.output / "qualification-selection.json").open("x") as stream:
                    json.dump(provenance, stream, indent=2, sort_keys=True)
                    stream.write("\n")
            except BaseException:
                shutil.rmtree(args.output)
                raise
        print(f"Complete Linux campaign inputs retained: {args.output}")
    except (OSError, ValueError, KeyError, TypeError, ImportError) as exc:
        parser.exit(1, f"Linux campaign retention refused: {exc}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
