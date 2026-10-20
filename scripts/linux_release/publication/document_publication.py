"""Re-admit signed distribution metadata before immutable publication and notification."""
from __future__ import annotations

import hashlib
from pathlib import Path
import re

from scripts.release_distribution_contract.document import construct
from scripts.release_distribution_contract.document_schema import canonical, decode
from scripts.feed_signature import check
from scripts.feed_signing import verify
from scripts.update_keys import generation, load_registry
from .immutable import identity


def admitted_bundle(directory, request, catalog, updater, artifacts, *, authorize, registry=None):
    """A saved preparation receipt never grants publication or notification authority."""
    protected = load_registry() if registry is None else registry
    if request["mode"] != "rehearsal" and protected != load_registry():
        raise ValueError("production document publication requires maintained feed trust")
    document_path = directory / "distribution.v1.json"
    signature_path = directory / "distribution.v1.json.sig"
    raw = document_path.read_bytes()
    document = decode(raw)
    fresh = construct(request, catalog, updater, artifacts, authorize=authorize)
    if canonical(fresh) != raw:
        raise ValueError("distribution document differs from freshly admitted source and delivery")
    signature = signature_path.read_text()
    number = document["signing_generation"]
    check(signature, file=document_path.name, version=document["version"], number=number, registry=protected)
    verify(document_path, signature, generation(protected, number)["feed_public_key"])
    if document_path.read_bytes() != raw or signature_path.read_text() != signature:
        raise ValueError("distribution signature or document changed during admission")
    return document, {"distribution_sha256": hashlib.sha256(raw).hexdigest(),
                      "signature_sha256": hashlib.sha256(signature.encode()).hexdigest()}


def publish(directory, request, catalog, updater, artifacts, *, authorize, backend, registry=None):
    document, admitted = admitted_bundle(directory, request, catalog, updater, artifacts,
                                         authorize=authorize, registry=registry)
    for name, content_type in (("distribution.v1.json", "application/json"),
                               ("distribution.v1.json.sig", "text/plain")):
        current, checked = admitted_bundle(directory, request, catalog, updater, artifacts,
                                          authorize=authorize, registry=registry)
        if current != document or checked != admitted:
            raise ValueError("distribution admission changed before immutable publication")
        path = directory / name
        expected = identity(path)
        key = "releases/v" + document["version"] + "/" + name
        backend.put(key, path, content_type)
        backend.verify(key, path, expected)
        current, checked = admitted_bundle(directory, request, catalog, updater, artifacts,
                                          authorize=authorize, registry=registry)
        if current != document or checked != admitted:
            raise ValueError("distribution admission changed during immutable publication")
    return admitted


def notification(directory, request, catalog, updater, artifacts, *, authorize, manifest_sha256,
                 registry=None, bad_distribution_sha256=None):
    """Construct a payload only; the protected workflow separately owns any dispatch."""
    if not isinstance(manifest_sha256, str) or not re.fullmatch("[a-f0-9]{64}", manifest_sha256):
        raise ValueError("notification requires an independently selected updater digest")
    document, admitted = admitted_bundle(directory, request, catalog, updater, artifacts,
                                         authorize=authorize, registry=registry)
    if document["updater_manifest_sha256"] != manifest_sha256:
        raise ValueError("notification updater digest differs from admitted distribution metadata")
    payload = {"version": document["version"], "tag": "v" + document["version"],
               "channel": document["channel"], "manifest_sha256": manifest_sha256,
               "distribution_sha256": admitted["distribution_sha256"]}
    if bad_distribution_sha256 is not None:
        if not isinstance(bad_distribution_sha256, str) or not re.fullmatch("[a-f0-9]{64}", bad_distribution_sha256):
            raise ValueError("halt notification requires an explicitly selected bad distribution digest")
        payload["bad_distribution_sha256"] = bad_distribution_sha256
    return payload
