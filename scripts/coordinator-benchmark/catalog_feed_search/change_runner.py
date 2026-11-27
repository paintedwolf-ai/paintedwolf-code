"""Deterministic change-script generator and runner with independent write log (F01, D5)."""

from __future__ import annotations

import base64
from dataclasses import dataclass
import hashlib
import os
from pathlib import Path
import random
import stat
import time
from typing import Callable

from .seeds import ReplaySelector, seed_rng

CHANGE_OP_KINDS = ("create", "edit", "rename", "delete", "chmod", "atomic_replace")


@dataclass(frozen=True)
class ChangeOp:
    """A single filesystem mutation in a generated change script."""

    kind: str  # create | edit | rename | delete | chmod | atomic_replace
    path: str
    new_path: str | None = None
    content: bytes | None = None
    mode: int | None = None

    def to_dict(self) -> dict[str, object]:
        data: dict[str, object] = {"kind": self.kind, "path": self.path}
        if self.new_path is not None:
            data["new_path"] = self.new_path
        if self.content is not None:
            data["content_b64"] = base64.b64encode(self.content).decode("ascii")
        if self.mode is not None:
            data["mode"] = self.mode
        return data

    @classmethod
    def from_dict(cls, data: dict[str, object]) -> "ChangeOp":
        raw = data.get("content_b64")
        return cls(
            kind=str(data["kind"]),
            path=str(data["path"]),
            new_path=str(data["new_path"]) if data.get("new_path") is not None else None,
            content=base64.b64decode(str(raw)) if raw is not None else None,
            mode=int(data["mode"]) if data.get("mode") is not None else None,
        )


@dataclass(frozen=True)
class ChangeScript:
    """Ordered sequence of filesystem operations bound to a replay selector."""

    selector: ReplaySelector
    ops: tuple[ChangeOp, ...]

    def to_dict(self) -> dict[str, object]:
        return {
            "selector": self.selector.to_dict(),
            "ops": [op.to_dict() for op in self.ops],
        }

    @classmethod
    def from_dict(cls, data: dict[str, object]) -> "ChangeScript":
        return cls(
            selector=ReplaySelector.from_dict(dict(data["selector"])),  # type: ignore[arg-type]
            ops=tuple(ChangeOp.from_dict(dict(o)) for o in data["ops"]),  # type: ignore[union-attr]
        )


@dataclass(frozen=True)
class WriteLogEntry:
    """Independent ground-truth record of one executed filesystem write."""

    seq: int
    clock_domain: str
    timestamp_ns: int
    op: str
    path: str
    new_path: str | None
    before_sha256: str | None
    after_sha256: str | None
    before_mode: int | None = None
    after_mode: int | None = None
    root_id: str = "root-0"

    def to_dict(self) -> dict[str, object]:
        return {
            "seq": self.seq,
            "root_id": self.root_id,
            "clock_domain": self.clock_domain,
            "timestamp_ns": self.timestamp_ns,
            "op": self.op,
            "path": self.path,
            "new_path": self.new_path,
            "before_sha256": self.before_sha256,
            "after_sha256": self.after_sha256,
            "before_mode": self.before_mode,
            "after_mode": self.after_mode,
        }


@dataclass(frozen=True)
class WriteLog:
    """Complete independent write log produced by ChangeRunner."""

    clock_domain: str
    entries: tuple[WriteLogEntry, ...]

    def touched_paths(self) -> set[str]:
        paths: set[str] = set()
        for entry in self.entries:
            paths.add(entry.path)
            if entry.new_path:
                paths.add(entry.new_path)
        return paths

    def to_dict(self) -> dict[str, object]:
        return {
            "clock_domain": self.clock_domain,
            "entries": [e.to_dict() for e in self.entries],
        }


