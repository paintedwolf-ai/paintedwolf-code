"""Independent-store and explicit authority requirements for the installed SSH case."""
import copy
import errno
import json
import unittest

from scripts.linux_release.installed_cases import nix_remote as remote


class InstalledNixRemoteTests(unittest.TestCase):
    def test_only_explicit_private_protocol_fixture_endpoints_are_admitted(self):
        self.assertEqual(remote.endpoint('ssh-ng://pw-nix-builder@10.0.2.2:39337'), ('10.0.2.2', 39337))
        for value in ['ssh://pw-nix-builder@10.0.2.2:22', 'ssh-ng://root@10.0.2.2:22',
                      'ssh-ng://pw-nix-builder:secret@10.0.2.2:22', 'ssh-ng://pw-nix-builder@8.8.8.8:22',
                      'ssh-ng://pw-nix-builder@10.0.2.2', 'ssh-ng://pw-nix-builder@10.0.2.2:22?ssh-key=/foreign']:
            with self.subTest(value=value), self.assertRaises(ValueError):
                remote.endpoint(value)

    def test_real_remote_output_and_local_absence_are_both_required(self):
        output, token = '/nix/store/' + 'a' * 32 + '-remote', 'c' * 32
        fixture = {'output': output, 'token': token}
        values = [('ping', '{"version":"2.35.3"}'), ('remote-before', json.dumps({output: None})),
                  ('copy', ''), ('build', ''), ('content', token + '\n'),
                  ('remote-after', json.dumps({output: {'narHash': 'sha256-actual'}})),
                  ('local-after', json.dumps({output: None}))]
        steps = [{'id': key, 'exit_code': 0, 'stdout': value} for key, value in values]
        first = {'socket_errno': errno.ENETUNREACH, 'exit_code': 1, 'pid_namespace': 20}
        second = {'socket_errno': 0, 'exit_code': 0, 'pid_namespace': 10, 'stdout': json.dumps(steps)}
        self.assertTrue(remote.successful(first, second, fixture, 10))
        for label, replacement in [('local-after', json.dumps({output: {'narHash': 'sha256-copy'}})),
                                   ('remote-before', json.dumps({output: {'narHash': 'sha256-existing'}})),
                                   ('remote-after', json.dumps({output: None})), ('content', 'wrong')]:
            changed = copy.deepcopy(steps)
            next(row for row in changed if row['id'] == label)['stdout'] = replacement
            self.assertFalse(remote.successful(first, {**second, 'stdout': json.dumps(changed)}, fixture, 10))
        self.assertFalse(remote.successful({**first, 'socket_errno': errno.ECONNREFUSED}, second, fixture, 10))
        self.assertFalse(remote.successful({**first, 'pid_namespace': 10}, second, fixture, 10))
        changed = copy.deepcopy(steps)
        changed[3]['exit_code'] = 1
        self.assertFalse(remote.successful(first, {**second, 'stdout': json.dumps(changed)}, fixture, 10))

    def test_fixed_ssh_probe_preserves_host_key_and_signature_checks(self):
        compile(remote.SCRIPT, '<installed-nix-ssh>', 'exec')
        self.assertIn('StrictHostKeyChecking=yes', remote.SCRIPT)
        self.assertIn('IdentitiesOnly=yes', remote.SCRIPT)
        self.assertNotIn('StrictHostKeyChecking=no', remote.SCRIPT)
        self.assertNotIn('no-check-sigs', remote.SCRIPT)
        self.assertNotIn('require-sigs', remote.SCRIPT)
        self.assertNotIn('trusted-users', remote.SCRIPT)


if __name__ == '__main__':
    unittest.main()
