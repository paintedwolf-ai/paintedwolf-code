"""Publish reverified complete caches before recording a release's availability."""
from __future__ import annotations

import hashlib
import json
from contextlib import contextmanager
from pathlib import Path, PurePosixPath
import re
import shutil
import tempfile
import uuid

from .adapter import path_info, verify
from .admission import closure_projection, inventory, validate_request, nar_sha256


def encoded(value: dict) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()


def verified_report(cache: Path, report: dict) -> dict:
    fields = {"schema_version", "status", "request", "public_keys", "nix_version", "closure_sha256", "closure",
              "derivations", "objects", "upstream_dependencies", "dependency_policy"}
    if (not isinstance(report, dict) or set(report) != fields or type(report["schema_version"]) is not int
            or report["schema_version"] != 1 or report["status"] != "passed"):
        raise ValueError("cache publication requires a complete construction report")
    receipt = availability(report)
    request, closure = receipt['request'], receipt['closure']
    if report["upstream_dependencies"] != [] or report["dependency_policy"] != "complete-closure-in-this-cache":
        raise ValueError("cache publication requires its complete runtime closure")
    if inventory(cache, closure, report["public_keys"]) != report["objects"]:
        raise ValueError("cache files differ from their construction report")
    verify(cache, request["outputs"], report["public_keys"])
    actual = closure_projection(path_info(cache.as_uri(), request["outputs"]), request["outputs"])
    if set(actual) != set(closure) or any(actual[path][field] != closure[path][field]
                                          for path in actual for field in ("nar_hash", "nar_bytes", "references")):
        raise ValueError("Nix cache bytes differ from the reported closure identities")
    return report


def availability(report):
    record = {key: report[key] for key in ('request', 'closure_sha256', 'closure', 'objects', 'public_keys')}
    return validate_availability({'schema_version': 1, **record})


def validate_availability(record, policy=None):
    fields = {'schema_version', 'request', 'closure_sha256', 'closure', 'objects', 'public_keys'}
    if (not isinstance(record, dict) or set(record) != fields or type(record['schema_version']) is not int
            or record['schema_version'] != 1):
        raise ValueError('cache availability has an unsupported versioned shape')
    request = record['request']
    if not isinstance(request, dict):
        raise ValueError('cache availability requires its typed request')
    selected = {'schema_version': 1, 'mode': request.get('mode'), 'public_keys': record['public_keys']}
    validate_request(request, selected)
    if policy is not None and selected != policy:
        raise ValueError('cache availability differs from selected trust')
    closure = record['closure']
    from .admission import STORE_PATH
    if (not isinstance(closure, dict) or not set(request['outputs']) <= closure.keys()
            or any(not isinstance(path, str) or not re.fullmatch(STORE_PATH, path) for path in closure)):
        raise ValueError('cache availability omits its selected closure')
    for row in closure.values():
        if (not isinstance(row, dict) or set(row) != {'nar_hash', 'nar_bytes', 'references', 'derivation'}
                or not isinstance(row['nar_hash'], str) or not row['nar_hash']
                or type(row['nar_bytes']) is not int or row['nar_bytes'] <= 0
                or not isinstance(row['references'], list) or any(not isinstance(path, str) for path in row['references'])
                or len(set(row['references'])) != len(row['references']) or not set(row['references']) <= closure.keys()
                or row['derivation'] is not None and (not isinstance(row['derivation'], str)
                    or not re.fullmatch(STORE_PATH, row['derivation']))):
            raise ValueError('cache availability has inconsistent closure metadata')
    for row in closure.values():
        nar_sha256(row['nar_hash'])
    if hashlib.sha256(json.dumps(closure, sort_keys=True, separators=(',', ':')).encode()).hexdigest() != record['closure_sha256']:
        raise ValueError('cache availability differs from its closure identity')
    objects, seen = record['objects'], set()
    if not isinstance(objects, list) or not objects:
        raise ValueError('cache availability requires its declared object inventory')
    for row in objects:
        if not isinstance(row, dict) or set(row) != {'name', 'phase', 'bytes', 'sha256', 'content_type', 'cache_control'}:
            raise ValueError('cache availability has an unsupported object shape')
        name = row['name']
        if (not isinstance(name, str) or not name or PurePosixPath(name).is_absolute() or '..' in PurePosixPath(name).parts
                or PurePosixPath(name).as_posix() != name or name in seen
                or row['phase'] not in {'nar', 'narinfo', 'cache-info'} or type(row['bytes']) is not int or row['bytes'] <= 0
                or not isinstance(row['sha256'], str) or not re.fullmatch('[a-f0-9]{64}', row['sha256'])
                or row['content_type'] != ('application/octet-stream' if row['phase'] == 'nar' else 'text/plain; charset=utf-8')
                or row['cache_control'] != ('public, max-age=31536000, immutable' if row['phase'] == 'nar' else 'no-cache, no-store, must-revalidate')):
            raise ValueError('cache availability has inconsistent object metadata')
        seen.add(name)
    expected_metadata = {Path(path).name.split('-', 1)[0] + '.narinfo' for path in closure} | {'nix-cache-info'}
    if ({row['name'] for row in objects if row['phase'] != 'nar'} != expected_metadata
            or not any(row['phase'] == 'nar' for row in objects)
            or any((row['phase'] == 'nar' and not row['name'].startswith('nar/'))
                   or (row['phase'] == 'cache-info' and row['name'] != 'nix-cache-info')
                   or (row['phase'] == 'narinfo' and not row['name'].endswith('.narinfo')) for row in objects)):
        raise ValueError('cache availability inventory omits its declared closure metadata')
    return record


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
        return _publish_objects(snapshot, checked, backend_factory(checked["request"]), operation)


def _publish_objects(cache: Path, checked: dict, backend, operation: str | None) -> dict:
    request = checked["request"]
    operation = operation or uuid.uuid4().hex
    with backend.serialized(operation):
        name = record_name(request)
        if backend.read(name + ".withdrawn") is not None:
            raise ValueError("withdrawn cache release cannot be published again")
        receipt = availability(checked)
        previous = backend.read(name)
        if previous is not None and previous != encoded(receipt):
            old = json.loads(previous)
            stable = lambda rows: [row for row in rows if row["phase"] != "narinfo"]
            if (old["request"] != request or old["closure"] != receipt["closure"]
                    or stable(old["objects"]) != stable(receipt["objects"])):
                raise ValueError("cache release identity changed instead of adding rotation signatures")
            name = name.removesuffix(".json") + "-rotation-" + hashlib.sha256(encoded(receipt)).hexdigest() + ".json"
        # A lost connection does not cancel an in-flight object write. Keep its
        # full plan as a retention root before exposing any object from that plan.
        backend.put_bytes(f"publication-plans/{operation}.json",
                          encoded({"schema_version": 1, "request": request, "objects": checked["objects"]}),
                          "application/json", "no-cache, no-store, must-revalidate")
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
        raw = backend.read(name)
        if raw is None or validate_availability(json.loads(raw))["request"] != request:
            raise ValueError("cache withdrawal requires its exactly selected published release")
        backend.put_bytes(name + ".withdrawn", encoded({"schema_version": 1, "request": request, "withdrawn": True}),
                          "application/json", "no-cache, no-store, must-revalidate")
