"""Combine fresh Linux admission with independently owned release document authority."""
from __future__ import annotations

from pathlib import Path

from .installed_evidence import artifacts
from .publication_files import verify_files

EXTENSIONS = {"deb": "deb", "rpm": "rpm", "pacman": "pkg.tar.zst"}
ARCHITECTURES = {"amd64": "linux-x86_64", "arm64": "linux-aarch64"}


class DistributionAuthority:
    """Linux campaign admission cannot grant authority to other release inputs."""

    def __init__(self, selected, *, authorize_release):
        if not callable(authorize_release):
            raise ValueError("distribution requires independent catalog, updater and delivery admission")
        self.selected = selected
        self.authorize_release = authorize_release

    def __call__(self, binding):
        if (not isinstance(binding, dict) or set(binding) !=
                {"document", "catalog", "updater_manifest", "artifacts", "request"}):
            raise ValueError("distribution requires its complete constructor binding")
        inputs, report = self.selected.authorize()
        document = binding["document"]
        source = self.selected.source
        if (document["source"]["commit"] != source["source_commit"]
                or document["source"]["tree"] != source["source_tree"]
                or document["version"] != inputs["version"]
                or document["channel"] != inputs["channel"]):
            raise ValueError("distribution differs from the freshly admitted Linux source")
        selected = artifacts(inputs["artifacts"], source["source_commit"])
        actual = {row["id"]: row for row in verify_files(inputs["artifacts"],
                                                       source["source_commit"], self.selected.root / "packages")}
        native = {}
        for name, row in selected.items():
            if row["identity"]["kind"] != "file":
                continue
            key = (ARCHITECTURES[row["architecture"]], EXTENSIONS[row["installation"]])
            if key in native:
                raise ValueError("Linux campaign repeats a native package identity")
            native[key] = actual[name]
        for platform, row in document["platforms"].items():
            if not platform.startswith("linux-"):
                continue
            for extension, package in row["packages"].items():
                identity = native.get((platform, extension))
                if identity is None or any(package[field] != identity[field] for field in ("sha256", "bytes")):
                    raise ValueError("distribution package differs from freshly admitted installed bytes")
        nix = document["nix"]
        if nix is not None and (nix["source"]["commit"] != source["source_commit"]
                or nix["source"]["flake_lock_sha256"] != source.get("flake_lock_sha256")):
            raise ValueError("distribution Nix source differs from the immutable admitted lock")
        # The independent owner must bind catalog/updater bytes, all other platforms,
        # controller identity and any advertised repository/cache/AUR availability.
        # A successful Linux campaign supplies none of those unrelated permissions.
        if self.authorize_release(binding, inputs, report) is not True:
            raise ValueError("independent release admission refused distribution facts")
        return True


def prepare(selected, destination: Path, request, catalog: Path, updater_manifest: Path,
            publication_files: Path, *, authorize_release, registry=None, signer=None):
    from scripts.release_distribution_contract.document_signing import prepare as sign
    authority = DistributionAuthority(selected, authorize_release=authorize_release)
    return sign(destination, request, catalog, updater_manifest, publication_files,
                authorize=authority, registry=registry, signer=signer)
