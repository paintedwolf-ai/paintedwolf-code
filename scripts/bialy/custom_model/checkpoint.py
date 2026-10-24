"""Durable run-local checkpoints and interruption-safe reference scores."""
from contextlib import contextmanager
import fcntl
import json
import math
import os
from pathlib import Path
import shutil
import sqlite3
import uuid

from common import digest, file_digest


def sync_directory(path):
    fd = os.open(path, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def atomic_json(path, value):
    path = Path(path)
    temporary = path.with_name("." + path.name + "." + uuid.uuid4().hex)
    with temporary.open("x") as stream:
        json.dump(value, stream, sort_keys=True, allow_nan=False)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)
    sync_directory(path.parent)


class Checkpoints:
    def __init__(self, root, identity, keep=3):
        self.root, self.identity, self.keep = Path(root), identity, keep
        if keep < 2:
            raise ValueError("retain at least two recovery checkpoints")
        self.root.mkdir(exist_ok=True)

    def directories(self):
        return sorted(p for p in self.root.glob("[0-9]*") if p.is_dir() and not p.is_symlink())

    def restore(self):
        import torch
        rejected = []
        for directory in reversed(self.directories()):
            try:
                manifest = json.loads((directory / "manifest.json").read_text())
                if manifest["identity"] != self.identity:
                    raise ValueError("checkpoint belongs to changed training inputs")
                if file_digest(directory / "state.pt") != manifest["state_sha256"]:
                    raise ValueError("checkpoint checksum mismatch")
                state = torch.load(directory / "state.pt", map_location="cpu", weights_only=True)
                return state, rejected
            except (OSError, ValueError, KeyError, json.JSONDecodeError) as exc:
                rejected.append({"checkpoint":directory.name,"reason":str(exc)})
        if rejected:
            raise ValueError("no intact recovery checkpoint: " + json.dumps(rejected))
        return None, []

    def save(self, state):
        import torch
        def tensor_bytes(value):
            if isinstance(value, torch.Tensor):
                return value.numel() * value.element_size()
            if isinstance(value, dict):
                return sum(tensor_bytes(v) for v in value.values())
            if isinstance(value, (tuple,list)):
                return sum(map(tensor_bytes,value))
            return 0
        if shutil.disk_usage(self.root).free < 2*tensor_bytes(state) + (4 << 20):
            raise OSError("insufficient free disk for a recovery checkpoint; previous checkpoints remain intact")
        directories = self.directories()
        generation = max((int(p.name.split("-")[0]) for p in directories), default=0) + 1
        staged = self.root / (".pending-" + uuid.uuid4().hex)
        staged.mkdir()
        target = self.root / ("%08d-step%012d" % (generation, state["cursor"]["step"]))
        torch.save(state, staged / "state.pt")
        with (staged / "state.pt").open("rb") as stream:
            os.fsync(stream.fileno())
        atomic_json(staged / "manifest.json", {"identity":self.identity,"state_sha256":file_digest(staged / "state.pt")})
        sync_directory(staged)
        os.rename(staged, target)
        sync_directory(self.root)
        # Only complete checkpoints from this run are eligible for retention.
        verified = []
        for candidate in self.directories():
            try:
                manifest = json.loads((candidate / "manifest.json").read_text())
                if manifest.get("identity") == self.identity and file_digest(candidate / "state.pt") == manifest.get("state_sha256"):
                    verified.append(candidate)
            except (OSError, ValueError):
                continue
        for obsolete in verified[:-self.keep]:
            try:
                manifest = json.loads((obsolete / "manifest.json").read_text())
            except (OSError, ValueError):
                continue
            if manifest.get("identity") == self.identity:
                shutil.rmtree(obsolete)
        sync_directory(self.root)
        return target


class ReferenceCache:
    def __init__(self, path, identity):
        self.identity = identity
        self.db = sqlite3.connect(path)
        self.db.execute("PRAGMA synchronous=FULL")
        self.db.execute("CREATE TABLE IF NOT EXISTS scores(key TEXT PRIMARY KEY, value REAL NOT NULL, digest TEXT NOT NULL)")
        if self.db.execute("PRAGMA quick_check").fetchone()[0] != "ok":
            raise ValueError("reference cache is corrupt; preserve it and rebuild the cache")

    def get(self, key):
        row = self.db.execute("SELECT value,digest FROM scores WHERE key=?", (key,)).fetchone()
        if row is None:
            return None
        value, checksum = row
        if not math.isfinite(value) or checksum != digest([self.identity,key,value]):
            return None
        return value

    def put(self, key, value):
        if not math.isfinite(value):
            raise ValueError("reference score is not finite")
        with self.db:
            self.db.execute("INSERT OR REPLACE INTO scores VALUES(?,?,?)", (key,value,digest([self.identity,key,value])))

    def close(self):
        self.db.close()


@contextmanager
def exclusive_run(root, resume):
    root = Path(root)
    root.mkdir(parents=True, exist_ok=resume)
    with (root / ".run.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise ValueError("another process owns this training run") from exc
        try:
            yield
        finally:
            fcntl.flock(lock, fcntl.LOCK_UN)
