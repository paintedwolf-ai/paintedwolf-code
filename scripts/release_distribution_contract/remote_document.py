"""Authenticate immutable distribution documents for publication and halt confirmation."""
from __future__ import annotations

import hashlib
from pathlib import Path
import re
import tempfile
import urllib.request
from urllib.parse import urlsplit

from feed_signature import check
from feed_signing import verify
from release_semver import compare, parse
from update_keys import generation, load_registry

from .document_schema import decode, validate

from .platforms import ORIGIN


def read_public(url, maximum):
    request = urllib.request.Request(url, headers={"Cache-Control": "no-cache, no-store",
                                                 "User-Agent": "painted-wolf-release/1"})
    with urllib.request.urlopen(request, timeout=30) as response:
        if response.url != url:
            raise ValueError("release metadata redirected away from its selected URL")
        raw = response.read(maximum + 1)
    if len(raw) > maximum:
        raise ValueError("release distribution metadata exceeds its bound")
    return raw


def trust(rehearsal, registry, origin):
    if type(rehearsal) is not bool:
        raise ValueError("distribution verification requires an explicit trust mode")
    if rehearsal:
        parsed = urlsplit(origin) if isinstance(origin, str) else None
        if (parsed is None or parsed.scheme != "https" or not parsed.netloc or parsed.path
                or parsed.query or parsed.fragment or parsed.username or parsed.password
                or parsed.hostname == urlsplit(ORIGIN).hostname or registry is None):
            raise ValueError("rehearsal verification requires isolated origin and trust")
        return registry
    if registry is not None or origin != ORIGIN:
        raise ValueError("production verification requires maintained origin and trust")
    return load_registry()


def verify_metadata(raw, signature, version, manifest_sha256, *, rehearsal=False, registry=None, origin=ORIGIN):
    protected = trust(rehearsal, registry, origin)
    parse(version)
    document = validate(decode(raw))
    if (document["mode"] != ("rehearsal" if rehearsal else "production") or document["origin"] != origin
            or document["version"] != version
            or document["updater_manifest_sha256"] != manifest_sha256):
        raise ValueError("distribution differs from the independently selected release")
    with tempfile.TemporaryDirectory(prefix="release-distribution-verification-") as temporary:
        path = Path(temporary) / "distribution.v1.json"
        path.write_bytes(raw)
        number = document["signing_generation"]
        check(signature, file=path.name, version=version, number=number, registry=protected)
        verify(path, signature, generation(protected, number)["feed_public_key"])
    return document


def confirm_public(receipt, *, fetch=read_public, rehearsal=False, registry=None, origin=ORIGIN):
    trust(rehearsal, registry, origin)
    version, expected = receipt["version"], receipt["distribution_sha256"]
    if version is None:
        if expected is not None or receipt["manifest_sha256"] is not None:
            raise ValueError("withdrawn discovery cannot retain release identities")
        return None
    parse(version)
    if not isinstance(expected, str) or not re.fullmatch("[a-f0-9]{64}", expected):
        raise ValueError("website confirmation requires an independent distribution digest")
    url = origin + "/releases/v" + version + "/distribution.v1.json"
    raw, signature = fetch(url, 2 << 20), fetch(url + ".sig", 65536).decode()
    if hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError("public distribution differs from its selected digest")
    return verify_metadata(raw, signature, version, receipt["manifest_sha256"], rehearsal=rehearsal,
                           registry=registry, origin=origin)


def halt_digests(bad, good, manifest_sha256, *, read=None, legacy_versions=frozenset(),
                 rehearsal=False, registry=None, origin=ORIGIN):
    if not isinstance(bad, str) or good is not None and not isinstance(good, str):
        raise ValueError("halt requires typed release identifiers")
    bad_version = parse(bad)
    if good is not None:
        if compare(parse(good), bad_version) >= 0:
            raise ValueError("halt replacement must precede its selected bad release")
        if not isinstance(manifest_sha256, str) or not re.fullmatch("[a-f0-9]{64}", manifest_sha256):
            raise ValueError("halt replacement requires its independently selected updater digest")
    elif manifest_sha256 is not None:
        raise ValueError("halt without replacement cannot retain an updater identity")
    trust(rehearsal, registry, origin)
    if rehearsal and not callable(read):
        raise ValueError("rehearsal halt requires an explicit isolated storage transport")
    from release_distribution import read_storage_bytes
    read = read or read_storage_bytes
    def selected(version, updater_digest=None, *, legacy=False):
        prefix = "releases/v" + version + "/distribution.v1.json"
        raw, signature = read(prefix), read(prefix + ".sig")
        if raw is None and signature is None and legacy and version in legacy_versions:
            return None
        if raw is None or signature is None:
            raise ValueError("halt requires the complete immutable distribution document")
        updater = read("updates/releases/" + version + ".json")
        if updater is None:
            raise ValueError("halt distribution lacks its immutable updater identity")
        actual = hashlib.sha256(updater).hexdigest()
        if updater_digest is not None and updater_digest != actual:
            raise ValueError("halt replacement differs from its independently selected updater")
        verify_metadata(raw, signature.decode(), version, actual, rehearsal=rehearsal, registry=registry, origin=origin)
        return hashlib.sha256(raw).hexdigest()
    bad_digest = selected(bad, legacy=True)
    return {"bad_distribution_sha256": bad_digest,
            "distribution_sha256": selected(good, manifest_sha256, legacy=True) if good is not None else None}
