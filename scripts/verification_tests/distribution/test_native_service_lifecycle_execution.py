"""Execute shipped maintainer scripts against a stateful service-manager fixture."""
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import unittest

from verification_tests.distribution.maintainer_fixture import GENERATIONS, program

ROOT = Path(__file__).resolve().parents[3]
SOCKET = 'paintedwolf-vault-identity.socket'
SERVICE = 'paintedwolf-vault-identity.service'
STATE_DIR = '/var/lib/painted-wolf-code'
SCRIPTS = {
    'deb': {name: name for name in ('preinst', 'postinst', 'prerm', 'postrm')},
    'rpm': {'preinst': 'pre', 'postinst': 'post', 'prerm': 'preun', 'postrm': 'postun'},
}
MANAGER = f'#!{sys.executable}\n' + '''import json,os,sys
from pathlib import Path
p=Path(os.environ['SERVICE_STATE'])
s=json.loads(p.read_text()); a=sys.argv[1:]; s['calls'].append(a)
code=0
if a==['enable','--now','paintedwolf-vault-identity.socket']:
    if s['masked']: code=1
    else: s.update(enabled=True,socket_active=True)
elif a==['disable','--now','paintedwolf-vault-identity.socket']:
    s.update(enabled=False,socket_active=False)
elif a==['stop','paintedwolf-vault-identity.service']:
    s.update(active=False)
elif a==['try-restart','paintedwolf-vault-identity.service']:
    if s['active']: s['restarts']=s.get('restarts',0)+1
elif a!=['daemon-reload']: raise SystemExit('unexpected service operation')
p.write_text(json.dumps(s));raise SystemExit(code)
'''
# Systemd is present; the package state directory resolves inside the fixture.
FIXTURE = '''fixture_path() {
case "$1" in
''' + STATE_DIR + '|' + STATE_DIR + '/*) printf %s "$FIXTURE_STATE${1#' + STATE_DIR + '''}";;
*) printf %s "$1";;
esac
}
fixture_command() {
fixture_name=$1; shift
for fixture_argument do
shift
set -- "$@" "$(fixture_path "$fixture_argument")"
done
command -p "$fixture_name" "$@"
}
fixture_test() {
case "$1:$2" in
-f:/usr/lib/systemd/system/paintedwolf-vault-identity.socket|-d:/run/systemd/system) return 0;;
esac
fixture_command [ "$@"
}
mkdir() { fixture_command mkdir "$@"; }
touch() { fixture_command touch "$@"; }
rm() { fixture_command rm "$@"; }
rmdir() { fixture_command rmdir "$@"; }
alias '['=fixture_test
'''


