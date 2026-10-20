"""Execute shipped maintainer scripts against a stateful service-manager fixture."""
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]
SOCKET = 'paintedwolf-vault-identity.socket'
SERVICE = 'paintedwolf-vault-identity.service'


class NativeServiceLifecycle(unittest.TestCase):
    def transaction(self, family, arguments, *, enabled=False, active=False, masked=False, socket_active=False):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state = root / 'state.json'
            state.write_text(json.dumps({'enabled': enabled, 'active': active, 'masked': masked, 'socket_active': socket_active, 'calls': []}))
            manager = root / 'systemctl'
            manager.write_text(f'#!{sys.executable}\n' + '''import json,os,sys
from pathlib import Path
p=Path(os.environ['SERVICE_STATE'])
s=json.loads(p.read_text()); a=sys.argv[1:]; s['calls'].append(a)
code=0
if a==['enable','--now','paintedwolf-vault-identity.socket']:
    if s['masked']: code=1
    else: s.update(enabled=True,socket_active=True)
elif a==['try-restart','paintedwolf-vault-identity.service']:
    if s['active']: s['restarts']=s.get('restarts',0)+1
elif a!=['daemon-reload']: raise SystemExit('unexpected service operation')
p.write_text(json.dumps(s));raise SystemExit(code)
''')
            manager.chmod(0o755)
            for name in ('update-desktop-database', 'gtk-update-icon-cache', 'apparmor_parser'):
                stub = root / name
                stub.write_text('#!/bin/sh\nexit 0\n')
                stub.chmod(0o755)
            script = ROOT / 'packaging/linux' / family / ('postinst' if family == 'deb' else 'post')
            wrapper = '''fixture_test() {
case "$1:$2" in
-f:/usr/lib/systemd/system/paintedwolf-vault-identity.socket|-d:/run/systemd/system) return 0;;
esac
command [ "$@"
}
alias '['=fixture_test
set -- ''' + ' '.join(shlex.quote(value) for value in arguments) + '\n. ' + shlex.quote(str(script))
            result = subprocess.run(['/bin/sh', '-c', wrapper], env={**os.environ,
                'PATH': str(root), 'SERVICE_STATE': str(state)}, capture_output=True, text=True, timeout=15)
            self.assertEqual(result.returncode, 0, result.stderr)
            return json.loads(state.read_text())

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
