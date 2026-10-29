"""Hosted desktop preparation builds the disposable GNOME account from the selected controller only."""
import json
from pathlib import Path
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.hosted import desktop
from linux_release.hosted.desktop import USER

CONTROLLER = 'a' * 40
SESSION = {'Desktop': 'GNOME', 'Name': 'gnome-xorg', 'Type': 'x11', 'Active': 'yes'}


class Node:
    """One fixed system path with the facts preparation reads from it."""

    def __init__(self, path, *, file=False, uid=0, mode=0o644, present=True):
        self.path, self.file, self.uid, self.mode, self.present = path, file, uid, mode, present
        self.children, self.written, self.appended = {}, None, []

    def child(self, name, **facts):
        self.children[name] = Node(self.path + '/' + name, **facts)
        return self.children[name]

    def __truediv__(self, name):
        node = self
        for part in str(name).split('/'):
            node = node.children.get(part) or Node(node.path + '/' + part, present=False)
        return node

    def __fspath__(self):
        return self.path

    def __str__(self):
        return self.path

    def exists(self):
        return self.present

    def is_symlink(self):
        return False

    def is_file(self):
        return self.present and self.file

    def stat(self):
        return SimpleNamespace(st_mode=(stat.S_IFREG if self.file else stat.S_IFDIR) | self.mode, st_uid=self.uid)

    lstat = stat

    def write_text(self, text):
        self.written = text

    def open(self, mode):
        node = self

        class Stream:
            def __enter__(self):
                return self

            def __exit__(self, *_):
                return False

            def write(self, text):
                node.appended.append(text)
        assert mode == 'a'
        return Stream()


class Host:
    """Patch the hosted machine around one preparation."""

    def __init__(self, *, revision=CONTROLLER, cloned=CONTROLLER, marker_present=False, account_exists=False,
                 session_entry='gnome-xorg', gdm_uid=0, session=SESSION, authority_mode=0o600):
        self.revision, self.cloned, self.marker_present, self.account_exists = revision, cloned, marker_present, account_exists
        self.session_entry, self.gdm_uid, self.session, self.authority_mode = session_entry, gdm_uid, session, authority_mode
        self.calls, self.fresh = [], []

    def run(self, arguments, *, input=None, timeout=1200, cwd=None):
        self.calls.append((arguments, input, cwd))
        if arguments[0] == 'git' and arguments[-2:] == ['rev-parse', 'HEAD']:
            revision = self.cloned if str(self.controller) in arguments else self.revision
            return (revision + '\n').encode()
        if arguments[0] == 'runuser':
            return json.dumps(self.session).encode()
        return b''

    def __enter__(self):
        self.temporary = tempfile.TemporaryDirectory()
        base = Path(self.temporary.name).resolve()
        self.checkout, self.home, self.output = base / 'checkout', base / 'home', base / 'facts.json'
        (self.checkout / 'scripts/linux_release/campaign').mkdir(parents=True)
        (self.checkout / 'scripts/linux_release/campaign/environment.py').write_text('')
        self.home.mkdir()
        self.controller = self.home / 'controller'
        self.account = SimpleNamespace(pw_uid=1001, pw_gid=1001, pw_dir=str(self.home))
        marker = Node('/etc/paintedwolf-qualification.json', file=True, present=self.marker_present)
        sessions = Node('/usr/share/xsessions')
        if self.session_entry:
            sessions.child(self.session_entry + '.desktop', file=True)
        gdm = Node('/etc/gdm3/custom.conf', file=True, uid=self.gdm_uid)
        environment = Node('/etc/environment', file=True)
        run_user = Node('/run/user')
        run_user.child('1001').child('gdm').child('Xauthority', file=True, uid=1001, mode=self.authority_mode)
        self.nodes = {node.path: node for node in (marker, sessions, gdm, environment, run_user)}
        native = desktop.Path
        present, absent = Node('/usr/share/applications'), Node('/usr/share/icons', present=False)
        accounts = [KeyError(USER)] if not self.account_exists else [self.account]
        self.patches = [patch.object(desktop, 'validate_host'), patch.object(desktop, 'release', return_value={'ID': 'ubuntu'}),
                        patch.dict(desktop.os.environ, {'GITHUB_SHA': CONTROLLER, 'GITHUB_RUN_ID': '77'}),
                        patch.object(desktop, 'run', side_effect=self.run),
                        patch.object(desktop, 'Path', side_effect=lambda value='.': self.nodes.get(str(value)) or native(value)),
                        patch.object(desktop.pwd, 'getpwnam', side_effect=[*accounts, self.account]),
                        patch.object(desktop, 'prepare_install_parent', side_effect=lambda path=None: {'path': str(path or desktop.INSTALL_PARENT)}),
                        patch.object(desktop, 'DESKTOP_PARENTS', (present, absent)),
                        patch.object(desktop, 'fresh', side_effect=lambda path, text, mode=0o644: self.fresh.append((str(path), text, mode))),
                        patch.object(desktop.os, 'chown'), patch.object(desktop.os, 'lchown'),
                        patch.object(desktop.time, 'sleep'), patch.object(desktop.time, 'monotonic', side_effect=[0.0, 1.0, 2.0, 500.0, 500.0])]
        self.mocks = [item.start() for item in self.patches]
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()
        self.temporary.cleanup()

    def prepare(self, controller_commit=None):
        return desktop.prepare(self.checkout, self.output, controller_commit)

    def commands(self):
        return [arguments[0] for arguments, _, _ in self.calls]


