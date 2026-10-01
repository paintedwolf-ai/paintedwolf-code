import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import focused_outcomes
import candidate_process
import oracle_process
from grade import GradingUnavailable


class OracleProcessTests(unittest.TestCase):
    def test_preserves_exit_and_both_streams(self):
        result = oracle_process.capture([sys.executable, '-c',
            'import sys; print("answer"); print("diagnostic",file=sys.stderr); sys.exit(7)'])
        self.assertEqual((result.returncode, result.stdout, result.stderr),
                         (7, b'answer\n', b'diagnostic\n'))

    def test_total_output_is_bounded_across_streams(self):
        result = oracle_process.capture([sys.executable, '-c',
            'import os; os.write(1,b"x"*600); os.write(2,b"y"*600)'], output_limit=1000)
        self.assertIsNone(result)

    def test_timeout_stops_owned_process(self):
        with self.assertRaises(subprocess.TimeoutExpired):
            oracle_process.capture([sys.executable, '-c', 'import time; time.sleep(60)'], timeout=.1)

    def test_candidate_deadline_is_a_failed_observation(self):
        program = 'while True: pass'
        self.assertEqual(candidate_process.run(program, timeout=.05), candidate_process.TIMEOUT_EXIT)
        with tempfile.TemporaryDirectory() as temporary, patch.object(focused_outcomes, 'capture') as capture:
            capture.return_value = subprocess.CompletedProcess([], candidate_process.TIMEOUT_EXIT, b'', b'')
            self.assertIsNone(focused_outcomes.execute(pathlib.Path(temporary), program))
            command = capture.call_args.args[0]
            self.assertEqual(command[-2], pathlib.Path(candidate_process.__file__).read_text())
            self.assertEqual(capture.call_args.kwargs['timeout'], 600)

    def test_candidate_supervisor_preserves_observations_and_failure_exit(self):
        supervisor = pathlib.Path(candidate_process.__file__).read_text()
        for program, code, output in [('print("[42]")', 0, '[42]\n'), ('raise SystemExit(7)', 1, ''), ('raise SystemExit(125)', 1, '')]:
            result = subprocess.run([sys.executable, '-I', '-B', '-c', supervisor, program],
                                    capture_output=True, text=True, timeout=15)
            self.assertEqual((result.returncode, result.stdout), (code, output))

    def container_exit(self, code):
        container = 'a'*64
        def run_container(command, **kwargs):
            pathlib.Path(command[command.index('--cidfile')+1]).write_text(container)
            return subprocess.CompletedProcess(command, code, b'', b'candidate diagnostic')
        with tempfile.TemporaryDirectory() as temporary:
            with patch.object(focused_outcomes, 'capture', side_effect=run_container), \
                 patch.object(focused_outcomes.subprocess, 'run') as docker:
                docker.return_value = subprocess.CompletedProcess([], 0, b'', b'')
                try:
                    return focused_outcomes.execute(pathlib.Path(temporary), 'raise SystemExit(125)')
                finally:
                    self.assertEqual(docker.call_args.args[0], ['docker','rm','--force',container])

    def test_candidate_exit_125_is_a_model_outcome(self):
        self.assertIsNone(self.container_exit(candidate_process.run('raise SystemExit(125)')))

    def test_container_launch_failure_is_unmeasured(self):
        with self.assertRaises(GradingUnavailable):
            self.container_exit(125)

    def test_oracle_deadline_is_unmeasured_and_cleans_up_only_owned_container(self):
        container = 'b'*64
        def expire(command, **kwargs):
            pathlib.Path(command[command.index('--cidfile')+1]).write_text(container)
            raise subprocess.TimeoutExpired(command,30)
        with tempfile.TemporaryDirectory() as temporary, \
                patch.object(focused_outcomes, 'capture', side_effect=expire), \
                patch.object(focused_outcomes.subprocess, 'run') as docker:
            with self.assertRaises(GradingUnavailable):
                focused_outcomes.execute(pathlib.Path(temporary), 'pass')
            self.assertEqual(docker.call_args_list[0].args[0], ['docker','rm','--force',container])


class ContainerCleanupTests(unittest.TestCase):
    def test_cleanup_confirms_removal_or_absence(self):
        container = 'a' * 64
        for removed, listed, body, expected in [(0, None, b'', True), (1, 0, b'', True),
                (1, 0, ('"' + container + '"\n').encode(), False), (1, 1, b'', False)]:
            with self.subTest(removed=removed, listed=listed, body=body):
                results = [subprocess.CompletedProcess([], removed, b'', b'')]
                if listed is not None:
                    results.append(subprocess.CompletedProcess([], listed, body, b''))
                with patch.object(oracle_process.subprocess, 'run', side_effect=results) as docker:
                    self.assertEqual(oracle_process.remove_container(container), expected)
                    self.assertEqual(docker.call_args_list[0].args[0], ['docker', 'rm', '--force', container])

    def test_unconfirmed_cleanup_cannot_publish_an_observation(self):
        container = 'c' * 64
        def completed(command, **kwargs):
            self.assertIn('--rm', command)
            pathlib.Path(command[command.index('--cidfile') + 1]).write_text(container)
            return subprocess.CompletedProcess(command, 0, b'[42]', b'')
        with tempfile.TemporaryDirectory() as temporary, patch.object(focused_outcomes, 'capture', side_effect=completed), \
                patch.object(focused_outcomes, 'remove_container', return_value=False):
            with self.assertRaisesRegex(GradingUnavailable, container):
                focused_outcomes.execute(pathlib.Path(temporary), 'print("[42]")')

    def test_cleanup_requires_exact_container_ownership(self):
        with patch.object(oracle_process.subprocess, 'run') as docker:
            with self.assertRaises(ValueError):
                oracle_process.remove_container('all')
            docker.assert_not_called()
