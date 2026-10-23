"""Abandoned calls and closed terminals leave no descendant of the installed app.

Background stop and engine loss are the background-cancel and
setsid-and-double-fork cases. These add the two remaining owners: a session
cancel that abandons a running foreground call, and a terminal close. Each
fixture double-forks a setsid daemon before its owner goes away. Unit-level
proof: the test:confine:linux supervisor leg
(TestSupervisorCleansDoubleForkedNewSessionOnCommandCompletion,
TestSupervisorCancellationSparesAnotherCommandsDetachedDaemon,
TestSupervisorEngineLifetimeEOFRemovesDetachedDaemon,
TestSupervisedPTYInteractiveShellOwnsForeground) and, under the protected
launch, TestRelayedSupervisorsStaySupervised."""
from __future__ import annotations

from contextlib import ExitStack
import json
from pathlib import Path
import shlex
import sys
import tempfile
import time

from .app import InstalledApp
from .lifecycle import SCRIPT, capture, cleanup, state
from .provider import ProviderFixture
from .terminal import TerminalProvider, document
from .workflow import ToolTurn, accepted

OBSERVATION = 'qualification-observation.json'


def witness(workspace: Path, limit: float = 10) -> dict:
    path = workspace / OBSERVATION
    deadline = time.monotonic() + limit
    while time.monotonic() < deadline:
        if path.exists():
            if path.is_symlink() or path.stat().st_size > 4096:
                raise ValueError('installed process witness exceeds its boundary')
            return json.loads(path.read_bytes())
        time.sleep(0.05)
    raise ValueError('installed process did not produce a bounded identity witness')


def gone(members: list[dict], limit: float = 10) -> bool:
    deadline = time.monotonic() + limit
    while time.monotonic() < deadline:
        if all(state(row) is None for row in members):
            return True
        time.sleep(0.1)
    return False


def detached(project: Path) -> str:
    script = project / 'lifecycle.py'
    script.write_text(SCRIPT)
    return shlex.join([str(Path(sys.executable).resolve()), str(script), 'detached'])


def abandon(app, project: Path, owned: list[dict]) -> tuple[str, dict]:
    """Cancels the session while its foreground call runs a double-forked daemon."""
    arguments = {'command': detached(project), 'timeout_ms': 90000}
    with ProviderFixture([{'name': 'command', 'arguments': arguments}]) as provider, ToolTurn(app, provider, project) as turn:
        turn.submit()
        observation = witness(turn.workspace, 30)
        members = capture(observation)
        owned.extend(members)
        if len(members) != 2 or observation.get('first_child_reaped') is not True:
            raise ValueError('abandoned call fixture was not live before its cancel')
        session = turn.session_id
        accepted(app, 'POST', f'/v1/sessions/{session}/abort', {}, {200})
        turn.session_id = None
        ended = gone(members)
    return ('passed' if ended else 'failed', {'session_id': session, 'members': members,
                                              'members_gone': ended, 'turn': turn.observations})


class CloseProvider(TerminalProvider):
    """Opens a terminal on the fixture, then closes it once the daemon is captured."""

    def __init__(self, command: str):
        super().__init__(command)
        self.workspace = None
        self.members = []

    def step(self, results: dict[str, str]):
        if 'terminal-fixture-open' not in results:
            return 'terminal-fixture-open', 'terminal_open', {'command': self.command, 'observe': 'ack'}
        opened = document(results['terminal-fixture-open'])
        if 'terminal-fixture-close' not in results:
            if not self.members:
                self.members = capture(witness(self.workspace, 30))
            return 'terminal-fixture-close', 'terminal_close', {'id': opened['id']}
        closed = document(results['terminal-fixture-close'], opened['id'])
        if closed.get('closed') is not True:
            raise ValueError('terminal close did not acknowledge its exact terminal')
        return None


def close_terminal(app, project: Path, owned: list[dict]) -> tuple[str, dict]:
    with CloseProvider(detached(project)) as provider, ToolTurn(app, provider, project) as turn:
        provider.workspace = turn.workspace
        turn.submit()
        turn.wait(timeout=90)
        deadline = time.monotonic() + 30
        while not provider.members and time.monotonic() < deadline:
            time.sleep(0.05)
        members = provider.members
        owned.extend(members)
        if len(members) != 2:
            raise ValueError('terminal fixture daemon was not captured before close')
        ended = gone(members)
    return ('passed' if ended else 'failed', {'members': members, 'members_gone': ended,
                                              'provider': provider.observations, 'turn': turn.observations})


def scenario(binding: dict, leg, launcher: dict | None = None) -> tuple[str, dict, dict]:
    owned = []
    try:
        with tempfile.TemporaryDirectory(prefix='paintedwolf-descendants-') as directory, ExitStack() as stack:
            app = stack.enter_context(InstalledApp(binding, launcher=launcher))
            status, facts = leg(app, Path(directory), owned)
            facts['app'] = app.observations
        return status, facts, app.streams()
    finally:
        cleanup(owned)


def cancel_abandoned_call(binding: dict) -> tuple[str, dict, dict]:
    return scenario(binding, abandon)


def terminal_close_descendants(binding: dict) -> tuple[str, dict, dict]:
    return scenario(binding, close_terminal)