class PrepareTests(unittest.TestCase):
    def test_the_disposable_account_desktop_session_and_record_come_from_the_selected_controller(self):
        with Host() as host:
            result = host.prepare()
            self.assertEqual(host.commands(), ['git', 'apt-get', 'apt-get', 'useradd', 'chpasswd', 'git', 'git', 'chown', 'systemctl', 'runuser'])
            self.assertEqual(host.calls[1][0], ['apt-get', 'update'])
            self.assertEqual(host.calls[2][0][:4], ['apt-get', 'install', '-y', '--no-install-recommends'])
            self.assertEqual(host.calls[3][0], ['useradd', '--create-home', '--shell', '/bin/bash', USER])
            self.assertTrue(host.calls[4][1].startswith((USER + ':').encode()))
            self.assertEqual(host.calls[5][0][-2:], [str(host.checkout), str(host.controller)])
            self.assertEqual(host.calls[7][0], ['chown', '-R', '1001:1001', str(host.controller)])
            self.assertEqual(host.calls[8][0], ['systemctl', 'restart', 'gdm3'])
            self.assertEqual(host.calls[9][0][:3], ['runuser', '-u', USER])
            self.assertEqual(host.calls[9][2], host.home)
            self.assertTrue((host.home / '.ssh').is_dir())
            self.assertEqual(stat.S_IMODE((host.home / '.ssh').stat().st_mode), 0o700)
            self.assertTrue((host.home / '.Xauthority').is_symlink())
            self.assertEqual(str((host.home / '.Xauthority').readlink()), '/run/user/1001/gdm/Xauthority')
            gdm = host.nodes['/etc/gdm3/custom.conf'].written
            self.assertIn('AutomaticLogin=' + USER + '\n', gdm)
            self.assertIn('DefaultSession=gnome-xorg.desktop', gdm)
            self.assertIn('WaylandEnable=false', gdm)
            self.assertEqual(host.nodes['/etc/environment'].appended, ['\nLIBGL_ALWAYS_SOFTWARE=1\n'])
            written = {path: (text, mode) for path, text, mode in host.fresh}
            self.assertEqual(written['/etc/sudoers.d/paintedwolf-qualification'], (USER + ' ALL=(ALL) NOPASSWD: ALL\n', 0o440))
            self.assertEqual(written['/etc/X11/xorg.conf.d/20-paintedwolf-qualification.conf'][0], desktop.XORG)
            self.assertIn('Session=gnome-xorg\n', written['/var/lib/AccountsService/users/' + USER][0])
            self.assertEqual(written[str(host.home / '.dmrc')][0], '[Desktop]\nSession=gnome-xorg\n')
            self.assertEqual(json.loads(written['/etc/paintedwolf-qualification.json'][0]),
                             {'format_version': 1, 'desktop': 'gnome', 'package': '/opt/painted-wolf-code'})
            self.assertEqual(json.loads(written[str(host.output)][0]), result)
            self.assertEqual((result['controller_commit'], result['session'], result['uid']), (CONTROLLER, SESSION, 1001))
            self.assertEqual(result['controller_checkout'], str(host.controller))
            self.assertEqual(result['install_parent_preparation'], {'path': str(desktop.INSTALL_PARENT)})
            self.assertEqual(result['shared_parent_preparation'], {'path': str(desktop.SHARED_PARENT)})
            self.assertEqual(result['desktop_parent_preparation'], [{'path': '/usr/share/applications'}])
            self.assertIs(result['application_qualified'], False)
            self.assertEqual(host.mocks[9].call_count, 2)
            host.mocks[10].assert_called_once_with(host.home / '.Xauthority', 1001, 1001)

    def test_an_explicit_controller_commit_must_match_the_checkout(self):
        with Host(revision='b' * 40, cloned='b' * 40) as host:
            self.assertEqual(host.prepare('b' * 40)['controller_commit'], 'b' * 40)
            self.assertEqual(host.calls[0][0][-2:], ['rev-parse', 'HEAD'])
        with Host(revision='b' * 40) as host, self.assertRaises(ValueError):
            host.prepare()
        self.assertEqual(host.commands(), ['git'])

    def test_a_claimed_runner_existing_account_or_diverged_clone_is_refused_before_the_desktop(self):
        with Host(marker_present=True) as host, self.assertRaises(ValueError):
            host.prepare()
        self.assertEqual(host.commands(), ['git'])
        with Host(account_exists=True) as host, self.assertRaises(ValueError):
            host.prepare()
        self.assertEqual(host.commands(), ['git'])
        with Host(cloned='c' * 40) as host, self.assertRaises(ValueError):
            host.prepare()
        self.assertNotIn('systemctl', host.commands())
        self.assertEqual(host.fresh, [])

    def test_the_session_entry_and_gdm_configuration_must_belong_to_the_administrator(self):
        with Host(session_entry=None) as host, self.assertRaises(ValueError):
            host.prepare()
        self.assertIsNone(host.nodes['/etc/gdm3/custom.conf'].written)
        with Host(gdm_uid=1000) as host, self.assertRaises(ValueError):
            host.prepare()
        self.assertIsNone(host.nodes['/etc/gdm3/custom.conf'].written)
        self.assertNotIn('systemctl', host.commands())

    def test_a_desktop_that_never_reaches_an_active_x11_session_fails_after_the_deadline(self):
        with Host(session={'Type': 'wayland', 'Active': 'yes'}) as host, self.assertRaises(OSError):
            host.prepare()
        self.assertEqual(host.commands().count('runuser'), 2)
        self.assertEqual(host.fresh[-1][0], str(host.home / '.dmrc'))

    def test_a_shared_display_authority_is_refused(self):
        with Host(authority_mode=0o644) as host, self.assertRaises(ValueError):
            host.prepare()
        self.assertFalse((host.home / '.Xauthority').is_symlink())
        self.assertNotIn('/etc/paintedwolf-qualification.json', [path for path, _, _ in host.fresh])

    def test_a_missing_controller_or_an_existing_output_is_refused_before_any_command(self):
        with Host() as host:
            host.output.write_text('{}')
            with self.assertRaises(ValueError):
                host.prepare()
            self.assertEqual(host.calls, [])
        with Host() as host:
            (host.checkout / 'scripts/linux_release/campaign/environment.py').unlink()
            with self.assertRaises(ValueError):
                host.prepare()
            self.assertEqual(host.calls, [])


class RunTests(unittest.TestCase):
    def test_a_successful_command_returns_its_bounded_output(self):
        result = SimpleNamespace(returncode=0, stdout=b'ok\n', stderr=b'')
        with patch.object(desktop.subprocess, 'run', return_value=result) as command:
            self.assertEqual(desktop.run(['true'], timeout=5, cwd='/tmp'), b'ok\n')
        self.assertEqual((command.call_args.kwargs['timeout'], command.call_args.kwargs['cwd']), (5, '/tmp'))
        self.assertEqual(command.call_args.kwargs['env']['LC_ALL'], 'C.UTF-8')
        with patch.object(desktop.subprocess, 'run', return_value=SimpleNamespace(returncode=0, stdout=b'x' * ((16 << 20) + 1), stderr=b'')):
            with self.assertRaises(OSError):
                desktop.run(['true'])


if __name__ == '__main__':
    unittest.main()
