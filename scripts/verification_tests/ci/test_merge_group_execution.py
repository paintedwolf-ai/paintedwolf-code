"""A merge-group run stops itself once the queue removes its group's branch."""
import base64
import subprocess
import unittest
from unittest.mock import patch

from ci_policy import merge_group

QUEUE_REF = 'refs/heads/gh-readonly-queue/main/pr-7-0123456789abcdef0123456789abcdef01234567'


class MergeGroupTests(unittest.TestCase):
    def test_watch_cancels_once_when_the_branch_is_gone(self):
        answers = iter([True, None, True, False, True])
        polls, cancels, sleeps = [], [], []

        def exists(url, ref):
            polls.append((url, ref))
            return next(answers)

        with patch('builtins.print'):
            merge_group.watch(QUEUE_REF, 'https://github.example/owner/repo', lambda: cancels.append(1),
                              exists=exists, sleep=sleeps.append)
        self.assertEqual(cancels, [1])
        self.assertEqual(polls, [('https://github.example/owner/repo', QUEUE_REF)] * 4)
        self.assertEqual(sleeps, [merge_group.INTERVAL_SECONDS] * 4)

    def test_only_a_missing_ref_reads_as_gone(self):
        for code, state in [(0, True), (2, False), (128, None), (1, None)]:
            with self.subTest(code=code), patch.object(merge_group.subprocess, 'run',
                                                       return_value=subprocess.CompletedProcess([], code)) as run:
                self.assertIs(merge_group.branch_exists('https://example.test/repo', QUEUE_REF), state)
                self.assertEqual(run.call_args.args[0],
                                 ['git', 'ls-remote', '--exit-code', 'https://example.test/repo', QUEUE_REF])
        with patch.object(merge_group.subprocess, 'run', side_effect=subprocess.TimeoutExpired('git', 30)):
            self.assertIsNone(merge_group.branch_exists('https://example.test/repo', QUEUE_REF))

    def test_token_reaches_git_through_the_environment_only(self):
        with patch.object(merge_group.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0)) as run:
            merge_group.branch_exists('https://example.test/repo', QUEUE_REF, 'secret-token')
        self.assertNotIn('secret-token', ' '.join(run.call_args.args[0]))
        environment = run.call_args.kwargs['env']
        credential = base64.b64encode(b'x-access-token:secret-token').decode()
        self.assertEqual((environment['GIT_CONFIG_KEY_0'], environment['GIT_CONFIG_VALUE_0']),
                         ('http.extraHeader', f'AUTHORIZATION: basic {credential}'))
        self.assertEqual(environment['GIT_TERMINAL_PROMPT'], '0')

    def test_main_cancels_its_own_run_with_one_call(self):
        environment = {'GITHUB_REF': QUEUE_REF, 'GITHUB_SERVER_URL': 'https://github.example',
                       'GITHUB_REPOSITORY': 'owner/repo', 'GITHUB_RUN_ID': '42', 'GH_TOKEN': 'token'}
        with patch.dict('os.environ', environment), patch.object(merge_group, 'api') as api, \
                patch.object(merge_group, 'branch_exists', return_value=False) as exists, \
                patch.object(merge_group.time, 'sleep'), patch('builtins.print'):
            merge_group.main()
        exists.assert_called_once_with('https://github.example/owner/repo', QUEUE_REF, 'token')
        api.assert_called_once_with('repos/owner/repo/actions/runs/42/cancel', 'POST')

    def test_main_refuses_a_branch_outside_the_merge_queue(self):
        with patch.dict('os.environ', {'GITHUB_REF': 'refs/heads/main'}), patch.object(merge_group, 'watch') as watch, \
                self.assertRaises(SystemExit):
            merge_group.main()
        watch.assert_not_called()

    def test_queue_certification_validates_inputs_synthesizes_event_and_propagates_failure(self):
        import json
        from pathlib import Path
        import runpy
        from ci_policy import queue_certification as qc
        sha = 'a' * 40
        other = 'b' * 40
        with self.assertRaisesRegex(ValueError, 'cannot combine pull_request'):
            qc.event('owner/repo', QUEUE_REF, sha, '7')
        for missing_ref, missing_sha in (('', sha), (QUEUE_REF, '')):
            with self.assertRaisesRegex(ValueError, 'requires both queue_ref and queue_sha'):
                qc.event('owner/repo', missing_ref, missing_sha)
        with patch.object(qc, 'context', side_effect=ValueError('merge-queue ref no longer identifies this exact commit')):
            with self.assertRaisesRegex(ValueError, 'exact commit'):
                qc.run('owner/repo', QUEUE_REF, sha, 'checker.py')
        captured = {}
        def call(cmd, env=None):
            captured['cmd'] = cmd
            captured['event_name'] = env['GITHUB_EVENT_NAME']
            captured['payload'] = json.loads(Path(env['GITHUB_EVENT_PATH']).read_text())
            return 1
        with patch.object(qc, 'context', return_value=other), patch.object(qc.subprocess, 'call', side_effect=call), \
                patch.dict('os.environ', {'GITHUB_REPOSITORY': 'owner/repo', 'QUEUE_REF': QUEUE_REF, 'QUEUE_SHA': sha, 'PULL_REQUEST': ''}, clear=False), \
                patch('sys.argv', ['queue_certification.py', 'checker.py']), self.assertRaises(SystemExit) as exited:
            runpy.run_module('ci_policy.queue_certification', run_name='__main__')
        self.assertEqual(exited.exception.code, 1)
        self.assertEqual(captured['cmd'][-1], 'checker.py')
        self.assertEqual(captured['event_name'], 'merge_group')
        self.assertEqual(captured['payload'], {'merge_group': {
            'base_ref': 'refs/heads/main', 'head_ref': QUEUE_REF, 'base_sha': other, 'head_sha': sha}})


if __name__ == '__main__':
    unittest.main()
