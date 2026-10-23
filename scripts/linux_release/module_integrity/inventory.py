"""Canonical module bytes selected by the privileged runtime producer.

This codec grants no filesystem authority. The derived generation's final
manifest authenticates its auditor, guard and instrumented executable anchors.
"""
from __future__ import annotations

import hashlib
import struct

MAGIC = b"PWMIV01\0"
CAPACITY = 4 * 1024 * 1024
MAX_ENTRIES = 32768
MACHINES = {"x86_64": 62, "aarch64": 183}
HEADER = struct.Struct("<8sIIQQHH4x")
ENTRY = struct.Struct("<Q32sH")


def path_bytes(path: str) -> bytes:
    if not isinstance(path, str):
        raise ValueError("module alias must be text")
    raw = path.encode("utf-8")
    if (not 1 < len(raw) < 4096 or not raw.startswith(b"/") or raw.endswith(b"/")
            or any(value < 32 or value == 127 for value in raw)
            or any(part in (b"", b".", b"..") for part in raw.split(b"/")[1:])):
        raise ValueError("module alias must be a canonical absolute path")
    return raw


def encode(entries: list[dict], guard: str, offset: int, digest_offset: int, architecture: str) -> bytes:
    """Encode exact ordinary modules; callers separately authenticate anchors."""
    if (not isinstance(entries, list) or not 1 <= len(entries) <= MAX_ENTRIES
            or type(offset) is not int or not 0 < offset <= 64 * 1024 * 1024 or offset % 8
            or type(digest_offset) is not int or not 0 < digest_offset <= 64 * 1024 * 1024 or digest_offset % 8
            or architecture not in MACHINES):
        raise ValueError("invalid module inventory shape")
    guard_raw = path_bytes(guard)
    selected = {}
    for entry in entries:
        if not isinstance(entry, dict) or set(entry) != {"path", "size", "sha256"}:
            raise ValueError("invalid module inventory entry")
        name, size, digest = path_bytes(entry["path"]), entry["size"], entry["sha256"]
        if (name == guard_raw or name in selected or type(size) is not int or not 0 < size < 1 << 63
                or not isinstance(digest, str) or len(digest) != 64
                or any(value not in "0123456789abcdef" for value in digest)):
            raise ValueError("invalid or duplicate ordinary module")
        selected[name] = ENTRY.pack(size, bytes.fromhex(digest), len(name)) + name
    body = guard_raw + b"".join(selected[name] for name in sorted(selected))
    size = HEADER.size + len(body)
    if size > CAPACITY - 16:
        raise ValueError("module inventory exceeds its embedded capacity")
    return HEADER.pack(MAGIC, size, len(selected), offset, digest_offset,
                       len(guard_raw), MACHINES[architecture]) + body


def decode(raw: bytes) -> dict:
    """Refuse unknown, duplicate, unsorted or trailing inventory shapes."""
    if not isinstance(raw, bytes) or not HEADER.size <= len(raw) <= CAPACITY - 16:
        raise ValueError("invalid module inventory length")
    magic, size, count, offset, digest_offset, guard_size, machine = HEADER.unpack_from(raw)
    if (magic != MAGIC or size != len(raw) or not 1 <= count <= MAX_ENTRIES
            or raw[36:40] != bytes(4) or machine not in MACHINES.values()
            or not 0 < offset <= 64 * 1024 * 1024 or offset % 8
            or not 0 < digest_offset <= 64 * 1024 * 1024 or digest_offset % 8):
        raise ValueError("invalid module inventory header")
    cursor = HEADER.size
    try:
        guard = raw[cursor:cursor + guard_size].decode("utf-8")
        if len(path_bytes(guard)) != guard_size:
            raise ValueError("invalid guard alias length")
        cursor += guard_size
        entries, previous = [], b""
        for _ in range(count):
            if len(raw) - cursor < ENTRY.size:
                raise ValueError("truncated module inventory entry")
            length, digest, name_size = ENTRY.unpack_from(raw, cursor)
            cursor += ENTRY.size
            name = raw[cursor:cursor + name_size]
            alias = name.decode("utf-8")
            if (path_bytes(alias) != name or len(name) != name_size or name <= previous
                    or alias == guard or not 0 < length < 1 << 63):
                raise ValueError("invalid or unsorted ordinary module")
            entries.append({"path": alias, "size": length, "sha256": digest.hex()})
            previous, cursor = name, cursor + name_size
    except UnicodeDecodeError as error:
        raise ValueError("module inventory alias is not UTF-8") from error
    if cursor != len(raw):
        raise ValueError("module inventory has trailing bytes")
    return {"format_version": 1, "architecture": next(k for k, v in MACHINES.items() if v == machine),
            "guard": guard, "guard_witness_offset": offset, "guard_inventory_offset": digest_offset,
            "entries": entries,
            "sha256": hashlib.sha256(raw).hexdigest()}
