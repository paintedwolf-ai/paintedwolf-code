"""KScreen observation binds to the active user's KWin process and its one display socket."""
import json
import os
from pathlib import Path
import socket
import stat
import struct
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.campaign import plasma
from linux_release.installed_cases.records import CaseUnavailable

UID = os.getuid()
SESSION = {'Desktop': 'KDE', 'Type': 'wayland', 'Active': 'yes', 'Remote': 'no', 'User': str(UID)}
IMAGE = SimpleNamespace(st_mode=stat.S_IFREG | 0o755, st_uid=0, st_dev=1, st_ino=2, st_size=3, st_mtime_ns=4)


def proc_entry(root: Path, pid: int, executable: Path, uid: int = UID, parent: int = 1):
    directory = root / str(pid)
    directory.mkdir()
    fields = ['S', str(parent)] + ['0'] * 17 + ['4321', '0']
    (directory / 'stat').write_bytes(f'{pid} (kwin) '.encode() + ' '.join(fields).encode() + b'\n')
    (directory / 'status').write_text(f'Name:\tkwin\nUid:\t{uid} {uid} {uid} {uid}\n')
    (directory / 'exe').symlink_to(executable)


class Executable:
    """The resolved exe link with an administrator-owned image identity."""

    image = IMAGE

    def __init__(self, path: Path):
        self.path, self.name = path, path.name

    def stat(self):
        return self.image

    def __str__(self):
        return str(self.path)


class ProcPath(type(Path())):
    def resolve(self, strict=False):
        return Executable(super().resolve(strict=strict))


def proc(root: Path):
    tree = ProcPath(root)
    return patch.object(plasma, 'Path', side_effect=lambda value: tree if value == '/proc' else Path(value))


