"""Advertise AUR only after fresh protected recipe and read-only origin admission."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
import re
import tempfile

from ...release_admission import document
from ..trust.authority import PREFIX
from ..trust.generations import CATALOG
from ..trust.repositories import digest

POLICY = Path(__file__).resolve().parents[4] / "packaging/linux/aur-delivery-policy.json"
ORIGIN = "https://downloads.paintedwolf.dev"


def load(path=POLICY):
    declaration, _ = document(path)
    if (not isinstance(declaration, dict) or type(declaration.get("schema_version")) is not int
            or declaration["schema_version"] != 1 or type(declaration.get("enabled")) is not bool):
        raise ValueError("AUR policy requires an explicit version and enabled state")
    if not declaration["enabled"]:
        if set(declaration) != {"schema_version", "enabled"}:
            raise ValueError("disabled AUR cannot declare availability")
        return None
    if (set(declaration) != {"schema_version", "enabled", "channel", "package", "commit"}
            or declaration["channel"] not in {"stable", "preview"}
            or declaration["package"] != "painted-wolf-code" + ("-preview" if declaration["channel"] == "preview" else "") + "-bin"
            or not isinstance(declaration["commit"], str) or not re.fullmatch(r"[0-9a-f]{40}", declaration["commit"])):
        raise ValueError("enabled AUR requires its protected channel, package and exact commit")
    return declaration


def facts(selected):
    policy = load()
    if policy is None:
        return {"enabled": False}
    from ...aur_delivery import plan
    from ...aur_recipe import generate
    from ...aur_release_authority import authorize
    from ...aur_observation import observe
    from ...aur_origin import ReadOnlyOrigin

    inputs, _ = selected.authorize()
    if inputs["channel"] != policy["channel"]:
        raise ValueError("protected AUR channel differs from the selected campaign")
    manifests = [entry["manifest"] for entry in inputs["repositories"]["generations"]
                 if entry["manifest"]["family"] == "pacman"]
    generations = [(manifest, selected.root / "generations" / digest(manifest)) for manifest in manifests]
    with tempfile.TemporaryDirectory(prefix="public-aur-observation-") as temporary:
        root = Path(temporary)
        recipe = root / "recipe"
        metadata = generate(generations, recipe, ORIGIN + "/" + PREFIX, CATALOG, False)
        raw = (selected.root / "final-artifacts.json").read_bytes()
        if json.loads(raw) != inputs["artifacts"]:
            raise ValueError("AUR inventory changed after campaign admission")
        operation = plan(metadata, manifests, recipe, hashlib.sha256(raw).hexdigest())
        if operation["package"] != policy["package"]:
            raise ValueError("generated AUR recipe differs from protected package policy")

        def fresh(operation, directory):
            current, _ = selected.authorize()
            actual = (selected.root / "final-artifacts.json").read_bytes()
            if actual != raw or json.loads(actual) != current["artifacts"]:
                raise ValueError("AUR selected final inventory changed during observation")
            current.pop("artifacts")
            return authorize(operation, directory, artifacts_bytes=actual, **current)

        with ReadOnlyOrigin(root / "origin", policy["package"]) as backend:
            observed = observe(operation, recipe, policy["commit"], backend=backend, authorize=fresh)
        if observed.get("production_available") is not True:
            raise ValueError("fixture observation cannot establish public AUR availability")
        selected.authorize()
        return observed["facts"]
