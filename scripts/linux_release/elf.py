"""Read ELF64 load, dynamic-link and symbol-version facts without executing payloads."""

from __future__ import annotations

import mmap
from pathlib import Path
import struct


class ELF64:
    def __init__(self, data):
        self.data = data
        if self.bytes(0, 7) != b"\x7fELF\x02\x01\x01":
            raise ValueError("expected a little-endian ELF64 version 1 object")
        self.machine = self.unpack("H", 18)[0]
        offset = self.unpack("Q", 32)[0]
        size, count = self.unpack("HH", 54)
        if size != 56 or count == 0xFFFF:
            raise ValueError("unsupported ELF program header layout")
        self.segments = [self.unpack("IIQQQQQQ", offset + index * size) for index in range(count)]
        for segment in self.segments:
            self.check_span(segment[2], segment[5])

    def check_span(self, offset: int, size: int):
        if offset < 0 or size < 0 or offset > len(self.data) or size > len(self.data) - offset:
            raise ValueError("ELF table extends outside its object")

    def bytes(self, offset: int, size: int):
        self.check_span(offset, size)
        return self.data[offset:offset + size]

    def unpack(self, shape: str, offset: int) -> tuple:
        return struct.unpack("<" + shape, self.bytes(offset, struct.calcsize("<" + shape)))

    def file_offset(self, address: int, size: int) -> int:
        for segment in self.segments:
            if segment[0] == 1 and segment[3] <= address and address + size <= segment[3] + segment[5]:
                return segment[2] + address - segment[3]
        raise ValueError("ELF dynamic address is not in a file-backed load segment")

    def dynamic(self) -> dict[int, list[int]]:
        segments = [segment for segment in self.segments if segment[0] == 2]
        if not segments:
            return {}
        if len(segments) != 1 or segments[0][5] % 16:
            raise ValueError("invalid ELF dynamic segment")
        segment = segments[0]
        tags = {}
        for offset in range(segment[2], segment[2] + segment[5], 16):
            tag, value = self.unpack("qQ", offset)
            if tag == 0:
                return tags
            tags.setdefault(tag, []).append(value)
        raise ValueError("ELF dynamic segment has no terminator")

    def facts(self) -> dict:
        tags = self.dynamic()
        result = {"machine": self.machine, "needed": [], "runpath": [], "versions": {}}
        if not tags:
            return result
        for tag in (5, 10):
            if len(tags.get(tag, [])) != 1:
                raise ValueError("ELF dynamic string table is missing or ambiguous")
        strings = self.bytes(self.file_offset(tags[5][0], tags[10][0]), tags[10][0])

        def string(offset):
            if offset < 0 or offset >= len(strings):
                raise ValueError("ELF string offset is outside its table")
            end = strings.find(b"\0", offset)
            if end < 0:
                raise ValueError("unterminated ELF dynamic string")
            return strings[offset:end].decode("utf-8", errors="strict")

        result["needed"] = [string(offset) for offset in tags.get(1, [])]
        result["runpath"] = [string(offset) for tag in (15, 29) for offset in tags.get(tag, [])]
        if 0x6FFFFFFE not in tags:
            if tags.get(0x6FFFFFFF, [0]) != [0]:
                raise ValueError("ELF version count has no version table")
            return result
        if len(tags[0x6FFFFFFE]) != 1 or len(tags.get(0x6FFFFFFF, [])) != 1:
            raise ValueError("ELF version table is ambiguous or lacks its count")
        count = tags[0x6FFFFFFF][0]
        if count == 0 or count > len(self.data) // 16:
            raise ValueError("invalid ELF version requirement count")
        address = tags[0x6FFFFFFE][0]
        visited = set()
        for index in range(count):
            if address in visited:
                raise ValueError("cyclic ELF version requirements")
            visited.add(address)
            version, entries, library, auxiliary, following = self.unpack("HHIII", self.file_offset(address, 16))
            if version != 1 or not entries or entries > len(self.data) // 16 or auxiliary < 16:
                raise ValueError("invalid ELF version requirement record")
            name = string(library)
            versions = result["versions"].setdefault(name, [])
            cursor = address + auxiliary
            for number in range(entries):
                _, _, _, requirement, next_aux = self.unpack("IHHII", self.file_offset(cursor, 16))
                versions.append(string(requirement))
                if number + 1 < entries and next_aux < 16:
                    raise ValueError("truncated ELF version auxiliary chain")
                if number + 1 == entries and next_aux != 0:
                    raise ValueError("ELF version auxiliary count disagrees with its chain")
                cursor += next_aux
            if index + 1 < count and following < 16:
                raise ValueError("truncated ELF version requirement chain")
            if index + 1 == count and following != 0:
                raise ValueError("ELF version count disagrees with its chain")
            address += following
        return result


def inspect(path: Path) -> dict | None:
    with path.open("rb") as source:
        if source.read(4) != b"\x7fELF":
            return None
        with mmap.mmap(source.fileno(), 0, access=mmap.ACCESS_READ) as data:
            return ELF64(data).facts()
