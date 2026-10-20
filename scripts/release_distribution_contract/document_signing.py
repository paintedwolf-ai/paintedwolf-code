"""Sign a fresh immutable distribution bundle without activating public delivery."""
from __future__ import annotations

import hashlib
from pathlib import Path

from feed_signature import check
from feed_signing import sign, verify
from update_keys import generation, load_registry

from .document import REQUEST, construct
from .document_schema import canonical, exact, require


def prepare(destination: Path, request, catalog: Path, updater_manifest: Path, artifacts: Path, *, authorize,
            registry=None, signer=None):
    require(destination.is_absolute() and not destination.exists() and not destination.is_symlink()
            and destination.parent.is_dir(), "distribution bundle requires a fresh absolute directory")
    exact(request, REQUEST)
    protected = load_registry() if registry is None else registry
    require(request.get("mode") == "rehearsal" or protected == load_registry(),
            "production distribution cannot replace its maintained signing registry")
    document = construct(request, catalog, updater_manifest, artifacts, authorize=authorize)
    number = document["signing_generation"]
    public = generation(protected, number)["feed_public_key"]
    destination.mkdir(mode=0o700)
    path = destination / "distribution.v1.json"
    raw = canonical(document)
    with path.open("xb") as stream:
        stream.write(raw)
    signature = (sign(path, number, protected).read_text() if signer is None
                 else signer(path, number, protected))
    check(signature, file=path.name, version=document["version"], number=number, registry=protected)
    verify(path, signature, public)
    # Re-read selected artifacts and freshly call the admission owner after signing.
    current = construct(request, catalog, updater_manifest, artifacts, authorize=authorize)
    require(canonical(current) == raw, "distribution facts changed while signing")
    signature_path = destination / "distribution.v1.json.sig"
    if signer is None:
        require(signature_path.read_text() == signature, "distribution signature changed after verification")
    else:
        with signature_path.open("x") as stream:
            stream.write(signature)
    identity = {"schema_version": 1, "version": document["version"], "publication": "nonrelease",
                "distribution_sha256": hashlib.sha256(raw).hexdigest(),
                "signature_sha256": hashlib.sha256(signature.encode()).hexdigest(),
                "source_commit": document["source"]["commit"], "controller_commit": document["controller_commit"],
                "signing_generation": number}
    with (destination / "distribution-identity.json").open("xb") as stream:
        stream.write(canonical(identity))
    return identity
