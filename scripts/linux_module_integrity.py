"""Describe the fixed guard ABI from final ELF bytes; no host trust is inferred."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
import struct

LIMIT = 32 * 1024 * 1024
MACHINES = {62: "x86_64", 183: "aarch64"}
WITNESS = "pw_module_guard_witness_v2"


class ELF:
    def __init__(self, path: Path):
        if path.is_symlink() or not path.is_file() or path.stat().st_size > LIMIT:
            raise ValueError("module must be a bounded physical file")
        self.raw = path.read_bytes()
        header = self.unpack("16sHHIQQQIHHHHHH", 0)
        if header[0][:7] != b"\x7fELF\x02\x01\x01" or header[1] != 3 or header[2] not in MACHINES:
            raise ValueError("module must be a supported little-endian ELF64 DSO")
        self.architecture = MACHINES[header[2]]
        phoff, shoff = header[5:7]
        if header[9] != 56 or not 1 <= header[10] <= 64 or header[11] != 64 or not 1 <= header[12] <= 4096:
            raise ValueError("invalid module table bounds")
        self.segments = [self.unpack("IIQQQQQQ", phoff + i * 56) for i in range(header[10])]
        self.sections = [self.unpack("IIQQQQIIQQ", shoff + i * 64) for i in range(header[12])]
        if any(row[0] == 3 for row in self.segments):
            raise ValueError("DSO must not have an ELF interpreter")
        dynamic = [row for row in self.segments if row[0] == 2]
        if len(dynamic) != 1 or dynamic[0][5] > LIMIT or dynamic[0][5] % 16:
            raise ValueError("DSO must have one bounded dynamic table")
        self.tags = []
        for offset in range(dynamic[0][2], dynamic[0][2] + dynamic[0][5], 16):
            tag, value = self.unpack("QQ", offset)
            if not tag:
                break
            self.tags.append((tag, value))
        else:
            raise ValueError("dynamic table must terminate")
        if any(tag == 1 for tag, _ in self.tags):
            raise ValueError("module must have no DT_NEEDED dependencies")

    def unpack(self, shape, offset):
        size = struct.calcsize("<" + shape)
        if offset < 0 or offset + size > len(self.raw):
            raise ValueError("ELF table exceeds file")
        return struct.unpack_from("<" + shape, self.raw, offset)

    def bytes(self, offset, count):
        if offset < 0 or count < 0 or offset + count > len(self.raw):
            raise ValueError("ELF bytes exceed file")
        return self.raw[offset:offset + count]

    def symbols(self):
        symbols = []
        for row in self.sections:
            if row[1] != 11:
                continue
            if row[9] != 24 or row[5] % 24 or row[6] >= len(self.sections):
                raise ValueError("invalid dynamic symbol table")
            strings = self.sections[row[6]]
            table = self.bytes(strings[4], strings[5])
            for offset in range(row[4], row[4] + row[5], 24):
                name, info, other, section, value, size = self.unpack("IBBHQQ", offset)
                if name >= len(table):
                    raise ValueError("symbol name exceeds table")
                end = table.find(b"\0", name)
                if end < 0:
                    raise ValueError("unterminated symbol name")
                text = table[name:end].decode("ascii", errors="strict")
                if text and section == 0:
                    raise ValueError("module must have no undefined dynamic symbols")
                symbols.append((text, info, other, section, value, size))
        return symbols

    def contains(self, kind, offset, size=8, *, writable=False):
        return [row for row in self.segments if row[0] == kind
                and (not writable or row[1] & 2)
                and row[3] <= offset and offset + size <= row[3] + row[6]]


def describe(auditor: Path, guard: Path) -> dict:
    """The caller separately authorizes roots, source, installation and lifetime."""
    audit, gate = ELF(auditor), ELF(guard)
    if audit.architecture != gate.architecture:
        raise ValueError("auditor and guard architectures differ")
    exports = {row[0] for row in audit.symbols() if row[1] >> 4 == 1 and row[1] & 15 == 2}
    if not {"la_version", "la_objsearch", "la_objopen"}.issubset(exports):
        raise ValueError("auditor callbacks missing")
    if any(tag in (12, 25) and value for tag, value in audit.tags):
        raise ValueError("auditor must not have initialization callbacks")
    flags = [value for tag, value in gate.tags if tag == 0x6ffffffb]
    if len(flags) != 1 or not flags[0] & 0x20:
        raise ValueError("guard requires DF_1_INITFIRST")
    matches = [row for row in gate.symbols() if row[0] == WITNESS]
    if len(matches) != 1:
        raise ValueError("guard witness must be unique")
    _, info, other, section, offset, size = matches[0]
    if info != 0x11 or other != 0 or section == 0 or size != 8 or offset % 8 or not 0 < offset <= 64 * 1024 * 1024:
        raise ValueError("guard witness must be a public aligned eight-byte object")
    writable = gate.contains(1, offset, writable=True)
    if len(writable) != 1 or not gate.contains(0x6474e552, offset):
        raise ValueError("guard witness must lie inside writable load and GNU_RELRO segments")
    row = writable[0]
    if offset + 8 > row[3] + row[5] or gate.bytes(row[2] + offset - row[3], 8) != bytes(8):
        raise ValueError("guard witness must begin as zero in final file bytes")
    return {
        "schema_version": 2, "architecture": audit.architecture, "guard_witness_offset": offset,
        "auditor_sha256": hashlib.sha256(audit.raw).hexdigest(),
        "guard_sha256": hashlib.sha256(gate.raw).hexdigest(),
    }


def policy_bytes(roots: list[str], guard: str, offset: int, nonce: bytes) -> bytes:
    """Encode already-authorized physical roots; this function grants no host authority."""
    def encoded(path):
        if not isinstance(path, str):
            raise ValueError("module path must be text")
        raw = path.encode("utf-8")
        if (not 1 < len(raw) < 4096 or not raw.startswith(b"/") or raw.endswith(b"/")
                or any(value < 32 or value == 127 for value in raw)
                or any(part in (b"", b".", b"..") for part in raw.split(b"/")[1:])):
            raise ValueError("module path must be a canonical absolute path")
        return raw
    if (not isinstance(roots, list) or not 1 <= len(roots) <= 512
            or len(set(roots)) != len(roots) or not isinstance(nonce, bytes) or len(nonce) != 32
            or type(offset) is not int or not 0 < offset <= 64 * 1024 * 1024 or offset % 8):
        raise ValueError("invalid module policy shape")
    paths = [encoded(path) for path in roots]
    guard_path = encoded(guard)
    if not any(guard_path.startswith(path + b"/") for path in paths):
        raise ValueError("guard is outside selected module roots")
    body = guard_path + b"".join(struct.pack("<H", len(path)) + path for path in paths)
    size = 64 + len(body)
    if size > 65536:
        raise ValueError("module policy exceeds bound")
    return (b"PWMOD02\0" + struct.pack("<II", size, len(paths)) + nonce
            + struct.pack("<QH6x", offset, len(guard_path)) + body)


def main():
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--auditor", type=Path, required=True)
    parser.add_argument("--guard", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.output.resolve() in (args.auditor.resolve(), args.guard.resolve()):
        parser.error("ABI output must not replace either module")
    report = describe(args.auditor, args.guard)
    with args.output.open("x", encoding="utf-8") as output:
        output.write(json.dumps(report, indent=2, sort_keys=True) + "\n")


if __name__ == "__main__":
    main()
