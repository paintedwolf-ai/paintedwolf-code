"""Publish reverified complete caches before recording a release's availability."""
from __future__ import annotations

import hashlib
import json
from contextlib import contextmanager
from pathlib import Path
import shutil
import tempfile
import uuid

from .adapter import path_info, verify
from .admission import closure_projection, inventory, validate_request


def encoded(value: dict) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()


def verified_report(cache: Path, report: dict) -> dict:
    fields = {"schema_version", "status", "request", "public_keys", "nix_version", "closure_sha256", "closure",
              "derivations", "objects", "upstream_dependencies", "dependency_policy"}
    if not isinstance(report, dict) or set(report) != fields or report["schema_version"] != 1 or report["status"] != "passed":
        raise ValueError("cache publication requires a complete construction report")
    request = report["request"]
    validate_request(request, {"schema_version": 1, "mode": request["mode"], "public_keys": report["public_keys"]})
    if report["upstream_dependencies"] != [] or report["dependency_policy"] != "complete-closure-in-this-cache":
        raise ValueError("cache publication requires its complete runtime closure")
    closure = report["closure"]
    if not isinstance(closure, dict) or not set(request["outputs"]) <= closure.keys():
        raise ValueError("cache report omits a selected output")
    for row in closure.values():
        if not isinstance(row, dict) or not set(row.get("references", [])) <= closure.keys():
            raise ValueError("cache report omits a referenced closure path")
    digest = hashlib.sha256(json.dumps(closure, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    if digest != report["closure_sha256"] or inventory(cache, closure, report["public_keys"]) != report["objects"]:
        raise ValueError("cache files or closure differ from their construction report")
    verify(cache, request["outputs"], report["public_keys"])
    actual = closure_projection(path_info(cache.as_uri(), request["outputs"]), request["outputs"])
    if set(actual) != set(closure) or any(actual[path][field] != closure[path][field]
                                          for path in actual for field in ("nar_hash", "nar_bytes", "references")):
        raise ValueError("Nix cache bytes differ from the reported closure identities")
    return report


def metadata_upgrade(previous: bytes, incoming: bytes) -> bool:
    """Rotation may add signatures while preserving every prior signature and field."""
    def project(raw):
        fields, signatures = [], set()
        for line in raw.decode("utf-8").splitlines():
            if line.startswith("Sig: "):
                signatures.add(line)
            else:
                fields.append(line)
        return fields, signatures
    old_fields, old_signatures = project(previous)
    new_fields, new_signatures = project(incoming)
    return old_fields == new_fields and bool(old_signatures) and old_signatures <= new_signatures


def record_name(request: dict) -> str:
    return f"release-records/{request['version']}/{request['system']}.json"


@contextmanager
def staged_cache(cache: Path, report: dict):
    with tempfile.TemporaryDirectory(prefix="paintedwolf-cache-publication-") as directory:
        snapshot = Path(directory)
        for record in report["objects"]:
            destination = snapshot / record["name"]
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(cache / record["name"], destination)
        if inventory(snapshot, report["closure"], report["public_keys"]) != report["objects"]:
            raise ValueError("cache changed while capturing private publication bytes")
        yield snapshot


def publish(cache: Path, report: dict, *, authorize, backend_factory, operation: str | None = None) -> dict:
    checked = verified_report(cache, report)
    with staged_cache(cache, checked) as snapshot:
        if authorize(checked["request"], checked) is not True:
            raise ValueError("cache controller did not authorize this exact release")
        # External I/O uses private captured bytes after fresh controller admission.
        return publish_objects(snapshot, checked, backend_factory(checked["request"]), operation)


def publish_objects(cache: Path, checked: dict, backend, operation: str | None) -> dict:
    request = checked["request"]
    operation = operation or uuid.uuid4().hex
    with backend.serialized(operation):
        name = record_name(request)
        if backend.read(name + ".withdrawn") is not None:
            raise ValueError("withdrawn cache release cannot be published again")
        receipt = {"schema_version": 1, "request": request, "closure_sha256": checked["closure_sha256"],
                   "closure": checked["closure"], "objects": checked["objects"], "public_keys": checked["public_keys"]}
        previous = backend.read(name)
        if previous is not None and previous != encoded(receipt):
            old = json.loads(previous)
            stable = lambda rows: [row for row in rows if row["phase"] != "narinfo"]
            if (old["request"] != request or old["closure"] != receipt["closure"]
                    or stable(old["objects"]) != stable(receipt["objects"])):
                raise ValueError("cache release identity changed instead of adding rotation signatures")
            name = name.removesuffix(".json") + "-rotation-" + hashlib.sha256(encoded(receipt)).hexdigest() + ".json"
        for record in checked["objects"]:
            relative = record["name"]
            previous = backend.read(relative) if record["phase"] != "nar" else None
            if previous is not None and previous != (cache / relative).read_bytes():
                if record["phase"] != "narinfo" or not metadata_upgrade(previous, (cache / relative).read_bytes()):
                    raise ValueError("cache metadata would change retained contents or remove signatures")
                backend.replace(relative, cache / relative, record, previous)
            else:
                backend.put(relative, cache / relative, record)
            backend.verify_public(relative, record)
        # A stale/negative public response leaves no new availability receipt. Retries
        # reuse identical objects and only this record advertises the complete closure.
        backend.put_bytes(name, encoded(receipt), "application/json", "no-cache, no-store, must-revalidate")
    return receipt


def withdraw(request: dict, *, authorize, backend_factory) -> None:
    if authorize(request) is not True:
        raise ValueError("cache withdrawal requires explicit controller authority")
    with backend_factory(request).serialized(uuid.uuid4().hex) as backend:
        name = record_name(request)
        if backend.read(name) is None:
            raise ValueError("cache withdrawal requires an existing published release")
        backend.put_bytes(name + ".withdrawn", encoded({"schema_version": 1, "request": request, "withdrawn": True}),
                          "application/json", "no-cache, no-store, must-revalidate")
