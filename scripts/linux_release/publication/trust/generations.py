"""Reverify actual immutable repository objects with controller-owned public trust."""

from pathlib import Path

from .repositories import digest
from ...release_admission import document

CATALOG = Path(__file__).resolve().parents[4] / "packaging/linux/signing/keys.json"


def verify(root: Path, plan: dict) -> None:
    for entry in plan["generations"]:
        manifest = entry["manifest"]
        generation = digest(manifest)
        directory = root / "generations" / generation
        retained, _ = document(directory / "generation.json")
        if retained != manifest:
            raise ValueError("retained repository differs from the independently selected generation")
        if manifest["family"] == "pacman":
            from scripts.linux_release.pacman_validation import admit
        else:
            from scripts.linux_release.repository_validation import admit
        # Exact inventory admission and signature verification belong to each format.
        # The bundle cannot supply its own public trust catalog or rehearse production.
        admit(manifest, directory, CATALOG, rehearsal=False)
