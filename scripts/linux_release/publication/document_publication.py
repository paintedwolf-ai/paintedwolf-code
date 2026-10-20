"""Re-admit signed distribution metadata before immutable publication and notification."""
from __future__ import annotations

import hashlib
from pathlib import Path
import re

from scripts.release_distribution_contract.document import construct, snapshot
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
    _, raw = snapshot(document_path, retain=True, limit=2 << 20)
    document = decode(raw)
    fresh = construct(request, catalog, updater, artifacts, authorize=authorize)
    if canonical(fresh) != raw:
        raise ValueError("distribution document differs from freshly admitted source and delivery")
    _, signature_raw = snapshot(signature_path, retain=True, limit=65536)
    signature = signature_raw.decode()
    number = document["signing_generation"]
    check(signature, file=document_path.name, version=document["version"], number=number, registry=protected)
    verify(document_path, signature, generation(protected, number)["feed_public_key"])
    if snapshot(document_path, retain=True, limit=2 << 20)[1] != raw or snapshot(signature_path, retain=True, limit=65536)[1] != signature_raw:
        raise ValueError("distribution signature or document changed during admission")
    return document, {"distribution_sha256": hashlib.sha256(raw).hexdigest(),
                      "signature_sha256": hashlib.sha256(signature.encode()).hexdigest(),
                      "distribution_bytes": len(raw), "signature_bytes": len(signature.encode())}


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
        role = "distribution" if name == "distribution.v1.json" else "signature"
        expected = {"sha256": admitted[role + "_sha256"], "bytes": admitted[role + "_bytes"]}
        key = "releases/v" + document["version"] + "/" + name
        backend.put(key, path, content_type, expected_identity=expected)
        backend.verify(key, path, expected)
        current, checked = admitted_bundle(directory, request, catalog, updater, artifacts,
                                          authorize=authorize, registry=registry)
        if current != document or checked != admitted:
            raise ValueError("distribution admission changed during immutable publication")
    return admitted


def notification(directory, request, catalog, updater, artifacts, *, authorize, manifest_sha256,
                 registry=None, bad_distribution_sha256=None, bad_version=None, backend=None):
    """Construct a payload only; the protected workflow separately owns any dispatch."""
    if not isinstance(manifest_sha256, str) or not re.fullmatch("[a-f0-9]{64}", manifest_sha256):
        raise ValueError("notification requires an independently selected updater digest")
    document, admitted = admitted_bundle(directory, request, catalog, updater, artifacts,
                                         authorize=authorize, registry=registry)
    if document["updater_manifest_sha256"] != manifest_sha256:
        raise ValueError("notification updater digest differs from admitted distribution metadata")
    if backend is None:
        raise ValueError("notification requires fresh immutable public byte verification")
    for name, role in (("distribution.v1.json", "distribution"), ("distribution.v1.json.sig", "signature")):
        backend.verify("releases/v" + document["version"] + "/" + name, directory / name,
                       {"sha256": admitted[role + "_sha256"], "bytes": admitted[role + "_bytes"]})
    current, checked = admitted_bundle(directory, request, catalog, updater, artifacts,
                                      authorize=authorize, registry=registry)
    if current != document or checked != admitted:
        raise ValueError("notification identity changed during immutable public verification")
    payload = {"version": document["version"], "tag": "v" + document["version"],
               "channel": document["channel"], "manifest_sha256": manifest_sha256,
               "distribution_sha256": admitted["distribution_sha256"]}
    if bad_distribution_sha256 is not None:
        if not isinstance(bad_distribution_sha256, str) or not re.fullmatch("[a-f0-9]{64}", bad_distribution_sha256):
            raise ValueError("halt notification requires an explicitly selected bad distribution digest")
        from scripts.release_semver import parse
        if not isinstance(bad_version, str):
            raise ValueError("halt notification requires the explicitly selected bad version")
        parse(bad_version)
        if bad_version == document["version"]:
            raise ValueError("halt replacement must differ from the explicitly selected bad version")
        payload = {"channel": document["channel"], "bad_version": bad_version,
                   "last_good_version": document["version"], "manifest_sha256": manifest_sha256,
                   "distribution_sha256": admitted["distribution_sha256"],
                   "bad_distribution_sha256": bad_distribution_sha256}
    elif bad_version is not None:
        raise ValueError("halt notification requires the independent bad distribution digest")
    return payload
