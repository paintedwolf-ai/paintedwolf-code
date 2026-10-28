"""The filesystem and egress case proves denial from inside the installed command with its own markers."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import security_boundary
from linux_release.installed_cases.records import CaseUnavailable

BINDING = {'binding': True}
DENIED = {'outside_write': True, 'protected_read': True, 'local_connect': True}


class Turn:
    """Patch the installed collaborators; the command's witness lands in a scratch workspace."""

    def __init__(self, witness=None, invoked=True):
        self.witness, self.invoked = witness, invoked
        self.app = MagicMock()
        self.app.observations = {'capture_complete': True}
        self.app.streams.return_value = {'stdout': 'engine'}
        self.turn = MagicMock()
        self.turn.observations = {'turn': 'facts'}
        self.turn.wait.return_value = [{'tool': 'command'}]
        self.provider = MagicMock()
        self.provider.observations = {'provider': 'facts'}

    def tool_turn(self, app, provider, project):
        self.project = Path(project)
        self.script = (self.project / 'boundary.py').read_text()
        self.workspace = tempfile.TemporaryDirectory()
        self.turn.workspace = Path(self.workspace.name)
        if self.witness is not None:
            (self.turn.workspace / 'qualification-observation.json').write_text(json.dumps(self.witness))
        manager = MagicMock()
        manager.__enter__.return_value = self.turn
        return manager

    def __enter__(self):
        app_manager = MagicMock()
        app_manager.__enter__.return_value = self.app
        provider_manager = MagicMock()
        provider_manager.__enter__.return_value = self.provider
        self.patches = [patch.object(security_boundary, 'InstalledApp', MagicMock(return_value=app_manager)),
                        patch.object(security_boundary, 'ProviderFixture', MagicMock(return_value=provider_manager)),
                        patch.object(security_boundary, 'ToolTurn', side_effect=self.tool_turn),
                        patch.object(security_boundary, 'successful_invocation', return_value=self.invoked),
                        patch.object(security_boundary.uuid, 'uuid4', return_value=MagicMock(hex='f' * 32))]
        self.mocks = [item.start() for item in self.patches]
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()
        if hasattr(self, 'workspace'):
            self.workspace.cleanup()

    def command(self) -> list[str]:
        return self.mocks[1].call_args.args[0][0]['arguments']['command'].split()


def home(path: Path):
    return patch.object(security_boundary.Path, 'home', return_value=path)


class FilesystemEgressTests(unittest.TestCase):
    def test_denied_markers_and_connection_from_the_command_pass_with_clean_fixture_paths(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            (base / '.ssh').mkdir()
            with home(base), Turn(witness=DENIED) as scenario:
                status, facts, streams = security_boundary.filesystem_egress(BINDING)
            self.assertEqual(status, 'passed')
            self.assertEqual(streams, {'stdout': 'engine'})
            self.assertEqual(scenario.script, security_boundary.SCRIPT)
            outside, protected, port = scenario.command()[-3:]
            self.assertEqual(outside, str(base / ('paintedwolf-outside-' + 'f' * 32)))
            self.assertEqual(protected, str(base / '.ssh' / ('paintedwolf-qualification-' + 'f' * 32)))
            self.assertTrue(0 < int(port) < 65536)
            self.assertEqual(sorted(item.name for item in base.rglob('*')), ['.ssh'])
            self.assertEqual(facts['kernel_observation'], DENIED)
            self.assertEqual(facts['unconfined_baseline'], {'outside_write': True, 'protected_read': True, 'local_connect': True})
            self.assertEqual((facts['turn'], facts['provider'], facts['app']),
                             ({'turn': 'facts'}, {'provider': 'facts'}, {'capture_complete': True}))

    def test_missing_witness_allowed_access_or_uninvoked_command_fail(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            (base / '.ssh').mkdir()
            with home(base), Turn() as scenario:
                status, facts, _ = security_boundary.filesystem_egress(BINDING)
            self.assertEqual(status, 'failed')
            self.assertEqual(facts['reason'], 'installed-command-produced-no-bounded-observation')
            self.assertEqual(facts['unconfined_baseline']['local_connect'], True)
            for witness, invoked in [(dict(DENIED, local_connect=False), True), (dict(DENIED, protected_read=False), True),
                                     (DENIED, False)]:
                with self.subTest(witness=witness, invoked=invoked), home(base), Turn(witness=witness, invoked=invoked):
                    status, facts, _ = security_boundary.filesystem_egress(BINDING)
                self.assertEqual(status, 'failed')
                self.assertEqual(facts['kernel_observation'], witness)
            self.assertEqual(sorted(item.name for item in base.rglob('*')), ['.ssh'])

    def test_fixture_paths_must_be_fresh_inside_an_existing_credential_directory(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            with home(base), Turn() as scenario, self.assertRaises(CaseUnavailable):
                security_boundary.filesystem_egress(BINDING)
            scenario.mocks[0].assert_not_called()
            (base / '.ssh').mkdir()
            (base / '.ssh' / ('paintedwolf-qualification-' + 'f' * 32)).write_bytes(b'existing')
            with home(base), Turn() as scenario, self.assertRaises(ValueError):
                security_boundary.filesystem_egress(BINDING)
            scenario.mocks[0].assert_not_called()
            self.assertEqual((base / '.ssh' / ('paintedwolf-qualification-' + 'f' * 32)).read_bytes(), b'existing')


if __name__ == '__main__':
    unittest.main()
