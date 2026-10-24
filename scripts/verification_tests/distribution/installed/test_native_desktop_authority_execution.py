"""Native desktop authority binds physical images and fixed bus connections."""
import os
from pathlib import Path
import stat
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from scripts.linux_release import installed_gnome as gnome
from scripts.linux_release.installed_cases.logind_context import Reader


def info(*, inode=2, uid=0, mode=stat.S_IFREG | 0o755):
    return SimpleNamespace(st_dev=1, st_ino=inode, st_uid=uid, st_gid=0, st_mode=mode, st_size=3)


class NativeDesktopAuthorityTests(unittest.TestCase):
    def test_selected_image_requires_every_physical_root_controlled_parent(self):
        image = info()
        directory = info(mode=stat.S_IFDIR | 0o755)
        for invalid in (None, info(uid=1000, mode=directory.st_mode),
                        info(mode=stat.S_IFDIR | 0o775), info(mode=stat.S_IFLNK | 0o777)):
            with self.subTest(invalid=invalid):
                chain = [directory, invalid or directory, directory, image]
                with patch.object(Path, 'lstat', side_effect=chain), patch.object(
                        Path, 'resolve', return_value=gnome.IMAGE):
                    if invalid is None:
                        self.assertEqual(gnome.physical_image().st_ino, 2)
                    else:
                        with self.assertRaises(ValueError):
                            gnome.physical_image()

    def owner(self, *, executable=None, names=None, births=None, image_after=None):
        names = iter(names or [':1.2', ':1.2'])
        pids = iter([200, 200])
        def call(path, interface, method, args, signature, **kwargs):
            self.assertTrue(kwargs['user'])
            self.assertEqual(kwargs['destination'], 'org.freedesktop.DBus')
            value = next(names) if method == 'GetNameOwner' else next(pids)
            return SimpleNamespace(unpack=lambda: (value,))
        reader = SimpleNamespace(call=call)
        image = info()
        births = iter(births or ['42', '42'])
        def process_stat(path, **kwargs):
            return executable or image if str(path).endswith('/exe') else info(uid=os.getuid())
        with patch.object(gnome, 'physical_image', side_effect=[image, image_after or image]), patch.object(
                gnome.os, 'open', return_value=99), patch.object(gnome.os, 'fstat', return_value=image), patch.object(
                gnome.os, 'close') as close, patch.object(Path, 'stat', process_stat), patch.object(
                Path, 'read_text', side_effect=lambda *args: '200 (shell) ' + ' '.join(['S', '1', *(['0'] * 17), next(births)])):
            try:
                return gnome.owner(reader)
            finally:
                close.assert_called_once_with(99)

    def test_shell_unique_owner_kernel_birth_and_selected_image_must_all_match(self):
        self.assertEqual(self.owner()['unique_owner'], ':1.2')
        for changes in ({'executable': info(inode=3)}, {'names': [':1.2', ':1.3']},
                        {'births': ['42', '43']}, {'image_after': info(inode=3)}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                self.owner(**changes)

    def test_fixed_bus_address_ignores_ambient_overrides_without_restricting_connect_mode(self):
        reader = Reader.__new__(Reader)
        captured = []
        connection = SimpleNamespace(close_sync=lambda _: None)
        reader.gio = SimpleNamespace(DBusConnectionFlags=SimpleNamespace(
            AUTHENTICATION_CLIENT=1, MESSAGE_BUS_CONNECTION=2), DBusConnection=SimpleNamespace(
                new_for_address_sync=lambda *args: captured.append(args) or connection))
        directory = info(mode=stat.S_IFDIR | 0o755)
        socket = info(mode=stat.S_IFSOCK | 0o777)
        def observed(path):
            return socket if str(path).endswith('system_bus_socket') else directory
        with patch.dict(os.environ, {'DBUS_SYSTEM_BUS_ADDRESS': 'unix:path=/tmp/foreign'}), patch.object(
                Path, 'lstat', observed):
            self.assertIs(reader.connect('/run/dbus/system_bus_socket', 0), connection)
        self.assertEqual(captured[0][0], 'unix:path=/run/dbus/system_bus_socket')

    def test_bus_connection_replacement_and_foreign_parent_refuse(self):
        reader = Reader.__new__(Reader)
        closed = []
        reader.gio = SimpleNamespace(DBusConnectionFlags=SimpleNamespace(
            AUTHENTICATION_CLIENT=1, MESSAGE_BUS_CONNECTION=2), DBusConnection=SimpleNamespace(
                new_for_address_sync=lambda *args: SimpleNamespace(close_sync=lambda _: closed.append(True))))
        directory = info(mode=stat.S_IFDIR | 0o755)
        socket = info(mode=stat.S_IFSOCK | 0o777)
        with patch.object(Path, 'lstat', side_effect=[directory] * 3 + [socket, info(inode=9, mode=socket.st_mode)]):
            with self.assertRaises(ValueError):
                reader.connect('/run/dbus/system_bus_socket', 0)
        self.assertEqual(closed, [True])
        with patch.object(Path, 'lstat', return_value=info(uid=1000, mode=directory.st_mode)):
            with self.assertRaises(ValueError):
                reader.connect('/run/dbus/system_bus_socket', 0)
