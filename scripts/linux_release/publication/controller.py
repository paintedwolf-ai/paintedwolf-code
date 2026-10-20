"""Reevaluate retained final campaign inputs inside the protected release controller."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess

from ..installed_evidence import artifacts as validate_artifacts
from ..installed_artifact_bundle import Limits, download
from ..installed_capacity import limits as campaign_limits
from .caches import verify as verify_caches
from .setups import verify as verify_setups
from .authority import PREFIX, repository_origin
from .generations import verify as verify_generations
from ..installed_provenance import controller_context, select_final, verify_producer
from ..release_source import facts
from ..release_admission import document, verify_release
from .files import verify_files
from ..support_matrix import CATALOG


class SelectedCampaign:
    """Keep selected inputs, rather than a reusable accepted-receipt authority."""

    def __init__(self, root, provenance, source, catalog, signing_generation, key_fingerprint, *, repository_base_url=None):
        self.root, self.provenance, self.source = root, provenance, source
        self.catalog = catalog
        self.repository_base_url = repository_base_url
        self.signing_generation, self.key_fingerprint = signing_generation, key_fingerprint

    def inputs(self):
        producer, _ = document(self.root / "producer.json")
        verify_producer(self.provenance, producer, self.source["source_commit"], self.source["source_tree"])
        artifacts, _ = document(self.root / "final-artifacts.json")
        campaign, _ = document(self.root / "campaign.json")
        repositories, _ = document(self.root / "repositories.json")
        for artifact in validate_artifacts(artifacts, self.source["source_commit"]).values():
            identity = artifact.get("identity", {})
            if (identity.get("kind") == "nix-closure"
                    and identity.get("lock_sha256") != self.source.get("flake_lock_sha256")):
                raise ValueError("installed closure lock differs from the immutable release source")
        return {"catalog": self.catalog, "artifacts": artifacts, "campaign": campaign,
                "evidence_root": self.root / "evidence", "file_root": self.root / "packages",
                "repositories": repositories, "source_commit": self.source["source_commit"],
                "version": self.source["version"], "channel": self.source["channel"],
                "release_build": self.source["release_build"],
                "signing_generation": self.signing_generation, "key_fingerprint": self.key_fingerprint}

    def authorize(self):
        inputs = self.inputs()
        report = verify_release(**inputs)
        if report["accepted"] is not True:
            raise ValueError("publication requires the complete freshly admitted final campaign")
        verify_generations(self.root, inputs["repositories"])
        report["verified_caches"] = verify_caches(self.root, inputs)
        report["verified_setups"] = verify_setups(self.root, inputs, base_url=self.repository_base_url)
        return inputs, report


def retain(api, repository: str, repository_id: int, run_id: int, source_repository: Path,
           source_commit: str, destination: Path, *, catalog, signing_generation, key_fingerprint, repository_base_url=None):
    """The main controller supplies its policy, selected source and production signer."""
    source = facts(source_repository, source_commit)
    provenance = select_final(api, repository, repository_id, run_id)
    capacity = campaign_limits(catalog)
    bounds = Limits(**{key: capacity[key] for key in ("max_files", "max_bytes", "max_file")})
    download(provenance, destination, limits=bounds)
    selected = SelectedCampaign(destination, provenance, source, catalog, signing_generation, key_fingerprint,
                                repository_base_url=repository_base_url)
    selected.inputs()
    return selected


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--campaign-run-id", type=int, required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--source-repository", type=Path, required=True)
    parser.add_argument("--retained-dir", type=Path, required=True)
    parser.add_argument("--publication-files", type=Path, required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        controller_context(os.environ)
        from scripts.ci_policy.github import api
        from scripts.linux_release.openpgp import selection
        from .generations import CATALOG as signing_catalog
        destination = args.retained_dir.absolute()
        output = args.receipt.absolute()
        protected_roots = [args.source_repository.resolve(strict=True), args.publication_files.resolve(strict=True),
                           Path(__file__).resolve().parents[3]]
        if (any(destination.resolve().is_relative_to(root) or output.resolve().is_relative_to(root)
                for root in protected_roots) or output.resolve().is_relative_to(destination.resolve())
                or output.exists() or output.is_symlink()):
            raise ValueError("campaign retention and receipt must not replace source or publication inputs")
        signer, _ = selection(signing_catalog, False)
        catalog, _ = document(CATALOG)
        selected = retain(api, os.environ["GITHUB_REPOSITORY"], int(os.environ["GITHUB_REPOSITORY_ID"]),
                          args.campaign_run_id, args.source_repository, args.source_commit, destination,
                          catalog=catalog, signing_generation=signer["generation"], key_fingerprint=signer["fingerprint"],
                          repository_base_url=repository_origin(os.environ["LINUX_REPOSITORY_ORIGIN"]) + "/" + PREFIX)
        inputs, report = selected.authorize()
        current = verify_files(inputs["artifacts"], inputs["source_commit"], args.publication_files)
        with output.open("x") as stream:
            json.dump({"format_version": 1, "provenance": selected.provenance, "source": selected.source,
                       **report, "publication_files": current}, stream, indent=2)
            stream.write("\n")
        print(f"Final Linux publication inputs admitted: {output}")
    except (OSError, ValueError, KeyError, TypeError, ImportError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Linux publication admission refused: {exc}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
