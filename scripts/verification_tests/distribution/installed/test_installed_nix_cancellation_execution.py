"""Actual Nix cancellation requires both client stop and daemon builder disappearance."""
import unittest

from scripts.linux_release.installed_cases.nix_cancellation import stopped


class InstalledNixCancellationTests(unittest.TestCase):
    def test_successful_invocation_or_client_exit_does_not_qualify_builder_cancellation(self):
        before = {'owned': [{'pid': 42, 'start_ticks': '99'}]}
        process = {'running': False, 'exit_code': -15}
        self.assertTrue(stopped(process, before, {'live': []}))
        for changed in [{'running': True}, {'exit_code': 0}, {'exit_code': None}]:
            self.assertFalse(stopped({**process, **changed}, before, {'live': []}))
        self.assertFalse(stopped(process, {'owned': []}, {'live': []}))
        self.assertFalse(stopped(process, before, {'live': before['owned']}))


if __name__ == '__main__':
    unittest.main()
