"""Noncredentialed cache transport, release binding and retained-reference controls."""
import base64
from contextlib import contextmanager
import copy
import datetime as dt
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import tempfile
import threading
import types
import sys
import unittest
from unittest.mock import Mock, patch

from scripts.nix_cache.admission import inventory
from scripts.nix_cache.publication import encoded, publish, verified_report, withdraw
from scripts.nix_cache.r2 import R2Transport, LOCK
from scripts.nix_cache.release_authority import authorize, nar_sha256
from scripts.nix_cache.retention import collect, recover

OUTPUT = "/nix/store/" + "0" * 32 + "-application"
PUBLIC = "paintedwolf-cache-1:" + base64.b64encode(bytes(32)).decode()
SRI = "sha256-" + base64.b64encode(bytes(32)).decode()


class DirectoryStore:
    def __init__(self, root):
        self.root, self.events, self.locked, self.fail, self.created = root, [], False, None, {}

    @contextmanager
    def serialized(self, operation):
        if self.locked:
            raise ValueError("already locked")
        self.locked = True
        try:
            yield self
        finally:
            self.locked = False

    def read(self, name):
        path = self.root / name
        return path.read_bytes() if path.exists() else None

    def put_bytes(self, name, data, *headers):
        old = self.read(name)
        if old is not None and old != data:
            raise ValueError("immutable collision")
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        self.events.append(("put", name))

    def put(self, name, path, record):
        self.put_bytes(name, path.read_bytes())

    def replace(self, name, path, record):
        (self.root / name).write_bytes(path.read_bytes())
        self.events.append(("rotate", name))

    def verify_public(self, name, record):
        if name == self.fail:
            raise ValueError("public cached miss")
        if hashlib.sha256(self.read(name)).hexdigest() != record["sha256"]:
            raise ValueError("public mismatch")
        self.events.append(("public", name))

    def listing(self):
        return [{"name": path.relative_to(self.root).as_posix(), "modified": self.created.get(path.name, dt.datetime(2020, 1, 1, tzinfo=dt.timezone.utc))}
                for path in self.root.rglob("*") if path.is_file()]

    def delete(self, row):
        self.events.append(("delete", row["name"]))
        (self.root / row["name"]).unlink()


class CachePublicationTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.cache = self.root / "cache"
        (self.cache / "nar").mkdir(parents=True)
        (self.cache / "nar/content.nar.xz").write_bytes(b"compressed fixture")
        self.metadata = "0" * 32 + ".narinfo"
        (self.cache / self.metadata).write_text(f"StorePath: {OUTPUT}\nURL: nar/content.nar.xz\nFileSize: 18\nSig: paintedwolf-cache-1:fixture\n")
        (self.cache / "nix-cache-info").write_text("StoreDir: /nix/store\n")
        self.closure = {OUTPUT: {"nar_hash": SRI, "nar_bytes": 37, "references": [], "derivation": OUTPUT + ".drv"}}
        request = {"schema_version": 1, "mode": "production", "version": "1.0.0", "application_commit": "a" * 40,
                   "flake_lock_sha256": "b" * 64, "system": "x86_64-linux", "outputs": [OUTPUT], "cache_prefix": "nix-cache"}
        self.report = {"schema_version": 1, "status": "passed", "request": request, "public_keys": [PUBLIC], "nix_version": "2.35.3",
                       "closure_sha256": hashlib.sha256(json.dumps(self.closure, sort_keys=True, separators=(",", ":")).encode()).hexdigest(),
                       "closure": self.closure, "derivations": {}, "objects": inventory(self.cache, self.closure, [PUBLIC]),
                       "upstream_dependencies": [], "dependency_policy": "complete-closure-in-this-cache"}
        destination = self.root / "published"
        destination.mkdir()
        self.store = DirectoryStore(destination)
        self.factory = Mock(return_value=self.store)
        self.nix = patch("scripts.nix_cache.publication.verify")
        self.nix.start()
        self.addCleanup(self.nix.stop)
        info = {OUTPUT: {"narHash": SRI, "narSize": 37, "references": [], "deriver": OUTPUT + ".drv"}}
        self.info = patch("scripts.nix_cache.publication.path_info", return_value=info)
        self.info.start()
        self.addCleanup(self.info.stop)

    def publish(self, **changes):
        return publish(self.cache, self.report, authorize=lambda request, report: True, backend_factory=self.factory, **changes)

    def test_refused_authority_or_modified_cache_never_loads_transport_credentials(self):
        with self.assertRaises(ValueError):
            publish(self.cache, self.report, authorize=lambda *args: False, backend_factory=self.factory)
        self.factory.assert_not_called()
        (self.cache / "nar/content.nar.xz").write_bytes(b"tampered")
        with self.assertRaises(ValueError):
            self.publish()
        self.factory.assert_not_called()

    def test_report_cannot_relabel_the_actual_cache_nar_identity(self):
        report = copy.deepcopy(self.report)
        report["closure"][OUTPUT]["nar_hash"] = "sha256-" + base64.b64encode(b"x" * 32).decode()
        report["closure_sha256"] = hashlib.sha256(json.dumps(report["closure"], sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        with self.assertRaises(ValueError):
            verified_report(self.cache, report)

    def test_content_precedes_metadata_and_complete_record_retry_survives_cached_miss(self):
        self.store.fail = self.metadata
        with self.assertRaises(ValueError):
            self.publish()
        self.assertFalse(any(name.startswith("release-records/") for _, name in self.store.events))
        self.store.fail = None
        self.publish()
        self.publish()
        writes = [name for action, name in self.store.events if action == "put"]
        self.assertLess(writes.index("nar/content.nar.xz"), writes.index(self.metadata))
        self.assertEqual(writes[-1], "release-records/1.0.0/x86_64-linux.json")

    def test_rotation_adds_signatures_without_changing_old_release_records(self):
        self.publish()
        original = self.store.read("release-records/1.0.0/x86_64-linux.json")
        path = self.cache / self.metadata
        path.write_text(path.read_text() + "Sig: paintedwolf-cache-2:fixture\n")
        self.report["objects"] = inventory(self.cache, self.closure, [PUBLIC])
        self.publish()
        self.assertEqual(self.store.read("release-records/1.0.0/x86_64-linux.json"), original)
        self.assertTrue(any("-rotation-" in name for _, name in self.store.events))
        path.write_text(path.read_text().replace("Sig: paintedwolf-cache-1:fixture\n", ""))
        self.report["public_keys"] = ["paintedwolf-cache-2:" + base64.b64encode(bytes(32)).decode()]
        self.report["objects"] = inventory(self.cache, self.closure, self.report["public_keys"])
        with self.assertRaises(ValueError):
            self.publish()

    def test_withdrawal_keeps_pins_and_gc_only_removes_old_unpublished_debris(self):
        self.publish()
        request = self.report["request"]
        withdraw(request, authorize=lambda request: True, backend_factory=self.factory)
        self.store.put_bytes("nar/orphan.nar.xz", b"orphan")
        self.store.put_bytes("nar/recent.nar.xz", b"recent")
        self.store.created["recent.nar.xz"] = dt.datetime.now(dt.timezone.utc)
        garbage = collect(request, dt.datetime(2021, 1, 1, tzinfo=dt.timezone.utc), authorize=lambda *args: True, backend_factory=self.factory)
        self.assertEqual(garbage, ["nar/orphan.nar.xz"])
        self.assertIsNotNone(self.store.read("nar/content.nar.xz"))
        with self.assertRaises(ValueError):
            self.publish()
        (self.store.root / "nar/content.nar.xz").unlink()
        with self.assertRaises(ValueError):
            collect(request, dt.datetime(2021, 1, 1, tzinfo=dt.timezone.utc), authorize=lambda *args: True, backend_factory=self.factory)

    def test_collection_and_recovery_refuse_before_backend_without_explicit_authority(self):
        with self.assertRaises(ValueError):
            collect({}, dt.datetime.now(dt.timezone.utc), authorize=lambda *args: False, backend_factory=self.factory)
        with self.assertRaises(ValueError):
            recover({}, "a" * 32, authorize=lambda *args: False, backend_factory=self.factory)
        self.factory.assert_not_called()

    def test_fresh_release_admission_binds_every_selected_output_and_actual_nar(self):
        artifact = {"architecture": "amd64", "identity": {"kind": "nix-closure", "store_path": OUTPUT,
                     "lock_sha256": "b" * 64, "sha256": bytes(32).hex()}}
        verify = Mock(return_value={"accepted": True})
        evidence = Mock(return_value={"nix-amd64": artifact})
        modules = {"scripts.linux_release.release_admission": types.SimpleNamespace(verify_release=verify),
                   "scripts.linux_release.installed_evidence": types.SimpleNamespace(artifacts=evidence)}
        selection = {"source_commit": "a" * 40, "version": "1.0.0", "artifacts": {}}
        request = self.report["request"]
        with patch.dict(sys.modules, modules):
            self.assertTrue(authorize(selection, request, self.report))
            self.assertTrue(authorize(selection, request, self.report))
            self.assertEqual(verify.call_count, 2)
            for changed in [{**request, "version": "1.0.1"}, {**request, "application_commit": "c" * 40},
                            {**request, "flake_lock_sha256": "c" * 64}, {**request, "outputs": [OUTPUT + "-extra"]},
                            {**request, "system": "aarch64-linux"}]:
                with self.subTest(request=changed), self.assertRaises(ValueError):
                    authorize(selection, changed, {**self.report, "request": changed})
            wrong = copy.deepcopy(self.report)
            wrong["closure"][OUTPUT]["nar_hash"] = "sha256-" + base64.b64encode(b"x" * 32).decode()
            with self.assertRaises(ValueError):
                authorize(selection, request, wrong)
            verify.return_value = {"accepted": False}
            with self.assertRaises(ValueError):
                authorize(selection, request, self.report)
        for value in ["sha256:" + "0" * 64, "sha512-" + SRI[7:], "sha256-AA==", "sha256-" + SRI[7:] + "="]:
            with self.subTest(hash=value), self.assertRaises(ValueError):
                nar_sha256(value)


class CachePublicHTTPTests(unittest.TestCase):
    def test_ordinary_url_refuses_cached_misses_tampering_and_stale_metadata(self):
        state = {"status": 404, "data": b"missing", "cache": "public, max-age=300", "age": "10", "paths": []}
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                state["paths"].append(self.path)
                self.send_response(state["status"])
                self.send_header("Cache-Control", state["cache"])
                self.send_header("Age", state["age"])
                self.end_headers()
                self.wfile.write(state["data"])

            def log_message(self, *args):
                pass
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            transport = R2Transport.__new__(R2Transport)
            transport.public = f"http://127.0.0.1:{server.server_port}/nix-cache"
            data = b"metadata"
            record = {"phase": "narinfo", "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()}
            with self.assertRaises(ValueError):
                transport.verify_public("object.narinfo", record)
            state.update(status=200, data=data, cache="no-cache, no-store, must-revalidate", age="0")
            transport.verify_public("object.narinfo", record)
            for changes in [{"age": "2"}, {"cache": "public, max-age=300"}, {"data": b"tampered"}]:
                original = dict(state)
                state.update(changes)
                with self.subTest(changes=changes), self.assertRaises(ValueError):
                    transport.verify_public("object.narinfo", record)
                state.update(original)
            self.assertTrue(all(path == "/nix-cache/object.narinfo" for path in state["paths"]))
        finally:
            server.shutdown()
            server.server_close()
            thread.join()

    def test_conditional_lock_never_steals_and_recovery_uses_exact_observed_identity(self):
        transport = R2Transport.__new__(R2Transport)
        operation = "a" * 32
        transport._read = Mock(return_value=None)
        transport._write = Mock(return_value='{"ETag":"held"}')
        with transport.serialized(operation):
            self.assertEqual(transport._write.call_args.args[-2:], ("--if-none-match", "*"))
        self.assertEqual(transport._write.call_args.args[-2:], ("--if-match", "held"))
        held = json.dumps({"schema_version": 1, "operation": operation}, sort_keys=True).encode()
        transport._read.return_value = (held, "observed")
        transport._write.reset_mock()
        with self.assertRaises(ValueError):
            with transport.serialized("b" * 32):
                self.fail("held lock was stolen")
        transport._write.assert_not_called()
        with self.assertRaises(ValueError):
            transport.recover_lock("b" * 32)
        transport.recover_lock(operation)
        self.assertEqual(transport._write.call_args.args[-2:], ("--if-match", "observed"))
        transport._read.return_value = (b'{"schema_version": 1, "state": "free"}', "free-tag")
        with transport.serialized("c" * 32):
            self.assertEqual(transport._write.call_args.args[-2:], ("--if-match", "free-tag"))