def generate_change_script(
    base_seed: str,
    family_id: str,
    instance_index: int,
    initial_files: list[str],
    op_count: int = 12,
    allowed_ops: tuple[str, ...] = CHANGE_OP_KINDS,
) -> ChangeScript:
    """Deterministically generate a valid ChangeScript over initial_files."""
    selector = ReplaySelector(base_seed=base_seed, family_id=family_id, instance_index=instance_index)
    rng = seed_rng(base_seed, f"{family_id}:changes", instance_index)
    live_files = sorted(set(initial_files))
    ops: list[ChangeOp] = []
    create_seq = 0

    for step in range(max(0, op_count)):
        if not live_files:
            kind = "create"
        else:
            kind = rng.choice(allowed_ops)

        if kind == "create":
            path = f"generated/created_{create_seq:04d}.txt"
            create_seq += 1
            body = f"created step={step} seq={create_seq} token=change_create\n".encode("utf-8")
            ops.append(ChangeOp(kind="create", path=path, content=body))
            live_files.append(path)
            live_files.sort()
        elif kind == "edit":
            target = rng.choice(live_files)
            body = f"edited step={step} path={target} token=change_edit\n".encode("utf-8")
            ops.append(ChangeOp(kind="edit", path=target, content=body))
        elif kind == "atomic_replace":
            target = rng.choice(live_files)
            body = f"atomic step={step} path={target} token=change_atomic\n".encode("utf-8")
            ops.append(ChangeOp(kind="atomic_replace", path=target, content=body))
        elif kind == "rename":
            target = rng.choice(live_files)
            new_path = f"renamed/moved_{step:04d}_{Path(target).name}"
            ops.append(ChangeOp(kind="rename", path=target, new_path=new_path))
            live_files.remove(target)
            live_files.append(new_path)
            live_files.sort()
        elif kind == "delete":
            target = rng.choice(live_files)
            ops.append(ChangeOp(kind="delete", path=target))
            live_files.remove(target)
        elif kind == "chmod":
            target = rng.choice(live_files)
            mode = rng.choice((0o644, 0o755, 0o600))
            ops.append(ChangeOp(kind="chmod", path=target, mode=mode))
        else:
            raise ValueError(f"unsupported change op kind: {kind}")

    return ChangeScript(selector=selector, ops=tuple(ops))


def _file_sha256_and_mode(path: Path) -> tuple[str | None, int | None]:
    if not path.exists() or not path.is_file():
        return None, None
    raw = path.read_bytes()
    mode = stat.S_IMODE(os.lstat(path).st_mode)
    return hashlib.sha256(raw).hexdigest(), mode


class ChangeRunner:
    """Executes a ChangeScript against a filesystem root and records an independent WriteLog."""

    def __init__(
        self,
        clock_fn: Callable[[], int] | None = None,
        clock_domain: str = "monotonic_ns",
    ) -> None:
        self._clock_fn = clock_fn or time.monotonic_ns
        self._clock_domain = clock_domain

    def run(self, root: Path, script: ChangeScript) -> WriteLog:
        root_path = Path(root)
        entries: list[WriteLogEntry] = []

        for seq, op in enumerate(script.ops, start=1):
            src = root_path / Path(op.path)
            before_sha, before_mode = _file_sha256_and_mode(src)
            after_sha: str | None = None
            after_mode: int | None = None

            if op.kind == "create":
                src.parent.mkdir(parents=True, exist_ok=True)
                src.write_bytes(op.content or b"")
                after_sha, after_mode = _file_sha256_and_mode(src)
            elif op.kind == "edit":
                src.parent.mkdir(parents=True, exist_ok=True)
                src.write_bytes(op.content or b"")
                after_sha, after_mode = _file_sha256_and_mode(src)
            elif op.kind == "atomic_replace":
                src.parent.mkdir(parents=True, exist_ok=True)
                tmp = src.parent / f".tmp.{src.name}.{seq}"
                tmp.write_bytes(op.content or b"")
                os.replace(tmp, src)
                after_sha, after_mode = _file_sha256_and_mode(src)
            elif op.kind == "rename":
                if not op.new_path:
                    raise ValueError("rename op requires new_path")
                dst = root_path / Path(op.new_path)
                dst.parent.mkdir(parents=True, exist_ok=True)
                os.replace(src, dst)
                after_sha, after_mode = _file_sha256_and_mode(dst)
            elif op.kind == "delete":
                if src.exists() or src.is_symlink():
                    src.unlink()
                after_sha, after_mode = None, None
            elif op.kind == "chmod":
                if op.mode is None:
                    raise ValueError("chmod op requires mode")
                os.chmod(src, op.mode)
                after_sha, after_mode = _file_sha256_and_mode(src)
            else:
                raise ValueError(f"unsupported op kind: {op.kind}")

            entries.append(
                WriteLogEntry(
                    seq=seq,
                    clock_domain=self._clock_domain,
                    timestamp_ns=int(self._clock_fn()),
                    op=op.kind,
                    path=op.path,
                    new_path=op.new_path,
                    before_sha256=before_sha,
                    after_sha256=after_sha,
                    before_mode=before_mode,
                    after_mode=after_mode,
                )
            )

        return WriteLog(clock_domain=self._clock_domain, entries=tuple(entries))
