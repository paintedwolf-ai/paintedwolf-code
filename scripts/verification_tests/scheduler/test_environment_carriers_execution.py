"""Finding a package's processes by an environment entry, through the macOS process-arguments sysctl."""

import contextlib
import ctypes
from pathlib import Path
import unittest
from unittest import mock

from verification_lifecycle import custody

ENTRY = "PW_TEST_PACKAGE_OWNER=1-abc"


def arguments(*environment):
    """A KERN_PROCARGS2 record: argc, executable path, argv, then the environment."""
    return ((1).to_bytes(4, "little") + b"/bin/sleep\0\0\0sleep\0"
            + b"".join(item.encode() + b"\0" for item in environment))


class FakeLibc:
    """sysctl answers for each pid: a record, or how its lookup fails."""

    def __init__(self, records):
        self.records = records

    def sysctl(self, mib, length, buffer, size, new, new_length):
        record = self.records.get(mib[2])
        if record in ("size-error", "empty") or (record == "read-error" and buffer is not None):
            return 0 if record == "empty" else -1
        if record is None:
            return -1
        if record == "read-error":
            size._obj.value = 64
            return 0
        if buffer is not None:
            ctypes.memmove(buffer, record, len(record))
        size._obj.value = len(record)
        return 0


class DarwinCarriersTests(unittest.TestCase):
    @contextlib.contextmanager
    def darwin(self, libc, snapshot=()):
        with mock.patch.object(Path, "is_file", lambda path: False), \
                mock.patch.object(custody.sys, "platform", "darwin"), \
                mock.patch.object(custody, "_darwin_libc", return_value=libc), \
                mock.patch.object(custody, "_darwin_process_snapshot", return_value={pid: {} for pid in snapshot}):
            yield

    def test_only_a_process_whose_environment_holds_the_entry_carries_it(self):
        libc = FakeLibc({11: arguments("HOME=/x", ENTRY), 12: arguments("HOME=/x", ENTRY + "0"),
                         13: arguments(), 14: "size-error", 15: "empty", 16: "read-error"})
        with self.darwin(libc, snapshot=(11, 12, 13, 14, 15, 16)):
            self.assertEqual(custody.carriers(ENTRY), {11})
            self.assertTrue(custody.carries(11, ENTRY))
            for pid in (12, 13, 14, 15, 16):
                self.assertFalse(custody.carries(pid, ENTRY), pid)

    def test_without_the_process_interfaces_the_search_refuses(self):
        with self.darwin(None):
            with self.assertRaises(OSError):
                custody.carriers(ENTRY)
            self.assertFalse(custody.carries(11, ENTRY))
        with mock.patch.object(Path, "is_file", lambda path: False), \
                mock.patch.object(custody.sys, "platform", "linux"):
            with self.assertRaises(OSError):
                custody.carriers(ENTRY)


if __name__ == "__main__":
    unittest.main()
