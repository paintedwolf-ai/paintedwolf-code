"""Execute package removal against a typed kernel-profile/parser fixture."""
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]
PROFILE = 'painted-wolf-code (unconfined)'
OTHER = 'painted-wolf-code-other (enforce)'


class NativeAppArmorRemoval(unittest.TestCase):
    def transaction(self, operation='remove', *, loaded=True, parser=True, read_error=False,
                    parser_failure=False, remains=False):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state = root / 'kernel.json'
            state.write_text(json.dumps({'profiles': [OTHER] + ([PROFILE] if loaded else []),
                'read_error': read_error, 'parser_failure': parser_failure, 'remains': remains, 'calls': []}))
            prefix = f'#!{sys.executable}\n' + '''import json,os,sys
from pathlib import Path
p=Path(os.environ['KERNEL_STATE']);s=json.loads(p.read_text())
'''
            grep = root / 'grep'
            grep.write_text(prefix + '''import re
assert sys.argv[1]=='-Eq' and sys.argv[3]=='/sys/kernel/security/apparmor/profiles'
if s['read_error']: raise SystemExit(2)
raise SystemExit(0 if any(re.search(sys.argv[2],line) for line in s['profiles']) else 1)
''')
            grep.chmod(0o755)
            if parser:
                binary = root / 'apparmor_parser'
                binary.write_text(prefix + '''import re
assert sys.argv[1:]==['--remove','--skip-cache']
policy=sys.stdin.read();match=re.fullmatch(r'profile ([a-zA-Z0-9_-]+) \\{\\}\\n',policy)
assert match,policy
s['calls'].append({'arguments':sys.argv[1:],'policy':policy})
if not s['parser_failure'] and not s['remains']:
 s['profiles']=[line for line in s['profiles'] if line.split(' (',1)[0]!=match[1]]
p.write_text(json.dumps(s));raise SystemExit(1 if s['parser_failure'] else 0)
''')
                binary.chmod(0o755)
            wrapper = '''fixture_test() {
case "$1:$2" in
-d:/sys/kernel/security/apparmor) return 0;;
-d:/run/systemd/system) return 1;;
esac
command [ "$@"
}
alias '['=fixture_test
set -- ''' + shlex.quote(operation) + '\n. ' + shlex.quote(str(ROOT / 'packaging/linux/deb/prerm'))
            result = subprocess.run(['/bin/sh', '-c', wrapper], env={**os.environ,
                'PATH': str(root), 'KERNEL_STATE': str(state)}, capture_output=True, text=True, timeout=15)
            return result, json.loads(state.read_text())

    def test_remove_unloads_only_the_exact_owned_profile(self):
        result, state = self.transaction()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(state['profiles'], [OTHER])
        self.assertEqual(len(state['calls']), 1)
        self.assertEqual(state['calls'][0]['policy'], 'profile painted-wolf-code {}\n')

    def test_already_unloaded_removal_does_not_need_a_parser(self):
        result, state = self.transaction(loaded=False, parser=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(state['profiles'], [OTHER])
        self.assertEqual(state['calls'], [])

    def test_upgrade_and_deconfigure_preserve_the_loaded_profile(self):
        for operation in ('upgrade', 'deconfigure', 'failed-upgrade'):
            with self.subTest(operation=operation):
                result, state = self.transaction(operation)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(PROFILE, state['profiles'])
                self.assertEqual(state['calls'], [])

    def test_unverified_kernel_or_failed_removal_refuses(self):
        for values in ({'parser': False}, {'read_error': True}, {'parser_failure': True}, {'remains': True}):
            with self.subTest(values=values):
                result, state = self.transaction(**values)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(PROFILE, state['profiles'])
                self.assertIn(OTHER, state['profiles'])
