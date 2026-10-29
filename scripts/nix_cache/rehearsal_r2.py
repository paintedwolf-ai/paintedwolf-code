"""Exercise signed cache delivery through an isolated real R2 prefix."""
from __future__ import annotations

import json
import os
import shutil
import urllib.error
import urllib.request
import uuid

from .adapter import nix, path_info, verify
from .admission import inventory
from .publication import publish, record_name, withdraw
from .r2 import LOCK, NoRedirect, R2Transport
from .rehearsal import must_refuse
from .rehearsal_fixture import cache_fixture, local_store


class RehearsalTransport(R2Transport):
    def __init__(self, request):
        if request["mode"] != "rehearsal":
            raise ValueError("cache live rehearsal cannot address production")
        super().__init__(request)
        self.created = set()
        self.interrupt_metadata = True

    def _write(self, name, *arguments):
        self.created.add(name)
        return super()._write(name, *arguments)

    def put(self, name, path, record):
        if record["phase"] == "narinfo" and self.interrupt_metadata:
            self.interrupt_metadata = False
            raise ValueError("disposable cache publication interrupted before metadata")
        self.created.add(name)
        return super().put(name, path, record)

    def cleanup(self):
        failures = []
        for name in sorted(self.created):
            try:
                self._aws("delete-object", "--key", self._key(name))
            except ValueError:
                failures.append(name)
        if failures or self.listing():
            raise ValueError("cache rehearsal exact-key cleanup failed")


def warm_missing(backend, name):
    request = urllib.request.Request(backend.public + "/" + name, headers={"User-Agent": "painted-wolf-release/1"})
    try:
        with urllib.request.build_opener(NoRedirect).open(request, timeout=30):
            raise ValueError("cache rehearsal prefix already has public metadata")
    except urllib.error.HTTPError as error:
        if error.code != 404:
            raise ValueError("cache rehearsal public domain did not return a missing object") from error


def cold_copy(fixture, backend, name, keys):
    target = local_store(fixture.work / name)
    nix("copy", "--from", backend.public, "--to", target, "--trusted-public-keys", " ".join(keys),
        "--option", "extra-trusted-public-keys", "", "--stdin", stdin=fixture.output + "\n")
    if set(path_info(target, [fixture.output])) != set(fixture.report["closure"]):
        raise ValueError("public cache did not substitute its complete dependent closure")


def execute(fixture, backend):
    report, request = fixture.report, fixture.report["request"]
    if backend.listing():
        raise ValueError("cache rehearsal prefix is not empty; refusing to adopt objects")
    metadata = next(row["name"] for row in report["objects"] if row["phase"] == "narinfo")
    warm_missing(backend, metadata)
    arguments = {"authorize": lambda selected, checked: selected == request, "backend_factory": lambda selected: backend}
    must_refuse(lambda: publish(fixture.cache, report, **arguments), "interrupted metadata publication")
    if backend.read(record_name(request)) is not None:
        raise ValueError("interrupted publication advertised cache availability")
    publish(fixture.cache, report, **arguments)
    publish(fixture.cache, report, **arguments)
    cold_copy(fixture, backend, "public-cold", [fixture.keys[0]])
    must_refuse(lambda: cold_copy(fixture, backend, "wrong-key-cold", [fixture.keys[1]]), "a wrong public cache key")

    interrupted = uuid.uuid4().hex
    previous = backend._read(LOCK)
    if previous is None:
        raise ValueError("cache rehearsal lost its publication lock")
    lock = fixture.work / "interrupted-lock.json"
    # Lock bytes use the transport's exact canonical representation.
    lock.write_bytes(json.dumps({"schema_version": 1, "operation": interrupted}, sort_keys=True).encode())
    backend._write(LOCK, lock, "application/json", "no-store", "--if-match", previous[1])
    must_refuse(lambda: publish(fixture.cache, report, **arguments), "a held publication lock")
    must_refuse(lambda: backend.recover_lock(uuid.uuid4().hex), "recovery of another operation")
    backend.recover_lock(interrupted)
    publish(fixture.cache, report, **arguments)

    rotated = fixture.work / "rotated"
    shutil.copytree(fixture.cache, rotated)
    nix("store", "sign", "--store", rotated.as_uri(), "--key-file", str(fixture.key_files[1]),
        "--recursive", "--stdin", stdin=fixture.output + "\n")
    verify(rotated, [fixture.output], fixture.keys)
    rotation = {**report, "public_keys": fixture.keys,
                "objects": inventory(rotated, report["closure"], fixture.keys)}
    publish(rotated, rotation, **arguments)
    cold_copy(fixture, backend, "rotated-cold", [fixture.keys[1]])
    withdraw(request, authorize=lambda selected: selected == request, backend_factory=arguments["backend_factory"])
    must_refuse(lambda: publish(rotated, rotation, **arguments), "a withdrawn cache release")
    for row in report["objects"]:
        backend.verify_public(row["name"], next(item for item in rotation["objects"] if item["name"] == row["name"]))
    return ["public-cold-substitution", "wrong-key-refusal", "negative-cache-read", "interrupted-publication-retry",
            "conditional-lock", "exact-lock-recovery", "rotation-overlap", "rotation-new-key", "withdrawal-retention"]


def main():
    # The common private loader is supplied by the bucket-scoped rehearsal prerequisite.
    from scripts.release_storage.rehearsal import configuration, environment
    selected = environment(configuration(os.environ), os.environ)
    selected["DOWNLOAD_BASE_URL"] = selected["RELEASE_TEST_DOWNLOAD_BASE_URL"]
    os.environ.clear()
    os.environ.update(selected)
    with cache_fixture() as fixture:
        backend = RehearsalTransport(fixture.report["request"])
        prefix = backend.prefix
        print("Nix cache R2 rehearsal prefix: " + prefix, flush=True)
        try:
            checks = execute(fixture, backend)
        finally:
            backend.cleanup()
            print("Nix cache R2 rehearsal exact-key cleanup verified", flush=True)
        print(json.dumps({"schema_version": 1, "mode": "rehearsal", "status": "passed", "cache_prefix": prefix,
                          "system": fixture.system, "closure_paths": len(fixture.report["closure"]), "checks": checks}, sort_keys=True))


if __name__ == "__main__":
    main()