class KWinProcessTests(unittest.TestCase):
    def test_process_identity_requires_the_named_root_owned_executable_run_by_this_user(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            kwin = root / 'kwin_wayland'
            kwin.write_bytes(b'')
            proc_entry(root, 4242, kwin, parent=4200)
            proc_entry(root, 4243, kwin, uid=UID + 1)
            with proc(root):
                self.assertEqual(plasma.process(4242, {'kwin_wayland'}),
                                 {'pid': 4242, 'parent_pid': 4200, 'start_time_ticks': 4321, 'executable': str(kwin.resolve()),
                                  'executable_identity': [1, 2, 3, 4]})
                with self.assertRaises(ValueError):
                    plasma.process(4242, {'kwin_wayland_wrapper'})
                with self.assertRaises(ValueError):
                    plasma.process(4243, {'kwin_wayland'})
            with proc(root), patch.object(Executable, 'image', SimpleNamespace(**{**vars(IMAGE), 'st_uid': UID})), \
                    self.assertRaises(ValueError):
                plasma.process(4242, {'kwin_wayland'})
            with proc(root), patch.object(Executable, 'image', SimpleNamespace(**{**vars(IMAGE), 'st_mode': stat.S_IFREG | 0o775})), \
                    self.assertRaises(ValueError):
                plasma.process(4242, {'kwin_wayland'})

    def test_owner_binds_the_bus_name_holder_and_its_socket_owning_parent(self):
        reply = {'exit_code': 0, 'stdout': json.dumps({'type': 'u', 'data': [4242]})}
        processes = {4242: {'pid': 4242, 'parent_pid': 4200}, 4200: {'pid': 4200, 'parent_pid': 1}}
        with patch.object(plasma, 'query', return_value=reply) as query, \
                patch.object(plasma, 'process', side_effect=lambda pid, names: (names, processes[pid])[1]) as process:
            self.assertEqual(plasma.owner(SESSION), {'window': processes[4242], 'socket_owner': processes[4200]})
        self.assertEqual(query.call_args.args[0][-3:], ['GetConnectionUnixProcessID', 's', 'org.kde.KWin'])
        self.assertIn('--address=unix:path=/run/user/' + str(UID) + '/bus', query.call_args.args[0])
        self.assertEqual([call.args for call in process.call_args_list],
                         [(4242, {'kwin_wayland'}), (4200, {'kwin_wayland_wrapper'})])
        with patch.object(plasma, 'query', return_value={'exit_code': 1, 'stdout': ''}), self.assertRaises(CaseUnavailable):
            plasma.owner(SESSION)
        with patch.object(plasma, 'query', return_value={'exit_code': 0, 'stdout': json.dumps({'type': 'u', 'data': [1]})}), \
                self.assertRaises(ValueError):
            plasma.owner(SESSION)
        for changed in [dict(SESSION, Desktop='GNOME'), dict(SESSION, Type='x11'), dict(SESSION, Active='no'),
                        dict(SESSION, Remote='yes'), dict(SESSION, User=str(UID + 1))]:
            with self.subTest(session=changed), patch.object(plasma, 'query') as query, self.assertRaises(CaseUnavailable):
                plasma.owner(changed)
            query.assert_not_called()


class Peer:
    """A fake AF_UNIX client whose peer credentials are fixed per socket path."""

    def __init__(self, peers):
        self.peers, self.connected = peers, []

    def __call__(self, family, kind):
        self.family, self.kind = family, kind
        return self

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False

    def settimeout(self, value):
        self.timeout = value

    def connect(self, path):
        self.connected.append(path)
        self.current = path

    def getsockopt(self, level, option, size):
        return struct.pack('iII', *self.peers.get(Path(self.current).name, (0, 0, 0)))


def runtime_sockets(base: Path, names):
    runtime = base / str(UID)
    runtime.mkdir(mode=0o700)
    for name in names:
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as listener:
            listener.bind(str(runtime / name))
        (runtime / name).chmod(0o700)
    return runtime


def display(base: Path, peers):
    fake_socket = SimpleNamespace(AF_UNIX='unix', SOCK_STREAM='stream', SOL_SOCKET='sol', SO_PEERCRED='peercred',
                                  socket=Peer(peers))
    return patch.object(plasma, 'Path', side_effect=lambda value: base if value == '/run/user' else Path(value)), \
        patch.object(plasma, 'socket', fake_socket), fake_socket.socket


class DisplaySocketTests(unittest.TestCase):
    AUTHORITY = {'socket_owner': {'pid': 4200}}

    def test_the_unique_socket_owned_by_the_kwin_parent_is_selected(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            runtime = runtime_sockets(base, ['wayland-0', 'wayland-1', 'notes.txt'])
            peers = {'wayland-0': (99, UID, os.getgid()), 'wayland-1': (4200, UID, os.getgid())}
            paths, sockets, peer = display(base, peers)
            with paths, sockets:
                selected = plasma.display(self.AUTHORITY)
            self.assertEqual((selected['path'], selected['name'], selected['peer']),
                             (str(runtime / 'wayland-1'), 'wayland-1', [4200, UID, os.getgid()]))
            info = (runtime / 'wayland-1').lstat()
            self.assertEqual(selected['identity'], [info.st_dev, info.st_ino, info.st_uid, info.st_mode])
            self.assertEqual(sorted(Path(path).name for path in peer.connected), ['wayland-0', 'wayland-1'])
            self.assertEqual(peer.timeout, 2)

    def test_no_match_two_matches_and_unsafe_runtime_state_are_refused(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            runtime = runtime_sockets(base, ['wayland-0', 'wayland-1'])
            cases = [({'wayland-0': (1, UID, 0), 'wayland-1': (2, UID, 0)}, CaseUnavailable),
                     ({'wayland-0': (4200, UID, os.getgid()), 'wayland-1': (4200, UID, os.getgid())}, CaseUnavailable)]
            for peers, expected in cases:
                paths, sockets, _ = display(base, peers)
                with self.subTest(peers=peers), paths, sockets, self.assertRaises(expected):
                    plasma.display(self.AUTHORITY)
            (runtime / 'wayland-2').write_bytes(b'')
            paths, sockets, _ = display(base, {})
            with paths, sockets, self.assertRaises(ValueError):
                plasma.display(self.AUTHORITY)
            (runtime / 'wayland-2').unlink()
            runtime.chmod(0o755)
            paths, sockets, _ = display(base, {})
            with paths, sockets, self.assertRaises(ValueError):
                plasma.display(self.AUTHORITY)
            runtime.chmod(0o700)
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            runtime_sockets(base, ['wayland-%d' % index for index in range(17)])
            paths, sockets, peer = display(base, {})
            with paths, sockets, self.assertRaises(ValueError):
                plasma.display(self.AUTHORITY)
            self.assertEqual(peer.connected, [])


class ObservationTests(unittest.TestCase):
    def test_output_inventory_and_kscreen_availability_are_required(self):
        for value in ['outputs', {'outputs': []}, {'outputs': [{}] * 17}]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                plasma.configuration(value)
        with patch.object(plasma, 'owner', return_value={'socket_owner': {'pid': 4200}}), \
                patch.object(plasma, 'display', return_value={'name': 'wayland-1'}), \
                patch.object(plasma, 'query', return_value={'exit_code': 1, 'stdout': ''}) as query, \
                self.assertRaises(CaseUnavailable):
            plasma.observe(SESSION)
        self.assertIn('WAYLAND_DISPLAY=wayland-1', query.call_args.args[0])


if __name__ == '__main__':
    unittest.main()
