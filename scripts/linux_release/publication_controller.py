"""Reevaluate retained final campaign inputs inside the protected release controller."""

from __future__ import annotations

from pathlib import Path

from .installed_evidence import artifacts as validate_artifacts
from .publication_bundle import download
from .publication_provenance import select, verify_producer
from .publication_source import facts
from .release_admission import document, verify_release


class SelectedCampaign:
    """Keep selected inputs, rather than a reusable accepted-receipt authority."""

    def __init__(self, root, provenance, source, catalog, signing_generation, key_fingerprint):
        self.root, self.provenance, self.source = root, provenance, source
        self.catalog = catalog
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
        return inputs, report


def retain(api, repository: str, repository_id: int, run_id: int, source_repository: Path,
           source_commit: str, destination: Path, *, catalog, signing_generation, key_fingerprint):
    """The main controller supplies its policy, selected source and production signer."""
    source = facts(source_repository, source_commit)
    provenance = select(api, repository, repository_id, run_id)
    download(provenance, destination)
    selected = SelectedCampaign(destination, provenance, source, catalog, signing_generation, key_fingerprint)
    selected.authorize()
    return selected
