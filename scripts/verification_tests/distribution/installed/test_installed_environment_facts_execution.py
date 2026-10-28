"""Environment facts come from actual release, login manager, package and repository observations."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.campaign import environment

RELEASE = 'NAME="Ubuntu"\n# comment\n\nID=ubuntu\nVERSION_ID="24.04"\nPRETTY_NAME=\'Ubuntu 24.04\'\nEMPTY=\n'


def mapped(root: Path):
    """Route the module's absolute path literals under a scratch root."""
    native = environment.Path
    return patch.object(environment, 'Path', side_effect=lambda value='.': native(root) / str(value).lstrip('/'))


class QueryTests(unittest.TestCase):
    def test_unavailable_or_slow_tools_are_recorded_and_output_is_bounded(self):
        for error, name in [(OSError('missing'), 'OSError'), (subprocess.TimeoutExpired('tool', 30), 'TimeoutExpired')]:
            with self.subTest(name=name), patch.object(environment.subprocess, 'run', side_effect=error):
                self.assertEqual(environment.query(['tool']), {'arguments': ['tool'], 'exit_code': None, 'error': name})
        result = SimpleNamespace(returncode=3, stdout=b'out\xff', stderr=b'err')
        with patch.object(environment.subprocess, 'run', return_value=result) as run:
            self.assertEqual(environment.query(['tool', 'x']),
                             {'arguments': ['tool', 'x'], 'exit_code': 3, 'stdout': 'out�', 'stderr': 'err'})
            self.assertEqual(run.call_args.kwargs['env']['LC_ALL'], 'C')
            self.assertIs(run.call_args.kwargs['stdin'], subprocess.DEVNULL)
        with patch.object(environment.subprocess, 'run', return_value=SimpleNamespace(returncode=0, stdout=b'x' * ((16 << 20) + 1), stderr=b'')):
            with self.assertRaises(ValueError):
                environment.query(['tool'])

    def test_properties_and_release_files_are_scalar_and_unique(self):
        self.assertEqual(environment.properties('A=1\nB=x=y\n'), {'A': '1', 'B': 'x=y'})
        for text in ['A=1\nA=2\n', 'A\n']:
            with self.subTest(text=text), self.assertRaises(ValueError):
                environment.properties(text)
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'os-release'
            path.write_text(RELEASE)
            self.assertEqual(environment.release(path), {'NAME': 'Ubuntu', 'ID': 'ubuntu', 'VERSION_ID': '24.04',
                                                         'PRETTY_NAME': 'Ubuntu 24.04', 'EMPTY': ''})
            for text in ['ID=ubuntu\nID=debian\n', 'ID=ubuntu\nNAME=two words\n', 'NAME=none\n', 'ID=ubuntu\nBROKEN\n']:
                path.write_text(text)
                with self.subTest(text=text), self.assertRaises(ValueError):
                    environment.release(path)


class SessionTests(unittest.TestCase):
    def listing(self, rows, details):
        answers = [{'exit_code': 0, 'stdout': '\n'.join(rows) + '\n\n'}]
        answers += [{'exit_code': 0, 'stdout': detail} if isinstance(detail, str) else detail for detail in details]
        return patch.object(environment, 'query', side_effect=answers)

    def test_the_one_active_local_graphical_session_of_the_account_is_selected(self):
        own = 'User=1000\nActive=yes\nRemote=no\nType=x11\nDesktop=ubuntu:GNOME\nDisplay=:0\n'
        others = ['User=1001\nActive=yes\nRemote=no\nType=wayland\nDesktop=KDE\n',
                  'User=1000\nActive=no\nRemote=no\nType=wayland\nDesktop=KDE\n',
                  'User=1000\nActive=yes\nRemote=yes\nType=x11\nDesktop=KDE\n',
                  'User=1000\nActive=yes\nRemote=no\nType=tty\nDesktop=\n']
        with patch.object(environment.os, 'getuid', return_value=1000), patch.object(environment, 'gnome_owner') as owner, \
                self.listing(['1 1000 tester seat0', '2 1001 other seat0', '3 1000 tester', '4 1000 tester', '5 1000 tester'],
                             [own, *others]) as query:
            session = environment.graphical_session()
        self.assertEqual(session['id'], '1')
        self.assertEqual(session['Desktop'], 'ubuntu:GNOME')
        self.assertEqual(session['Display'], ':0')
        owner.assert_not_called()
        self.assertEqual(query.call_args_list[1].args[0][:3], ['loginctl', 'show-session', '1'])

    def test_missing_ambiguous_or_undescribed_sessions_are_refused(self):
        own = 'User=1000\nActive=yes\nRemote=no\nType=x11\nDesktop=GNOME\n'
        refusals = [patch.object(environment, 'query', return_value={'exit_code': 1, 'stdout': ''}),
                    self.listing(['bad;id 1000 tester'], []),
                    self.listing(['1 1000 tester'], [{'exit_code': 1, 'stdout': ''}]),
                    self.listing(['1 1000 tester', '2 1000 tester'], [own, own]),
                    self.listing(['1 1001 other'], ['User=1001\nActive=yes\nRemote=no\nType=x11\nDesktop=GNOME\n'])]
        for index, query in enumerate(refusals):
            with self.subTest(index=index), patch.object(environment.os, 'getuid', return_value=1000), query, \
                    self.assertRaises(ValueError):
                environment.graphical_session()


