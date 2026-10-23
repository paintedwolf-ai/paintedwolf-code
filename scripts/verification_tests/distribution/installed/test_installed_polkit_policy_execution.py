"""The guest driver requires exact policy custody and restoration receipts."""
import copy
import hashlib
import json
import os
from pathlib import Path
import pwd
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

from linux_release.installed_cases import polkit_policy as policy, polkit_rule
from linux_release.installed_cases.records import CaseFailed


class PolicyClientTests(unittest.TestCase):
    def setUp(self):
        self.execution = {'format_version': 1, 'execution_nonce': 'a' * 32,
            'source_commit': 'b' * 40, 'driver_tree': 'c' * 40, 'artifact_id': 'app',
            'environment_id': 'noble', 'variant_id': 'gnome-wayland'}
        binding = {'native_input': {'binding': self.execution}}
        with mock.patch.object(policy, 'require_owned_guest'):
            self.client = policy.PresencePolicy(binding)
        body = polkit_rule.rule_bytes(pwd.getpwuid(os.getuid()).pw_name)
        self.installed = {'phase': 'installed', 'pid': 1234, 'rule': {
            'name': '00-paintedwolf-qualification-' + 'a' * 32 + '.rules',
            'action_id': polkit_rule.ACTION, 'before': {}, 'object': {
                'device': 1, 'inode': 2, 'uid': 0, 'gid': 0, 'mode': 0o100644,
                'links': 1, 'bytes': len(body), 'mtime_ns': 1, 'ctime_ns': 1,
                'sha256': hashlib.sha256(body).hexdigest()}}}
        self.restored = {'phase': 'restored', 'shared_policy_unchanged': True,
            'rule': {'created': True, 'removed': True, 'restored': True, 'after': {}}}
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)

    def child(self, installed=None, restored=None):
        helper = self.directory / 'custodian.py'
        received = self.directory / 'received'
        helper.write_text('import json,os,sys\nfrom pathlib import Path\n'
            'installed=json.loads(sys.argv[1]);installed["pid"]=os.getpid()\n'
            'print(json.dumps(installed),flush=True)\n'
            'request=sys.stdin.buffer.readline(16)\n'
            'Path(sys.argv[3]).write_bytes(request)\n'
            'print(sys.argv[2],flush=True)\n'
            'raise SystemExit(0 if request==b"release\\n" else 4)\n')
        run = subprocess.Popen
        def start(arguments, **options):
            self.assertEqual(arguments[:5], ['sudo', '-n', '--', '/usr/bin/python3', '-I'])
            self.assertEqual(arguments[-1], self.execution['execution_nonce'])
            self.assertTrue(Path(arguments[-2]).samefile(polkit_rule.__file__))
            return run([sys.executable, str(helper), json.dumps(installed or self.installed),
                        json.dumps(restored or self.restored), str(received)], **options)
        return mock.patch.object(policy.subprocess, 'Popen', side_effect=start), received

    def test_owned_private_pipe_releases_and_attests_both_policy_inventories(self):
        patched, received = self.child()
        with patched:
            with self.client as held:
                self.assertTrue(held.observations['installed'])
                self.assertFalse(held.observations['restored'])
            self.assertTrue(held.observations['restored'])
        self.assertEqual(received.read_bytes(), b'release\n')
        self.assertEqual(self.client.process.poll(), 0)

    def test_case_failure_still_releases_and_retains_restoration(self):
        patched, received = self.child()
        failure = ValueError('fixture interaction failed')
        with patched:
            with self.assertRaises(ValueError) as raised:
                with self.client:
                    raise failure
        self.assertIs(raised.exception, failure)
        self.assertTrue(failure.observations['presence_policy']['restored'])
        self.assertEqual(received.read_bytes(), b'release\n')

    def test_unverified_restoration_overrides_a_completed_ui_step(self):
        changed = copy.deepcopy(self.restored)
        changed['shared_policy_unchanged'] = False
        patched, _ = self.child(restored=changed)
        with patched:
            with self.assertRaises(CaseFailed) as raised:
                with self.client:
                    pass
        facts = raised.exception.observations['presence_policy']
        self.assertTrue(facts['recovery_required'])
        self.assertFalse(facts['restored'])
        self.assertEqual(facts['restoration'], changed)

    def test_bad_initial_receipt_cannot_be_accepted_even_if_cleanup_returns(self):
        changed = copy.deepcopy(self.installed)
        changed['rule']['action_id'] = 'another.action'
        patched, received = self.child(installed=changed)
        with patched:
            with self.assertRaises(CaseFailed):
                with self.client:
                    self.fail('unbound policy receipt entered the case')
        self.assertEqual(received.read_bytes(), b'release\n')

    def test_rule_facts_require_exact_identity_types_mode_scope_and_contents(self):
        self.client._installed(self.installed)
        for key, value in [('uid', False), ('uid', 1000), ('mode', 0o100666),
                           ('inode', 0), ('links', 2), ('sha256', '0' * 64)]:
            changed = copy.deepcopy(self.installed)
            changed['rule']['object'][key] = value
            with self.assertRaises(ValueError):
                self.client._installed(changed)
        changed = copy.deepcopy(self.installed)
        changed['rule']['name'] = '00-paintedwolf-qualification-' + 'd' * 32 + '.rules'
        with self.assertRaises(ValueError):
            self.client._installed(changed)

    def test_newline_does_not_bypass_the_receipt_size_bound(self):
        self.client.pending = b'x' * ((1 << 20) + 1) + b'\n'
        with self.assertRaises(ValueError):
            self.client._read()

    def test_owned_guest_and_exact_input_binding_are_required_before_sudo(self):
        with mock.patch.object(policy, 'require_owned_guest', side_effect=ValueError('wrong guest')):
            with self.assertRaises(ValueError):
                policy.PresencePolicy({})
        with mock.patch.object(policy, 'require_owned_guest'):
            with self.assertRaises(ValueError):
                policy.PresencePolicy({'native_input': {'binding': {'format_version': 1}}})
