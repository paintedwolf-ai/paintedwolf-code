"""Kernel smoke cases run their fixture through the installed command and read one bounded witness."""
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import kernel

BINDING = {'binding': True}


class Turn:
    """Patch the installed app, provider and turn; the witness lands in a scratch workspace."""

    def __init__(self, witness=None, raw: bytes | None = None, invoked=True, observations=None):
        self.witness, self.raw, self.invoked = witness, raw, invoked
        self.app = MagicMock()
        self.app.observations = {'capture_complete': True} if observations is None else observations
        self.app.streams.return_value = {'stdout': 'engine'}
        self.turn = MagicMock()
        self.turn.observations = {'turn': 'facts'}
        self.turn.wait.return_value = [{'tool': 'command'}]
        self.provider = MagicMock()
        self.provider.observations = {'provider': 'facts'}
        self.written = {}

    def tool_turn(self, app, provider, project):
        self.project = Path(project)
        self.written = {item.name: item.read_text() for item in self.project.iterdir() if item.is_file()}
        self.workspace = tempfile.TemporaryDirectory()
        self.turn.workspace = Path(self.workspace.name)
        if self.raw is not None:
            (self.turn.workspace / 'qualification-observation.json').write_bytes(self.raw)
        elif self.witness is not None:
            (self.turn.workspace / 'qualification-observation.json').write_text(json.dumps(self.witness))
        manager = MagicMock()
        manager.__enter__.return_value = self.turn
        return manager

    def __enter__(self):
        app_manager = MagicMock()
        app_manager.__enter__.return_value = self.app
        provider_manager = MagicMock()
        provider_manager.__enter__.return_value = self.provider
        self.patches = [patch.object(kernel, 'InstalledApp', MagicMock(return_value=app_manager)),
                        patch.object(kernel, 'ProviderFixture', MagicMock(return_value=provider_manager)),
                        patch.object(kernel, 'ToolTurn', side_effect=self.tool_turn),
                        patch.object(kernel, 'successful_invocation', return_value=self.invoked)]
        self.mocks = [item.start() for item in self.patches]
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()
        if hasattr(self, 'workspace'):
            self.workspace.cleanup()

    def command(self) -> str:
        return self.mocks[1].call_args.args[0][0]['arguments']['command']


class RunScriptTests(unittest.TestCase):
    def test_fixture_is_written_and_the_bounded_witness_becomes_the_observation(self):
        with Turn(witness={'child_reaped': True}) as scenario:
            invoked, facts, streams = kernel.run_script(BINDING, kernel.NAMESPACE, ['one', 'two words'])
        self.assertTrue(invoked)
        self.assertEqual(streams, {'stdout': 'engine'})
        self.assertEqual(scenario.written, {'kernel.py': kernel.NAMESPACE})
        self.assertTrue(scenario.command().endswith("kernel.py one 'two words'"))
        self.assertEqual(facts['kernel_observation'], {'child_reaped': True})
        self.assertEqual((facts['turn'], facts['provider'], facts['app']),
                         ({'turn': 'facts'}, {'provider': 'facts'}, {'capture_complete': True}))
        self.assertEqual(facts['fixture_sha256'], hashlib.sha256(kernel.NAMESPACE.encode()).hexdigest())
        scenario.mocks[3].assert_called_once_with([{'tool': 'command'}])
        self.assertFalse(scenario.project.exists())

    def test_missing_or_oversized_witnesses_observe_nothing_and_prose_is_refused(self):
        with Turn(invoked=False):
            invoked, facts, _ = kernel.run_script(BINDING, kernel.SHARED_MEMORY, [])
        self.assertFalse(invoked)
        self.assertEqual(facts['kernel_observation'], {})
        with Turn(raw=b'{"filler": "' + b'x' * 4096 + b'"}'):
            _, facts, _ = kernel.run_script(BINDING, kernel.SHARED_MEMORY, [])
        self.assertEqual(facts['kernel_observation'], {})
        with Turn(raw=b'"the kernel isolated everything"'), self.assertRaises(ValueError):
            kernel.run_script(BINDING, kernel.SHARED_MEMORY, [])


class SharedMemoryPathTests(unittest.TestCase):
    def test_existing_private_fixture_path_refuses_before_any_host_marker(self):
        with tempfile.TemporaryDirectory() as temporary:
            shm = Path(temporary)
            (shm / 'paintedwolf-private-fixed').write_bytes(b'stale')
            fake = lambda value: shm if value == '/dev/shm' else Path(value)
            with patch.object(kernel, 'Path', side_effect=fake), patch.object(kernel.uuid, 'uuid4', return_value=MagicMock(hex='fixed')), \
                    patch.object(kernel, 'run_script') as run, self.assertRaises(ValueError):
                kernel.private_shared_memory(BINDING)
            run.assert_not_called()
            self.assertFalse((shm / 'paintedwolf-host-fixed').exists())


class HelperBoundaryTests(unittest.TestCase):
    def helper(self, home: Path, **turn):
        return patch.object(kernel.Path, 'home', return_value=home), Turn(**turn)

    def test_filtered_descendant_that_cannot_write_outside_passes(self):
        witness = {'child': {'seccomp': 2, 'outside_write_denied': True}, 'child_reaped': True}
        with tempfile.TemporaryDirectory() as temporary:
            home = Path(temporary)
            patched, scenario = self.helper(home, witness=witness)
            with patched, scenario:
                status, facts, streams = kernel.helper_boundary(BINDING)
            self.assertEqual(status, 'passed')
            self.assertEqual(streams, {'stdout': 'engine'})
            self.assertEqual(scenario.written, {'helper.py': kernel.HELPER})
            outside = scenario.command().split()[-1]
            self.assertTrue(outside.startswith(str(home / 'paintedwolf-helper-outside-')))
            self.assertFalse(Path(outside).exists())
            self.assertEqual(list(home.iterdir()), [])
            self.assertEqual(facts['kernel_observation'], witness)
            self.assertTrue(facts['unconfined_marker_read'] and facts['outside_marker_preserved'])
            self.assertEqual(facts['fixture_sha256'], hashlib.sha256(kernel.HELPER.encode()).hexdigest())

    def test_unfiltered_descendants_incomplete_capture_and_missing_witnesses_fail_closed(self):
        with tempfile.TemporaryDirectory() as temporary:
            home = Path(temporary)
            failing = [dict(witness={'child': {'seccomp': 0, 'outside_write_denied': True}, 'child_reaped': True}),
                       dict(witness={'child': {'seccomp': 2, 'outside_write_denied': False}, 'child_reaped': True}),
                       dict(witness={'child': {'seccomp': 2, 'outside_write_denied': True}, 'child_reaped': True}, invoked=False),
                       dict(witness={'child': {'seccomp': 2, 'outside_write_denied': True}, 'child_reaped': True},
                            observations={'capture_complete': True, 'output_limit': True})]
            for index, changed in enumerate(failing):
                patched, scenario = self.helper(home, **changed)
                with self.subTest(index=index), patched, scenario:
                    status, _, _ = kernel.helper_boundary(BINDING)
                self.assertEqual(status, 'failed')
                self.assertEqual(list(home.iterdir()), [])
            patched, scenario = self.helper(home)
            with patched, scenario, self.assertRaises(ValueError):
                kernel.helper_boundary(BINDING)
            self.assertEqual(list(home.iterdir()), [])


if __name__ == '__main__':
    unittest.main()
