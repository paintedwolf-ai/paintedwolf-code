"""Fresh independent AUR availability; a publication receipt alone grants no claim."""
from __future__ import annotations

import hashlib
import json
import re

from .aur_delivery import recipe_objects
from .repository_objects import encoded


def observe(selected, recipe, commit, *, backend, authorize):
    """Bracket exact origin bytes with fresh release admission and branch observations."""
    if not isinstance(commit, str) or re.fullmatch(r"[0-9a-f]{40}", commit) is None:
        raise ValueError("AUR availability requires its protected exact recipe commit")
    package = selected.get("package")
    if package not in {"painted-wolf-code-bin", "painted-wolf-code-preview-bin"}:
        raise ValueError("AUR availability requires a maintained channel-specific package")
    if (type(selected.get("rehearsal")) is not bool
            or backend.production is not (selected["rehearsal"] is False)):
        raise ValueError("fixture and production AUR origin authority cannot be exchanged")
    expected_origin = "https://aur.archlinux.org/" + package + ".git"
    if backend.production and backend.origin != expected_origin:
        raise ValueError("AUR observation differs from its protected package origin")
    frozen = encoded(selected)
    objects = recipe_objects(recipe)
    if (objects != selected.get("recipe_objects")
            or hashlib.sha256(encoded(objects)).hexdigest() != selected.get("recipe_sha256")):
        raise ValueError("selected AUR recipe differs from its exact final declarations")
    report = authorize(selected, recipe)
    if not isinstance(report, dict) or report.get("accepted") is not True:
        raise ValueError("AUR availability requires fresh complete release admission")
    if backend.head() != commit:
        raise ValueError("AUR branch no longer offers the selected recipe commit")
    actual = backend.read(commit)
    if set(actual) != {"commit", "message", "objects"} or actual["commit"] != commit:
        raise ValueError("AUR origin did not retain the selected immutable commit")
    message = json.loads(actual["message"])
    if (not isinstance(message, dict) or set(message) != set(selected) | {"previous"}
            or {key: value for key, value in message.items() if key != "previous"} != selected
            or message["previous"] is not None and
            (not isinstance(message["previous"], str) or re.fullmatch(r"[0-9a-f]{40}", message["previous"]) is None)):
        raise ValueError("AUR publisher commit does not bind the selected release plan")
    if set(actual["objects"]) != {row["path"] for row in objects}:
        raise ValueError("AUR origin has missing or additional recipe declarations")
    for row in objects:
        raw = actual["objects"][row["path"]]
        if (not isinstance(raw, bytes) or len(raw) != row["size"]
                or hashlib.sha256(raw).hexdigest() != row["sha256"]):
            raise ValueError("AUR origin recipe bytes differ from fresh selected inputs")
    if backend.head() != commit:
        raise ValueError("AUR branch changed while its recipe was being observed")
    report = authorize(selected, recipe)
    if (not isinstance(report, dict) or report.get("accepted") is not True
            or encoded(selected) != frozen or recipe_objects(recipe) != objects):
        raise ValueError("AUR release or recipe admission changed during observation")
    # These are aggregate identities, never a claim that one CPU archive has this hash.
    facts = {"enabled": True, "git_url": expected_origin, "commit": commit,
             "source_sha256": selected["recipe_sha256"],
             "package_sha256": selected["final_artifacts_sha256"]}
    return {"facts": facts, "production_available": backend.production,
            "identity_semantics": {"source_sha256": "canonical-recipe-object-inventory",
                                   "package_sha256": "complete-final-artifact-inventory"},
            "admission_sha256": hashlib.sha256(encoded({"selection": selected, "facts": facts})).hexdigest()}