class GnomeOwnerTests(unittest.TestCase):
    def tree(self, root: Path, pid=2000, name='gnome-shell', ticks='77'):
        process = root / 'proc' / str(pid)
        process.mkdir(parents=True)
        (process / 'stat').write_text(f'{pid} ({name}) S ' + ' '.join(['0'] * 18 + [ticks, '0']) + '\n')
        executable = root / 'usr/bin' / name
        executable.parent.mkdir(parents=True, exist_ok=True)
        executable.write_text('shell')
        (process / 'exe').symlink_to(executable)
        return executable

    def root_shell(self):
        real = Path.stat

        def fake(self, **kwargs):
            info = real(self, **kwargs)
            if self.name == 'gnome-shell':
                return SimpleNamespace(st_uid=0, st_mode=info.st_mode & ~0o022, st_dev=info.st_dev, st_ino=info.st_ino)
            return info
        return patch.object(Path, 'stat', fake)

    def test_the_bus_owner_is_bound_to_a_live_root_installed_shell_image(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            executable = self.tree(root)
            response = {'exit_code': 0, 'stdout': json.dumps({'type': 'u', 'data': [2000]})}
            with mapped(root), self.root_shell(), patch.object(environment, 'query', return_value=response) as query, \
                    patch.object(environment.os, 'getuid', return_value=os.getuid()):
                owner = environment.gnome_owner()
            self.assertEqual(owner['pid'], 2000)
            self.assertEqual(owner['start_time_ticks'], 77)
            self.assertEqual(owner['executable'], str(executable))
            self.assertEqual(owner['bus_name'], 'org.gnome.Shell')
            self.assertEqual(query.call_count, 2)
            self.assertIn('org.gnome.Shell', query.call_args.args[0])

    def test_unowned_untyped_dead_or_changed_owners_are_refused(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            self.tree(root)
            good = {'exit_code': 0, 'stdout': json.dumps({'type': 'u', 'data': [2000]})}
            answers = [[{'exit_code': 1, 'stdout': ''}],
                       [{'exit_code': 0, 'stdout': json.dumps({'type': 'u', 'data': ['2000']})}],
                       [{'exit_code': 0, 'stdout': json.dumps({'type': 'u', 'data': [1]})}],
                       [good, {'exit_code': 0, 'stdout': json.dumps({'type': 'u', 'data': [2001]})}]]
            for index, responses in enumerate(answers):
                with self.subTest(index=index), mapped(root), self.root_shell(), \
                        patch.object(environment, 'query', side_effect=responses), \
                        patch.object(environment.os, 'getuid', return_value=os.getuid()), self.assertRaises(ValueError):
                    environment.gnome_owner()
            with mapped(root), patch.object(environment, 'query', return_value=good), \
                    patch.object(environment.os, 'getuid', return_value=os.getuid()), self.assertRaises(ValueError):
                environment.gnome_owner()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            self.tree(root, name='other-shell')
            with mapped(root), patch.object(environment, 'query', return_value=good), \
                    patch.object(environment.os, 'getuid', return_value=os.getuid()), self.assertRaises(ValueError):
                environment.gnome_owner()


class InventoryAndRepositoryTests(unittest.TestCase):
    def test_each_package_family_uses_its_native_inventory_tool(self):
        self.assertEqual(environment.package_inventory_arguments('deb')[0], 'dpkg-query')
        self.assertEqual(environment.package_inventory_arguments('rpm')[0], 'rpm')
        self.assertEqual(environment.package_inventory_arguments('pacman'), ['pacman', '-Q'])
        self.assertEqual(environment.package_inventory_arguments('nix')[0], 'nix-store')
        with patch.object(environment, 'query', return_value={'exit_code': 0}) as query:
            self.assertEqual(environment.package_inventory('pacman'), {'exit_code': 0})
            query.assert_called_once_with(['pacman', '-Q'])

    def test_optional_security_files_and_repository_configuration_are_hashed_in_place(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            self.assertIsNone(environment.read_optional(root / 'absent'))
            (root / 'enabled').write_text('Y\n')
            self.assertEqual(environment.read_optional(root / 'enabled'), 'Y')
            sources = root / 'etc/apt/sources.list.d'
            sources.mkdir(parents=True)
            (root / 'etc/apt/sources.list').write_text('deb http://archive.invalid noble main\n')
            (sources / 'extra.sources').write_text('Types: deb\n')
            (sources / 'nested').mkdir()
            (sources / 'nested/more.list').write_text('deb more\n')
            with mapped(root):
                records = environment.repository_files('deb')
            self.assertEqual(set(records), {str(root / 'etc/apt/sources.list'), str(sources / 'extra.sources'),
                                            str(sources / 'nested/more.list')})
            self.assertEqual(records[str(sources / 'extra.sources')]['bytes'], 11)
            (root / 'etc/nix').mkdir(parents=True)
            (root / 'etc/nix/nix.conf').write_text('experimental-features = nix-command\n')
            (root / 'run').mkdir()
            (root / 'run/current-system').symlink_to(root / 'etc/nix')
            with mapped(root):
                records = environment.repository_files('nix')
            self.assertEqual(records[str(root / 'run/current-system')], {'link_target': str(root / 'etc/nix')})
            self.assertEqual(records[str(root / 'etc/nix/nix.conf')]['bytes'], 36)
            (root / 'etc/pacman.conf').write_bytes(b'x' * ((1 << 20) + 1))
            with mapped(root), self.assertRaises(ValueError):
                environment.repository_files('pacman')


class CollectTests(unittest.TestCase):
    def binding(self, kind='deb'):
        return {'artifact': {'installation': 'deb', 'identity': {'kind': kind}}, 'payload': {'nix_client': 'x'},
                'source_commit': 'a' * 40}

    def facts(self, environment_row, guest, protocol, session, inventory=None, release=None):
        inventory = {'exit_code': 0, 'stdout': 'libgtk-4-1\t4.14\nvim\t9\n', 'stderr': ''} if inventory is None else inventory
        release = {'ID': 'ubuntu', 'VERSION_ID': '24.04'} if release is None else release
        queries = {'getconf': {'exit_code': 0, 'stdout': 'glibc 2.39\n'}, 'loginctl': {'exit_code': 0, 'stdout': 'systemd 255\n'}}
        with patch.object(environment, 'release', return_value=release), \
                patch.object(environment, 'graphical_session', return_value=dict(session)), \
                patch.object(environment, 'package_inventory', return_value=inventory) as packages, \
                patch.object(environment, 'query', side_effect=lambda arguments: queries[arguments[0]]), \
                patch.object(environment, 'read_optional', side_effect=['Y', None]), \
                patch.object(environment, 'repository_files', return_value={'/etc/apt/sources.list': {'bytes': 1}}), \
                patch.object(environment.platform, 'uname', return_value=SimpleNamespace(_asdict=lambda: {'system': 'Linux'})):
            result = environment.collect(environment_row, self.binding(environment_row.get('kind', 'deb')), guest, protocol)
        return result, packages

    def test_facts_bind_the_selected_row_to_the_observed_distribution_desktop_and_packages(self):
        row = {'distribution': 'Ubuntu', 'desktop': 'GNOME'}
        session = {'id': '1', 'Type': 'x11', 'Desktop': 'ubuntu:GNOME'}
        (facts, observations), packages = self.facts(row, {'desktop': 'gnome'}, 'x11', session)
        packages.assert_called_once_with('deb')
        self.assertEqual(json.loads(facts['distribution-and-release']), {'ID': 'ubuntu', 'VERSION_ID': '24.04'})
        self.assertEqual(json.loads(facts['gtk-webkitgtk-and-graphics-driver'])['runtime_packages'], ['libgtk-4-1\t4.14'])
        self.assertEqual(json.loads(facts['desktop-compositor-display-protocol']), session)
        self.assertEqual(json.loads(facts['security-policy']), {'apparmor': 'Y', 'selinux': None})
        self.assertEqual(facts['nix-version-and-locks-when-applicable'], 'not-applicable')
        self.assertEqual(json.loads(facts['source-commit-and-final-package-or-closure-identity'])['source_commit'], 'a' * 40)
        self.assertEqual(observations['kernel'], {'system': 'Linux'})
        self.assertEqual(observations['libc']['stdout'], 'glibc 2.39\n')
        self.assertEqual(observations['repositories'], {'/etc/apt/sources.list': {'bytes': 1}})
        row = {'distribution': 'NixOS', 'desktop': 'Plasma', 'kind': 'nix-closure'}
        session = {'id': '1', 'Type': 'wayland', 'Desktop': 'KDE'}
        (facts, _), packages = self.facts(row, {'desktop': 'plasma'}, 'wayland', session, release={'ID': 'nixos'})
        packages.assert_called_once_with('nix')
        self.assertEqual(json.loads(facts['nix-version-and-locks-when-applicable']), {'nix_client': 'x'})

    def test_mismatched_rows_or_unrecorded_inventories_are_refused(self):
        row = {'distribution': 'Ubuntu', 'desktop': 'GNOME'}
        session = {'id': '1', 'Type': 'x11', 'Desktop': 'ubuntu:GNOME'}
        refusals = [dict(release={'ID': 'fedora'}), dict(guest={'desktop': 'plasma'}), dict(protocol='wayland'),
                    dict(session=dict(session, Desktop='KDE')), dict(inventory={'exit_code': 1, 'stdout': ''}),
                    dict(inventory={'exit_code': 0, 'stdout': 'libgtk\t1\n' * 2001})]
        for changed in refusals:
            arguments = dict(environment_row=row, guest={'desktop': 'gnome'}, protocol='x11', session=session)
            arguments.update(changed)
            with self.subTest(**changed), self.assertRaises(ValueError):
                self.facts(**arguments)


if __name__ == '__main__':
    unittest.main()
