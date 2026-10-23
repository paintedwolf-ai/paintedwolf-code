"""The producer refuses ambiguous or unbounded module-authority bytes."""
import hashlib
import struct
import unittest

from linux_release.module_integrity import inventory as INVENTORY


class ModuleInventoryTests(unittest.TestCase):
    def setUp(self):
        self.entries = [
            {"path": "/usr/lib/z.so", "size": 6, "sha256": hashlib.sha256(b"module").hexdigest()},
            {"path": "/usr/lib/a.so", "size": 3, "sha256": hashlib.sha256(b"elf").hexdigest()},
        ]

    def encoded(self, entries=None):
        return INVENTORY.encode(self.entries if entries is None else entries,
                                "/usr/lib/pw-guard.so", 16000, 17000, "x86_64")

    def test_inventory_is_order_independent_and_binds_exact_bytes(self):
        raw = self.encoded()
        self.assertEqual(raw, self.encoded(list(reversed(self.entries))))
        selected = INVENTORY.decode(raw)
        self.assertEqual(selected["entries"], sorted(self.entries, key=lambda row: row["path"]))
        self.assertEqual(selected["sha256"], hashlib.sha256(raw).hexdigest())
        self.assertEqual(selected["guard_inventory_offset"], 17000)

    def test_refuses_alias_ambiguity_and_anchor_as_ordinary_module(self):
        for alias in ("relative.so", "/usr//lib/a.so", "/usr/lib/../a.so", "/usr/lib/a.so/",
                      "/usr/lib/\0a.so", "/usr/lib/pw-guard.so"):
            with self.subTest(alias=alias), self.assertRaises(ValueError):
                self.encoded([dict(self.entries[0], path=alias)])
        with self.assertRaises(ValueError):
            self.encoded([self.entries[0], self.entries[0]])

    def test_refuses_noncanonical_digest_size_and_unknown_fields(self):
        for changes in ({"sha256": "A" * 64}, {"sha256": "0" * 63}, {"size": True},
                        {"size": 0}, {"size": 1 << 63}, {"extra": 1}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                self.encoded([dict(self.entries[0], **changes)])

    def test_decoder_refuses_unknown_header_padding_truncation_and_trailing_bytes(self):
        raw = self.encoded()
        cases = [raw[:8], raw[:-1], raw + b"x"]
        for offset in (0, 8, 12, 36):
            altered = bytearray(raw)
            altered[offset] ^= 1
            cases.append(bytes(altered))
        for candidate in cases:
            with self.subTest(length=len(candidate)), self.assertRaises(ValueError):
                INVENTORY.decode(candidate)

    def test_decoder_refuses_duplicate_and_unsorted_records(self):
        raw = self.encoded()
        start = INVENTORY.HEADER.size + len("/usr/lib/pw-guard.so")
        length = INVENTORY.ENTRY.size + len("/usr/lib/a.so")
        for body in (raw[start + length:] + raw[start:start + length],
                     raw[start:start + length] * 2):
            candidate = bytearray(raw[:start] + body)
            struct.pack_into("<I", candidate, 8, len(candidate))
            with self.assertRaises(ValueError):
                INVENTORY.decode(bytes(candidate))

    def test_capacity_is_enforced_before_embedding(self):
        entries = [dict(self.entries[0], path="/" + str(i) + "/" + "x" * 4000)
                   for i in range(1100)]
        with self.assertRaises(ValueError):
            self.encoded(entries)
