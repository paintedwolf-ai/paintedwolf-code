"""Native process adapters retain typed births, descriptor identities and bounds."""
import ctypes
import errno
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import verification_process as process
from verification_process import darwin, linux


def darwin_library(case):
    birth = darwin.BsdInfo(pid=123, ppid=42, uid=os.getuid(), start_sec=100, start_usec=37)
    descriptor = darwin.VnodeFdInfo()
    descriptor.vnode.stat.dev, descriptor.vnode.stat.ino = 0x80000001, 456
    entries = [darwin.FdInfo(fd=3, type=1), darwin.FdInfo(fd=4, type=2)]
    def pidinfo(pid, flavor, argument, output, size):
        if flavor == 3:
            ctypes.memmove(output, ctypes.byref(birth), ctypes.sizeof(birth))
            return ctypes.sizeof(birth)
        case.assertEqual((pid, flavor, argument), (123, 1, 0))
        for index, entry in enumerate(entries[:size // ctypes.sizeof(darwin.FdInfo)]):
            output[index] = entry
        return min(len(entries) * ctypes.sizeof(darwin.FdInfo), size)
    def fdinfo(pid, fd, flavor, output, size):
        case.assertEqual((pid, fd, flavor, size), (123, 3, 1, ctypes.sizeof(descriptor)))
        ctypes.memmove(output, ctypes.byref(descriptor), size)
        return size
    return SimpleNamespace(proc_pidinfo=Mock(side_effect=pidinfo), proc_pidfdinfo=Mock(side_effect=fdinfo)), birth


class DarwinProcessTests(unittest.TestCase):
    def test_typed_birth_and_file_identity_do_not_depend_on_names(self):
        library, birth = darwin_library(self)
        with patch.object(darwin.c, 'CDLL', return_value=library), \
             patch.object(process, 'identity', return_value='recorded creation'):
            observer = darwin.Observer()
            self.assertEqual(observer.birth(123), {'pid': 123, 'parent_pid': 42, 'uid': os.getuid(),
                'start_time_seconds': 100, 'start_time_microseconds': 37, 'owner_identity': 'recorded creation'})
            self.assertEqual(observer.files(123, 2), [(-2147483647, 456)])
            birth.pid = 124
            with self.assertRaisesRegex(ValueError, 'another process'):
                observer.birth(123)

    def test_bounded_inventory_and_missing_kernel_facts_refuse(self):
        library, _ = darwin_library(self)
        with patch.object(darwin.c, 'CDLL', return_value=library):
            observer = darwin.Observer()
        with self.assertRaisesRegex(ValueError, 'exceeds bound'):
            observer.files(123, 0)
        library.proc_pidinfo.side_effect = None
        for size in (1, 24):
            library.proc_pidinfo.return_value = size
            with self.subTest(size=size), self.assertRaisesRegex(ValueError, 'exceeds bound'):
                observer.files(123, 2 if size == 1 else 1)
        for code in (errno.ESRCH, errno.EPERM, 0):
            ctypes.set_errno(code)
            library.proc_pidinfo.return_value = 0
            with self.subTest(code=code), self.assertRaises(OSError):
                observer.birth(123)
            with self.assertRaises(OSError):
                observer.files(123, 2)

    def test_a_closed_descriptor_is_not_authority_and_other_failures_are_not_hidden(self):
        library, _ = darwin_library(self)
        with patch.object(darwin.c, 'CDLL', return_value=library):
            observer = darwin.Observer()
        library.proc_pidfdinfo.side_effect = None
        library.proc_pidfdinfo.return_value = 0
        for code in (errno.EBADF, errno.ENOENT):
            ctypes.set_errno(code)
            self.assertEqual(observer.files(123, 2), [])
        for code in (errno.ESRCH, errno.EPERM, 0):
            ctypes.set_errno(code)
            with self.subTest(code=code), self.assertRaises(OSError):
                observer.files(123, 2)


class LinuxProcessTests(unittest.TestCase):
    def test_kernel_birth_opened_objects_and_inventory_bound(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            target = root / 'object';target.write_bytes(b'fixture')
            owner = root / '123';owner.mkdir()
            (owner / 'stat').write_text('123 (name with ) delimiters) ' + ' '.join(['S', '42', *['0'] * 17, '345']))
            descriptors = owner / 'fd';descriptors.mkdir()
            (descriptors / '3').symlink_to(target)
            (descriptors / '4').symlink_to(root / 'gone')
            observer = linux.Observer()
            with patch.object(linux, 'Path', side_effect=lambda value: root if value == '/proc' else Path(value)), \
                 patch.object(process, 'identity', return_value='recorded creation'):
                self.assertEqual(observer.birth(123), {'pid': 123, 'parent_pid': 42, 'start_time_ticks': 345,
                    'uid': os.getuid(), 'owner_identity': 'recorded creation'})
                self.assertEqual(observer.files(123, 2), [(target.stat().st_dev, target.stat().st_ino)])
                with self.assertRaisesRegex(ValueError, 'exceeds bound'):
                    observer.files(123, 0)
                (owner / 'stat').write_text('malformed')
                with self.assertRaisesRegex(ValueError, 'birth is unavailable'):
                    observer.birth(123)


class PlatformProcessTests(unittest.TestCase):
    def test_selected_adapter_declares_only_its_actual_dependency(self):
        library, _ = darwin_library(self)
        with patch.object(process, 'sys', SimpleNamespace(platform='darwin')), \
             patch.object(darwin.c, 'CDLL', return_value=library):
            self.assertEqual(process.dependencies(), {'verification_process.darwin': 'scripts/verification_process/darwin.py'})
            self.assertIsInstance(process.observer(), darwin.Observer)
        with patch.object(process, 'sys', SimpleNamespace(platform='linux')):
            self.assertEqual(process.dependencies(), {'verification_process.linux': 'scripts/verification_process/linux.py'})
            self.assertIsInstance(process.observer(), linux.Observer)
        with patch.object(process, 'sys', SimpleNamespace(platform='unsupported')):
            with self.assertRaisesRegex(OSError, 'unavailable on this platform'):
                process.observer()
