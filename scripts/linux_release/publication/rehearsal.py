"""Rehearse signed document publication under one disposable bucket-scoped prefix."""
from __future__ import annotations
import argparse
import base64
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import uuid

from scripts.release_distribution_contract.document_signing import prepare
from scripts.release_storage.s3 import request
from ..r2_objects import rehearsal_environment
from .document_publication import notification, publish
from .immutable import ImmutableOrigin


def objects(prefix):
    token, seen, result = None, set(), []
    while True:
        arguments = ["--prefix", prefix + "/", "--max-keys", "1000", "--no-paginate"]
        if token:
            arguments += ["--continuation-token", token]
        page = request("list-objects-v2", *arguments)
        for row in page.get("Contents", []):
            key = row["Key"]
            if not key.startswith(prefix + "/") or key in result:
                raise ValueError("document rehearsal listing escaped its exact disposable prefix")
            result.append(key)
        if not page.get("IsTruncated", False):
            return result
        token = page.get("NextContinuationToken")
        if not isinstance(token, str) or not token or token in seen:
            raise ValueError("document rehearsal listing has invalid pagination")
        seen.add(token)


def run(output):
    origin = rehearsal_environment()
    prefix = "release-system-tests/distribution-document-" + uuid.uuid4().hex
    with output.open("x") as stream:
        json.dump({"schema_version": 1, "status": "started", "prefix": prefix,
                   "rehearsal": True, "application_qualified": False, "production_published": False}, stream)
        stream.write("\n")
    output.chmod(0o600)
    backend = ImmutableOrigin(rehearsal=True, storage_prefix=prefix, origin=origin)
    if objects(prefix):
        raise ValueError("fresh document rehearsal prefix already contains objects")
    controls = []
    deleted = []
    try:
        with tempfile.TemporaryDirectory(prefix="distribution-document-fixture-") as temporary:
            root = Path(temporary)
            public, secret = root / "fixture.pub", root / "fixture.key"
            subprocess.run(["minisign", "-G", "-W", "-p", str(public), "-s", str(secret)], check=True, capture_output=True)
            secret.chmod(0o600)
            key = base64.b64encode(public.read_bytes()).decode()
            registry = {"schema_version": 1, "signing_generation": 1, "embedded_generation": 1,
                        "generations": [{"generation": 1, "public_key": key, "feed_public_key": key,
                                         "successor": None, "bridge_version": None}]}
            catalog, updater, artifacts = root / "catalog.json", root / "updater.json", root / "packages"
            artifacts.mkdir()
            catalog.write_text(json.dumps({"schema_version": 2, "platforms": [{"updater_key": "linux-x86_64",
                "goos": "linux", "artifact_arch": "x86_64", "publication": "public",
                "package_extensions": ["deb", "rpm", "pkg.tar.zst"], "updater_extension": None}]}))
            updater.write_text(json.dumps({"version": "0.0.900001", "notes": "Isolated nonrelease document fixture",
                                          "pub_date": "2026-10-20T00:00:00Z", "platforms": {}}))
            types = {}
            for extension in ("deb", "rpm", "pkg.tar.zst"):
                name = "painted-wolf-code_v0.0.900001_linux-x86_64." + extension
                (artifacts / name).write_bytes(("not an application: " + extension).encode())
                types[name] = "application/octet-stream"
            selected = {"mode": "rehearsal", "version": "0.0.900001", "origin": origin,
                "source_commit": "a" * 40, "source_tree": "b" * 40, "controller_commit": "c" * 40,
                "signing_generation": 1, "package_content_types": types, "repositories": [], "nix": None,
                "aur": {"enabled": False}}
            calls = []
            def authorize(binding):
                if binding["request"] != selected:
                    raise ValueError("document fixture changed its declared nonpublishing scope")
                calls.append(1)
                return True
            def signer(path, number, policy):
                target = root / "document.minisig"
                subprocess.run(["minisign", "-S", "-s", str(secret), "-m", str(path), "-x", str(target),
                    "-t", "timestamp:1792454400\tfile:distribution.v1.json\tversion:0.0.900001"],
                    check=True, capture_output=True)
                return base64.b64encode(target.read_bytes()).decode()
            bundle = root / "document"
            prepare(bundle, selected, catalog, updater, artifacts, authorize=authorize, registry=registry, signer=signer)
            controls.append("real-minisign-pre-and-post-sign-admission")
            arguments = (bundle, selected, catalog, updater, artifacts)
            identity = publish(*arguments, authorize=authorize, backend=backend, registry=registry)
            controls.append("immutable-document-and-signature-public-byte-verification")
            publish(*arguments, authorize=authorize, backend=backend, registry=registry)
            controls.append("identical-immutable-replay")
            altered = root / "altered.json"; altered.write_bytes(b"changed immutable document")
            try:
                backend.put("releases/v0.0.900001/distribution.v1.json", altered, "application/json")
            except subprocess.CalledProcessError:
                controls.append("different-immutable-bytes-refused")
            else:
                raise ValueError("document rehearsal overwrote immutable bytes")
            signature = bundle / "distribution.v1.json.sig"
            original = signature.read_bytes()
            decoded = base64.b64decode(original).decode().splitlines()
            value = bytearray(base64.b64decode(decoded[1])); value[-1] ^= 1
            decoded[1] = base64.b64encode(value).decode()
            signature.write_bytes(base64.b64encode(("\n".join(decoded) + "\n").encode()))
            try:
                publish(*arguments, authorize=authorize, backend=backend, registry=registry)
            except ValueError:
                controls.append("tampered-real-cryptographic-signature-refused")
            else:
                raise ValueError("document rehearsal accepted a corrupted signature")
            signature.write_bytes(original)
            try:
                publish(*arguments, authorize=lambda binding: False, backend=backend, registry=registry)
            except ValueError:
                controls.append("stale-fresh-admission-refused")
            else:
                raise ValueError("document rehearsal accepted stale authority")
            payload = notification(*arguments, authorize=authorize, registry=registry,
                manifest_sha256=hashlib.sha256(updater.read_bytes()).hexdigest(), bad_distribution_sha256="d" * 64)
            if payload["distribution_sha256"] != identity["distribution_sha256"] or payload["bad_distribution_sha256"] != "d" * 64:
                raise ValueError("document notification lost independent selected digests")
            controls.append("release-and-halt-independent-digest-payloads")
            if len(objects(prefix)) != 2:
                raise ValueError("document rehearsal retained unexpected object inventory")
    finally:
        for key in objects(prefix):
            request("delete-object", "--key", key); deleted.append(key)
        if objects(prefix):
            raise ValueError("document rehearsal cleanup audit is not empty")
    controls.append("paginated-exact-prefix-cleanup-and-fresh-empty-audit")
    result = {"schema_version": 1, "status": "passed", "rehearsal": True, "application_qualified": False,
              "production_published": False, "prefix": prefix, "controls": controls,
              "objects_deleted": len(deleted), "remaining_objects": 0}
    with output.open("w") as stream:
        json.dump(result, stream, indent=2); stream.write("\n")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if not args.output.is_absolute() or args.output.exists() or not args.output.parent.is_dir():
        parser.error("output must be a fresh absolute path in an existing private directory")
    result = run(args.output)
    print("Document publication rehearsal passed " + str(len(result["controls"])) + " controls; cleanup empty")

if __name__ == "__main__":
    main()
