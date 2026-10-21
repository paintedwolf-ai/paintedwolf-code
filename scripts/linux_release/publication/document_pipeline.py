"""Prepare signed immutable distribution metadata without activating public delivery."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil

from ..installed_provenance import controller_context
from .controller import retain as retain_campaign
from .distribution import InactiveDeliveryReleaseAuthority, prepare
from .release_inputs import retain as retain_release
from ..release_admission import document
from ..release_source import facts

TYPES = {"dmg": "application/x-apple-diskimage", "exe": "application/vnd.microsoft.portable-executable",
         "deb": "application/vnd.debian.binary-package", "rpm": "application/x-rpm", "pkg.tar.zst": "application/zstd"}
from .native_artifacts import ARCHES, collect as collect_native


def package_files(selected, released, source, destination):
    """Canonical public names cannot change installed or independently signed bytes."""
    from scripts.release_distribution_contract.document import snapshot
    from scripts.release_distribution_contract.platforms import load_platforms
    catalog = load_platforms(released.root / "catalog.json")
    collect_native(selected, catalog, destination)
    content_types = {}
    for platform, row in catalog.items():
        if row["publication"] != "public":
            continue
        for extension in row["package_extensions"]:
            name = f"painted-wolf-code_v{source['version']}_{platform}.{extension}"
            if extension not in TYPES:
                raise ValueError("public package has no maintained content type")
            if platform not in ARCHES.values():
                origin = released.root / "artifacts" / name
                expected = snapshot(origin)[0]
                shutil.copyfile(origin, destination / name)
                (destination / name).chmod(0o600)
                if snapshot(destination / name)[0] != expected:
                    raise ValueError("publication package changed while assembling document inputs")
            content_types[name] = TYPES[extension]
    for sidecar in destination.glob("*.sha256"):
        sidecar.unlink()
    return content_types, catalog


def produce(api, repository, producer, source_repository, source_commit, campaign_run_id,
            release_run_id, release_artifact_id, destination, *, catalog, signing_generation, package_signing_generation, key_fingerprint, repository_base_url, enabled_delivery=False, publish_immutable=False, delivery_expected=None,
            current_release=False, document_artifact_id=None, consumer=None):
    if type(enabled_delivery) is not bool or type(publish_immutable) is not bool:
        raise ValueError("document delivery operations require explicit booleans")
    if type(current_release) is not bool:
        raise ValueError("release producer handoff requires an explicit operation")
    notification_only = document_artifact_id is not None
    if notification_only and (not current_release or publish_immutable or not enabled_delivery):
        raise ValueError("published document notification requires a fixed read-only same-run handoff")
    if delivery_expected is not None and not (enabled_delivery and publish_immutable):
        raise ValueError("delivery activation requires explicit observed publication mode")
    if (destination.exists() or destination.is_symlink()
            or destination.resolve().is_relative_to(source_repository.resolve())):
        raise ValueError("distribution preparation requires fresh independent retained inputs")
    source = facts(source_repository, source_commit)
    destination.mkdir(mode=0o700)
    publication_started = False
    try:
        selected = retain_campaign(api, repository, producer["repository_id"], campaign_run_id, source_repository,
            source_commit, destination / "linux", catalog=catalog, signing_generation=package_signing_generation,
            key_fingerprint=key_fingerprint, repository_base_url=repository_base_url)
        if current_release:
            from .release_handoff import retain_current
            if release_run_id != producer["run_id"]:
                raise ValueError("current producer handoff cannot select another workflow run")
            released, handoff = retain_current(api, repository, producer, source_repository, source_commit,
                destination / "release", release_artifact_id, consumer=consumer)
            with (destination / "producer-selection.json").open("x") as stream:
                json.dump(handoff, stream, sort_keys=True); stream.write("\n")
        else:
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
            if delivery_expected is not None:
                from .delivery_publication import publish as publish_delivery
                from .authority import repository_origin
                publication_started = True
                publish_delivery(selected, origin=repository_origin(request["origin"]), expected=delivery_expected)
            from .delivery import ObservedDeliveryReleaseAuthority
            authority = ObservedDeliveryReleaseAuthority(selected, released, producer["controller_commit"])
            request["repositories"], request["nix"]["cache"] = authority.facts()
        else:
            authority = InactiveDeliveryReleaseAuthority(released, producer["controller_commit"])
        from .distribution import DistributionAuthority
        callback = DistributionAuthority(selected, authorize_release=authority)
        if notification_only:
            from .release_handoff import retain_document
            from .document_publication import admitted_bundle
            retain_document(api, repository, producer, source, destination / "document", document_artifact_id)
            signed, checked = admitted_bundle(destination / "document", request, released.root / "catalog.json",
                released.root / "updater.json", destination / "packages", authorize=callback)
            identity = {"schema_version": 1, "version": signed["version"], **checked,
                        "source_commit": source_commit, "controller_commit": producer["controller_commit"],
                        "signing_generation": signing_generation}
        else:
            identity = prepare(selected, destination / "document", request, released.root / "catalog.json",
                               released.root / "updater.json", destination / "packages", authorize_release=authority)
            with (destination / "document" / "producer.json").open("x") as stream:
                json.dump({"format_version": 1, **producer, "source_commit": source_commit,
                           "source_tree": source["source_tree"]}, stream, sort_keys=True)
                stream.write("\n")
        if type(publish_immutable) is not bool:
            raise ValueError("document publication requires an explicit boolean operation")
        if publish_immutable:
            from .document_publication import publish
            from .immutable import ImmutableOrigin
            publication_started = True
            immutable_backend = ImmutableOrigin()
            publish(destination / "document", request, released.root / "catalog.json",
                    released.root / "updater.json", destination / "packages", authorize=callback,
                    backend=immutable_backend)
        if publish_immutable or notification_only:
            from .document_publication import notification
            from .immutable import ImmutableOrigin
            immutable_backend = ImmutableOrigin()
            updater_sha256 = hashlib.sha256((released.root / "updater.json").read_bytes()).hexdigest()
            payload = notification(destination / "document", request, released.root / "catalog.json",
                    released.root / "updater.json", destination / "packages", authorize=callback,
                    manifest_sha256=updater_sha256, backend=immutable_backend)
            with (destination / "notification.json").open("x") as stream:
                json.dump(payload, stream, sort_keys=True); stream.write("\n")
        with (destination / "preparation.json").open("x") as stream:
            json.dump({**identity, "published": publish_immutable, "linux_public": linux,
                       "repository_availability": bool(request["repositories"]),
                       "cache_availability": bool(request["nix"] and request["nix"]["cache"]["enabled"]),
                       "aur_availability": False}, stream, indent=2)
            stream.write("\n")
        return identity
    except BaseException:
        if publication_started:
            with (destination / "publication-failure.json").open("x") as stream:
                json.dump({"schema_version": 1, "status": "incomplete", "immutable_objects_may_exist": True,
                           "mutable_activation_may_have_occurred": delivery_expected is not None, "notification_dispatched": False}, stream)
                stream.write("\n")
        else:
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
    parser.add_argument("--publish-immutable", action="store_true")
    parser.add_argument("--delivery-policy", type=Path)
    parser.add_argument("--current-release-producer", action="store_true")
    parser.add_argument("--current-document-artifact-id", type=int)
    args = parser.parse_args(arguments)
    try:
        producer = controller_context(os.environ)
        from scripts.ci_policy.github import api
        from scripts.update_keys import load_registry
        from ..openpgp import selection
        from .generations import CATALOG as signing_catalog
        from .authority import repository_origin, PREFIX
        from ..support_matrix import CATALOG
        signing, _ = selection(signing_catalog, False)
        catalog, _ = document(CATALOG)
        from .delivery_publication import load_policy
        expected = load_policy(args.delivery_policy) if args.delivery_policy is not None else None
        produce(api, os.environ["GITHUB_REPOSITORY"], producer, args.source_repository, args.source_commit,
                args.campaign_run_id, args.release_run_id, args.release_artifact_id, args.output,
                catalog=catalog, signing_generation=load_registry()["signing_generation"],
                package_signing_generation=signing["generation"], key_fingerprint=signing["fingerprint"],
                repository_base_url=repository_origin(os.environ["LINUX_REPOSITORY_ORIGIN"]) + "/" + PREFIX,
                enabled_delivery=args.observe_enabled_delivery, publish_immutable=args.publish_immutable, delivery_expected=expected,
                current_release=args.current_release_producer, document_artifact_id=args.current_document_artifact_id,
                consumer=os.environ.get("GITHUB_JOB"))
        print(f"Distribution document processing completed: {args.output}")
    except (OSError, ValueError, KeyError, TypeError, ImportError) as error:
        parser.exit(1, "Distribution document preparation refused: " + str(error) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
