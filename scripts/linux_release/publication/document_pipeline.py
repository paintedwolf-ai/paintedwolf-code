"""Prepare signed immutable distribution metadata without activating public delivery."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shutil

from ..installed_evidence import artifacts
from ..installed_provenance import controller_context
from .controller import retain as retain_campaign
from .distribution import InactiveDeliveryReleaseAuthority, prepare
from .files import verify_files
from .release_inputs import retain as retain_release
from ..release_admission import document
from ..release_source import facts

TYPES = {"dmg": "application/x-apple-diskimage", "exe": "application/vnd.microsoft.portable-executable",
         "deb": "application/vnd.debian.binary-package", "rpm": "application/x-rpm", "pkg.tar.zst": "application/zstd"}
ARCHES = {"amd64": "linux-x86_64", "arm64": "linux-aarch64"}
FAMILIES = {"deb": "deb", "rpm": "rpm", "pacman": "pkg.tar.zst"}


def package_files(selected, released, source, destination):
    """Canonical public names cannot change installed or independently signed bytes."""
    from scripts.release_distribution_contract.document import snapshot
    from scripts.release_distribution_contract.platforms import load_platforms
    catalog = load_platforms(released.root / "catalog.json")
    destination.mkdir(mode=0o700)
    inputs, _ = selected.authorize()
    verified = {row["id"]: row for row in verify_files(inputs["artifacts"], source["source_commit"], selected.root / "packages")}
    native = {}
    for name, row in artifacts(inputs["artifacts"], source["source_commit"]).items():
        if row["identity"]["kind"] != "file":
            continue
        key = (ARCHES[row["architecture"]], FAMILIES[row["installation"]])
        if key in native:
            raise ValueError("installed campaign repeats a native publication identity")
        native[key] = name
    content_types = {}
    for platform, row in catalog.items():
        if row["publication"] != "public":
            continue
        for extension in row["package_extensions"]:
            name = f"painted-wolf-code_v{source['version']}_{platform}.{extension}"
            if extension not in TYPES:
                raise ValueError("public package has no maintained content type")
            if platform in ARCHES.values():
                artifact_id = native.get((platform, extension))
                if artifact_id is None:
                    raise ValueError("public native package was not independently installed and admitted")
                origin = selected.root / "packages" / artifact_id
                expected = {key: verified[artifact_id][key] for key in ("sha256", "bytes")}
            else:
                origin = released.root / "artifacts" / name
                expected = snapshot(origin)[0]
            shutil.copyfile(origin, destination / name)
            (destination / name).chmod(0o600)
            if snapshot(destination / name)[0] != expected:
                raise ValueError("publication package changed while assembling document inputs")
            content_types[name] = TYPES[extension]
    return content_types, catalog


def produce(api, repository, producer, source_repository, source_commit, campaign_run_id,
            release_run_id, release_artifact_id, destination, *, catalog, signing_generation, package_signing_generation, key_fingerprint, repository_base_url, enabled_delivery=False):
    if (destination.exists() or destination.is_symlink()
            or destination.resolve().is_relative_to(source_repository.resolve())):
        raise ValueError("distribution preparation requires fresh independent retained inputs")
    source = facts(source_repository, source_commit)
    destination.mkdir(mode=0o700)
    try:
        selected = retain_campaign(api, repository, producer["repository_id"], campaign_run_id, source_repository,
            source_commit, destination / "linux", catalog=catalog, signing_generation=package_signing_generation,
            key_fingerprint=key_fingerprint, repository_base_url=repository_base_url)
        released = retain_release(api, repository, producer["repository_id"], release_run_id, release_artifact_id,
                                  destination / "release", source, producer["controller_commit"])
        content_types, platforms = package_files(selected, released, source, destination / "packages")
        linux = any(key in ARCHES.values() and row["publication"] == "public" for key, row in platforms.items())
        request = {"mode": "production", "version": source["version"], "origin": "https://downloads.paintedwolf.dev",
                   "source_commit": source_commit, "source_tree": source["source_tree"],
                   "controller_commit": producer["controller_commit"], "signing_generation": signing_generation,
                   "package_content_types": content_types, "repositories": [], "aur": {"enabled": False},
                   "nix": {"source": {"git_url": "github:paintedwolf-ai/paintedwolf-code/" + source_commit,
                                      "commit": source_commit, "flake_lock_sha256": source["flake_lock_sha256"]},
                           "cache": {"enabled": False}} if linux else None}
        if type(enabled_delivery) is not bool or enabled_delivery and not linux:
            raise ValueError("enabled Linux delivery requires explicit public source catalog policy")
        if enabled_delivery:
            from .delivery import ObservedDeliveryReleaseAuthority
            authority = ObservedDeliveryReleaseAuthority(selected, released, producer["controller_commit"])
            request["repositories"], request["nix"]["cache"] = authority.facts()
        else:
            authority = InactiveDeliveryReleaseAuthority(released, producer["controller_commit"])
        identity = prepare(selected, destination / "document", request, released.root / "catalog.json",
                           released.root / "updater.json", destination / "packages", authorize_release=authority)
        with (destination / "preparation.json").open("x") as stream:
            json.dump({**identity, "published": False, "linux_public": linux,
                       "repository_availability": bool(request["repositories"]),
                       "cache_availability": bool(request["nix"] and request["nix"]["cache"]["enabled"]),
                       "aur_availability": False}, stream, indent=2)
            stream.write("\n")
        return identity
    except BaseException:
        shutil.rmtree(destination)
        raise


def main(arguments=None):
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("source-repository", "output"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--source-commit", required=True)
    for name in ("campaign-run-id", "release-run-id", "release-artifact-id"):
        parser.add_argument("--" + name, type=int, required=True)
    parser.add_argument("--observe-enabled-delivery", action="store_true")
    args = parser.parse_args(arguments)
    try:
        producer = controller_context(os.environ)
        from scripts.ci_policy.github import api
        from update_keys import load_registry
        from ..openpgp import selection
        from .generations import CATALOG as signing_catalog
        from .authority import repository_origin, PREFIX
        from ..support_matrix import CATALOG
        signing, _ = selection(signing_catalog, False)
        catalog, _ = document(CATALOG)
        produce(api, os.environ["GITHUB_REPOSITORY"], producer, args.source_repository, args.source_commit,
                args.campaign_run_id, args.release_run_id, args.release_artifact_id, args.output,
                catalog=catalog, signing_generation=load_registry()["signing_generation"],
                package_signing_generation=signing["generation"], key_fingerprint=signing["fingerprint"],
                repository_base_url=repository_origin(os.environ["LINUX_REPOSITORY_ORIGIN"]) + "/" + PREFIX,
                enabled_delivery=args.observe_enabled_delivery)
        print(f"Nonpublishing distribution document prepared: {args.output}")
    except (OSError, ValueError, KeyError, TypeError, ImportError) as error:
        parser.exit(1, "Distribution document preparation refused: " + str(error) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
