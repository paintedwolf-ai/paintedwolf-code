"""Require fresh protected release reevaluation before separate AUR availability changes."""

import hashlib
import json

from .aur_delivery import recipe_objects
from .repository_objects import encoded
from .repository_operations import identity


def authorize(selected, recipe, *, catalog, artifacts_bytes, campaign, evidence_root, file_root,
              repositories, source_commit, version, channel, signing_generation, key_fingerprint, release_build):
    # This callable must run inside the controller that selected the trusted workflow
    # bundle; a retained accepted receipt never substitutes for this reevaluation.
    from .release_admission import verify_release

    artifacts = json.loads(artifacts_bytes)
    report = verify_release(catalog, artifacts, campaign, evidence_root, file_root, repositories,
                            source_commit, version=version, channel=channel,
                            signing_generation=signing_generation, key_fingerprint=key_fingerprint, release_build=release_build)
    if report.get("accepted") is not True:
        raise ValueError("AUR publication requires the complete freshly admitted Linux campaign and final files")
    expected = {"format_version": 1, "operation": "publish-aur", "source_commit": source_commit,
                "version": version, "channel": channel, "rehearsal": False,
                "package": "painted-wolf-code" + ("-preview" if channel == "preview" else "") + "-bin",
                "final_artifacts_sha256": hashlib.sha256(artifacts_bytes).hexdigest()}
    if any(type(selected.get(key)) is not type(value) or selected.get(key) != value for key, value in expected.items()):
        raise ValueError("AUR recipe delivery differs from the independently selected final release")
    required = {(row["generation"], row["inventory_sha256"]) for row in report["verified_repositories"]
                if row["family"] == "pacman"}
    admitted_manifests = {identity(entry["manifest"]): entry["manifest"] for entry in repositories["generations"]}
    generations = selected.get("generations")
    if (not required or not isinstance(generations, list) or len(generations) != len(required)
            or any(not isinstance(row, dict) or not {"generation", "inventory_sha256", "architecture",
                                                      "signing_generation", "key_fingerprint"}.issubset(row)
                   for row in generations)
            or {(row["generation"], row["inventory_sha256"]) for row in generations} != required
            or any(type(row[key]) is not type(admitted_manifests[row["generation"]][key])
                   or row[key] != admitted_manifests[row["generation"]][key]
                   for row in generations for key in ["architecture", "signing_generation", "key_fingerprint"])):
        raise ValueError("AUR recipe does not bind every independently admitted pacman generation and signer")
    objects = recipe_objects(recipe)
    if objects != selected["recipe_objects"] or hashlib.sha256(encoded(objects)).hexdigest() != selected["recipe_sha256"]:
        raise ValueError("AUR recipe differs from its independently reviewed delivery plan")
    return report