class NativeServiceLifecycle(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.state_dir = self.root / 'var/lib/painted-wolf-code'
        generation = self.bin / 'pw-generations'
        generation.write_text(f'#!{sys.executable}\n' + '''import json,os,sys
from pathlib import Path
a=sys.argv[1:]
if a not in [['install','--family','deb'],['install','--family','rpm'],['remove']]:
    raise SystemExit('unexpected generation operation')
p=Path(os.environ['SERVICE_STATE']);s=json.loads(p.read_text())
s.setdefault('generation_calls',[]).append(a);p.write_text(json.dumps(s))
''')
        generation.chmod(0o755)
        (self.bin / 'systemctl').write_text(MANAGER)
        for name in ('systemctl', 'update-desktop-database', 'gtk-update-icon-cache', 'apparmor_parser'):
            stub = self.bin / name
            if not stub.exists():
                stub.write_text('#!/bin/sh\nexit 0\n')
            stub.chmod(0o755)

    def service(self, *, enabled=False, active=False, masked=False, socket_active=False):
        self.state = self.root / 'state.json'
        self.state.write_text(json.dumps({'enabled': enabled, 'active': active, 'masked': masked,
                                          'socket_active': socket_active, 'calls': []}))

    def run_script(self, family, phase, *arguments):
        script = ROOT / 'packaging/linux' / family / SCRIPTS[family][phase]
        return self.execute_script(script, *arguments)

    def fixture_program(self, script):
        return program(script.read_text(), self.root, self.bin / 'pw-generations')

    def execute_script(self, script, *arguments):
        source = self.fixture_program(script)
        wrapper = FIXTURE + 'set -- ' + ' '.join(shlex.quote(value) for value in arguments) + '\n' + source
        result = subprocess.run(['/bin/sh', '-c', wrapper], env={**os.environ, 'PATH': str(self.bin),
            'SERVICE_STATE': str(self.state), 'FIXTURE_STATE': str(self.state_dir)},
            capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(self.state.read_text())

    def transaction(self, family, arguments, **service):
        self.service(**service)
        return self.run_script(family, 'postinst', *arguments)

    def deb_install(self, old_version=''):
        self.run_script('deb', 'preinst', 'install', *([old_version] if old_version else []))
        return self.run_script('deb', 'postinst', 'configure', old_version)

    def deb_upgrade(self, old_version):
        self.run_script('deb', 'preinst', 'upgrade', old_version)
        return self.run_script('deb', 'postinst', 'configure', old_version)

    def deb_remove(self):
        self.run_script('deb', 'prerm', 'remove')
        return self.run_script('deb', 'postrm', 'remove')

    def test_first_install_enables_socket_without_unmasking(self):
        for family, arguments in [('deb', ['configure', '']), ('rpm', ['1'])]:
            with self.subTest(family=family):
                installed = self.transaction(family, arguments)
                self.assertTrue(installed['enabled'])
                self.assertTrue(installed['socket_active'])
                self.assertFalse(installed['active'])
                masked = self.transaction(family, arguments, masked=True)
                self.assertTrue(masked['masked'])
                self.assertFalse(masked['enabled'])
                self.assertFalse(masked['active'])
                self.assertFalse(masked['socket_active'])

    def test_upgrades_preserve_enablement_and_restart_only_an_active_service(self):
        for family, arguments in [('deb', ['configure', '1.0.1']), ('rpm', ['2']), ('rpm', ['3'])]:
            for enabled, active, masked in [(False, False, False), (False, False, True),
                                           (True, False, False), (True, True, False),
                                           (False, True, False)]:
                with self.subTest(family=family, enabled=enabled, active=active, masked=masked):
                    result = self.transaction(family, arguments, enabled=enabled, active=active, masked=masked, socket_active=active)
                    self.assertEqual((result['enabled'], result['active'], result['masked']), (enabled, active, masked))
                    self.assertEqual(result['socket_active'], active)
                    self.assertEqual(result.get('restarts', 0), int(active))
                    self.assertNotIn(['enable', '--now', SOCKET], result['calls'])
                    self.assertIn(['try-restart', SERVICE], result['calls'])

    def test_debian_nonconfigure_recovery_does_not_activate_service(self):
        for operation in ('abort-upgrade', 'abort-remove', 'abort-deconfigure'):
            with self.subTest(operation=operation):
                result = self.transaction('deb', [operation])
                self.assertEqual(result['calls'], [['daemon-reload']])
                self.assertFalse(result['active'])

    def test_debian_upgrade_keeps_an_administrator_disabled_or_masked_socket(self):
        for masked in (False, True):
            with self.subTest(masked=masked):
                self.service()
                self.deb_install()
                self.service(masked=masked)
                upgraded = self.deb_upgrade('1.0.1')
                self.assertEqual((upgraded['enabled'], upgraded['socket_active'], upgraded['masked']), (False, False, masked))
                self.assertNotIn(['enable', '--now', SOCKET], upgraded['calls'])
                self.assertFalse(self.state_dir.exists())

    def test_debian_reinstall_after_remove_enables_like_first_install(self):
        self.service()
        self.deb_install()
        removed = self.deb_remove()
        self.assertEqual((removed['enabled'], removed['socket_active']), (False, False))
        reinstalled = self.deb_install('1.0.1')
        self.assertEqual((reinstalled['enabled'], reinstalled['socket_active'], reinstalled['active']), (True, True, False))
        self.assertFalse((self.state_dir / 'reinstall-after-remove').exists())
        self.service()
        upgraded = self.deb_upgrade('1.0.2')
        self.assertFalse(upgraded['enabled'])
        self.assertNotIn(['enable', '--now', SOCKET], upgraded['calls'])

    def test_debian_reinstall_after_remove_does_not_unmask(self):
        self.service(masked=True)
        self.deb_remove()
        reinstalled = self.deb_install('1.0.1')
        self.assertEqual((reinstalled['masked'], reinstalled['enabled'], reinstalled['socket_active']), (True, False, False))
        self.assertFalse((self.state_dir / 'reinstall-after-remove').exists())

    def test_debian_purge_then_install_enables_and_purge_clears_package_state(self):
        self.service()
        self.deb_install()
        self.deb_remove()
        self.run_script('deb', 'preinst', 'install', '1.0.1')
        self.run_script('deb', 'postrm', 'abort-install', '1.0.1')
        self.assertFalse(self.state_dir.exists())
        self.state_dir.mkdir()
        (self.state_dir / 'reinstall-after-remove').touch()
        self.run_script('deb', 'postrm', 'purge')
        self.assertFalse(self.state_dir.exists())
        installed = self.deb_install()
        self.assertEqual((installed['enabled'], installed['socket_active']), (True, True))

    def test_rpm_erase_then_install_enables_and_upgrade_keeps_disabled(self):
        self.service()
        self.run_script('rpm', 'preinst', '1')
        self.run_script('rpm', 'postinst', '1')
        self.run_script('rpm', 'prerm', '0')
        erased = self.run_script('rpm', 'postrm', '0')
        self.assertEqual((erased['enabled'], erased['socket_active']), (False, False))
        self.run_script('rpm', 'preinst', '1')
        installed = self.run_script('rpm', 'postinst', '1')
        self.assertEqual((installed['enabled'], installed['socket_active']), (True, True))
        self.service()
        for phase, argument in (('preinst', '2'), ('postinst', '2'), ('prerm', '1'), ('postrm', '1')):
            upgraded = self.run_script('rpm', phase, argument)
        self.assertEqual((upgraded['enabled'], upgraded['socket_active']), (False, False))
        self.assertNotIn(['enable', '--now', SOCKET], upgraded['calls'])
        self.assertNotIn(['disable', '--now', SOCKET], upgraded['calls'])
        self.assertFalse(self.state_dir.exists())


    def test_generation_installer_runs_only_the_exact_fixture_executable(self):
        self.service()
        script = self.root / 'generation-postinst'
        script.write_text('PW_GENERATIONS=' + GENERATIONS + '\nREINSTALL_MARKER=' + STATE_DIR +
                          '/isolated-marker\nmkdir -p ' + STATE_DIR +
                          '\ntouch "$REINSTALL_MARKER"\n"$PW_GENERATIONS" install --family deb\n')
        self.assertNotIn(GENERATIONS, self.fixture_program(script))
        self.assertIn(str(self.state_dir / 'isolated-marker'), self.fixture_program(script))
        result = self.execute_script(script)
        self.assertEqual(result['generation_calls'], [['install', '--family', 'deb']])
        self.assertTrue((self.state_dir / 'isolated-marker').is_file())

    def test_unbound_host_generation_command_is_rejected_before_execution(self):
        script = self.root / 'unbound-generation'
        script.write_text('"' + GENERATIONS + '" remove\n')
        with self.assertRaisesRegex(ValueError, 'unbound host generation'):
            self.execute_script(script)

    def test_fixture_bindings_refuse_unsafe_paths_or_ambiguous_assignments(self):
        assignment = 'PW_GENERATIONS=' + GENERATIONS + '\n'
        for source in (assignment + assignment, 'PW_GENERATIONS=/some/other/program\n'):
            with self.assertRaisesRegex(ValueError, 'unbound host generation'):
                program(source, self.root, self.bin / 'pw-generations')
        with self.assertRaisesRegex(ValueError, 'shell-safe scratch path'):
            program(assignment, self.root, self.bin / 'unsafe name')
