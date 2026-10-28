"""Bind AUR availability to the qualified release, its final files and its published pacman generations."""

import hashlib

from . import release_qualification
from .aur_delivery import recipe_objects
from .repository_objects import encoded
from .repository_operations import identity


def authorize(selected, recipe, *, artifacts_bytes, generations, source_commit, version, channel,
              qualified=release_qualification.require):
    expected = {"format_version": 1, "operation": "publish-aur", "source_commit": source_commit,
                "version": version, "channel": channel, "rehearsal": False,
                "package": "painted-wolf-code" + ("-preview" if channel == "preview" else "") + "-bin",
                "final_artifacts_sha256": hashlib.sha256(artifacts_bytes).hexdigest()}
    if any(type(selected.get(key)) is not type(value) or selected.get(key) != value for key, value in expected.items()):
        raise ValueError("AUR recipe delivery differs from the independently selected final release")
    published = {identity(manifest): manifest for manifest in generations
                 if manifest.get("family") == "pacman" and manifest.get("rehearsal") is False
                 and all(manifest.get(key) == expected[key] for key in ["source_commit", "version", "channel"])}
    rows = selected.get("generations")
    if (not published or not isinstance(rows, list) or len(rows) != len(published)
            or any(not isinstance(row, dict) or row.get("generation") not in published for row in rows)
            or {row["generation"] for row in rows} != set(published)
            or any(type(row.get(key)) is not type(published[row["generation"]][key])
                   or row.get(key) != published[row["generation"]][key]
                   for row in rows for key in ["inventory_sha256", "architecture", "signing_generation", "key_fingerprint"])):
        raise ValueError("AUR recipe does not bind every published pacman generation and signer")
    objects = recipe_objects(recipe)
    if objects != selected["recipe_objects"] or hashlib.sha256(encoded(objects)).hexdigest() != selected["recipe_sha256"]:
        raise ValueError("AUR recipe differs from its independently reviewed delivery plan")
    qualified(source_commit)
    return True
