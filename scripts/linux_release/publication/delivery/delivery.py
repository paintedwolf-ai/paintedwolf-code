"""Combine independently admitted releases with fresh, read-only delivery observations."""
from __future__ import annotations

import hashlib
from pathlib import Path
import tempfile

from ..trust.authority import PREFIX
from ..document.distribution import InactiveDeliveryReleaseAuthority
from ..trust.generations import CATALOG
from ..trust.repositories import digest
from .repository_observation import ReadOnlyOrigin, observe
from ...openpgp import selection
from ...release_admission import document

ORIGIN = "https://downloads.paintedwolf.dev"


def artifact(path, url, content_type):
    raw = path.read_bytes()
    return {"file": path.name, "url": url, "sha256": hashlib.sha256(raw).hexdigest(),
            "bytes": len(raw), "content_type": content_type}


def repository_facts(selected, inputs, report, backend):
    """Only exact selected activation and opt-in setup bytes become public facts."""
    _, public = selection(CATALOG, False)
    setups = {row["repository_generation"]: row for row in report["verified_setups"]}
    result = []
    for entry in inputs["repositories"]["generations"]:
        manifest, operation = entry["manifest"], entry["operation"]
        generation = digest(manifest)
        directory = selected.root / "generations" / generation
        observed = observe(manifest, directory, operation, backend=backend)["facts"]
        base = backend.public + "/generations/" + generation
        if manifest["family"] == "pacman":
            from ...pacman_publication import configuration
            with tempfile.TemporaryDirectory(prefix="public-pacman-setup-") as temporary:
                setup = Path(temporary) / "client.conf"
                setup.write_text(configuration(manifest, observed["base_url"], manifest["key_fingerprint"]))
                setup_key = "generations/" + generation + "/client.conf"
                setup_fact = artifact(setup, backend.public + "/" + setup_key, "text/plain")
                backend.verify_public(setup_key, setup, expected_identity={key: setup_fact[key] for key in ("sha256", "bytes")})
            setup_key = "generations/" + generation + "/client.conf"
            content_type = "text/plain"
        else:
            checked = setups.get(generation)
            if checked is None:
                raise ValueError("advertised repository lacks its independently admitted opt-in setup")
            setup = selected.root / "repository-setup" / generation / checked["package"]["file"]
            setup_key = "generations/" + generation + "/setup/" + setup.name
            content_type = "application/vnd.debian.binary-package" if manifest["family"] == "deb" else "application/x-rpm"
            if artifact(setup, backend.public + "/" + setup_key, content_type)["sha256"] != checked["package"]["sha256"]:
                raise ValueError("repository setup changed after independent admission")
        if manifest["family"] != "pacman":
            setup_fact = artifact(setup, backend.public + "/" + setup_key, content_type)
            if {key: setup_fact[key] for key in ("sha256", "bytes")} != {key: checked["package"][key] for key in ("sha256", "bytes")}:
                raise ValueError("repository setup identity changed after independent admission")
            backend.verify_public(setup_key, setup, expected_identity={key: setup_fact[key] for key in ("sha256", "bytes")})
        key_fact = artifact(public, base + "/" + public.name, "text/plain")
        backend.verify_public("generations/" + generation + "/" + public.name, public,
                              expected_identity={key: key_fact[key] for key in ("sha256", "bytes")})
        fields = ("family", "architecture", "channel", "generation", "inventory_sha256",
                  "activation_sha256", "base_url", "signing_generation", "key_fingerprint")
        result.append({**{key: observed[key] for key in fields}, "setup": setup_fact, "public_key": key_fact})
    return result


def cache_facts(selected, inputs, backend):
    """The cache owner observes transport outside local campaign admission."""
    from ..trust.cache_policy import POLICY, load
    from scripts.nix_cache.observation import observe as observe_cache
    from scripts.nix_cache.publication import encoded
    from scripts.linux_release.nix_closure import nar_digest
    policy = load(POLICY)
    if policy is None:
        return {"enabled": False}
    observations, closures = [], []
    for system in ("aarch64-linux", "x86_64-linux"):
        directory = selected.root / "nix-cache" / system
        retained, _ = document(directory / "report.json")
        if backend is None:
            from scripts.nix_cache.r2 import R2Transport
            backend = R2Transport(retained["request"])
        observed = observe_cache(directory / "cache", retained, selection=inputs, trust_policy=policy, backend=backend)
        facts = observed["facts"]
        if facts["request"]["system"] != system or len(facts["request"]["outputs"]) != 1:
            raise ValueError("cache delivery differs from its selected native default output")
        output = facts["request"]["outputs"][0]
        closures.append({"system": system, "store_path": output,
                         "nar_sha256": nar_digest(retained["closure"][output]["nar_hash"])})
        observations.append(observed)
    return {"enabled": True, "base_url": "https://downloads.paintedwolf.dev/nix-cache",
            "public_keys": policy["public_keys"], "closures": closures,
            "admission_sha256": hashlib.sha256(encoded({"observations": observations})).hexdigest()}


class ObservedDeliveryReleaseAuthority:
    """Every signing callback repeats independent release and real delivery admission."""

    def __init__(self, selected, released, controller_commit, *, repositories=None, cache=None):
        self.selected = selected
        self.released = released
        self.controller_commit = controller_commit
        # Reuse the existing constructor validation for the protected controller identity.
        InactiveDeliveryReleaseAuthority(released, controller_commit)
        self.repositories = repositories or ReadOnlyOrigin(ORIGIN, PREFIX)
        self.cache = cache

    def facts(self):
        inputs, report = self.selected.authorize()
        repositories = repository_facts(self.selected, inputs, report, self.repositories)
        cache = cache_facts(self.selected, inputs, self.cache)
        self.selected.authorize()
        return repositories, cache

    def __call__(self, binding, inputs, report):
        released = self.released.authorize(binding)
        if (not isinstance(released, dict) or set(released) != {"provenance", "metadata", "non_linux_packages"}
                or report.get("accepted") is not True):
            raise ValueError("delivery document requires fresh independent release inputs")
        document = binding["document"]
        if (document["mode"] != "production" or document["origin"] != ORIGIN
                or document["controller_commit"] != self.controller_commit
                or document["aur"] != {"enabled": False}):
            raise ValueError("delivery document differs from protected policy; AUR availability is not established")
        repositories, cache = self.facts()
        actual = document["repositories"]
        if sorted(actual, key=lambda row: row["generation"]) != sorted(repositories, key=lambda row: row["generation"]):
            raise ValueError("advertised repositories differ from fresh delivery observations")
        nix = document["nix"]
        if (nix is None and cache != {"enabled": False}) or nix is not None and nix["cache"] != cache:
            raise ValueError("advertised cache differs from fresh delivery observations")
        return True
