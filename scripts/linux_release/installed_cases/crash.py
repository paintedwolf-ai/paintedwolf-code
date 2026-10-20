"""Observe installed command cleanup after killing only its captured owning engine."""
from __future__ import annotations

from contextlib import ExitStack
from pathlib import Path
import os
import select
import shlex
import signal
import subprocess
import sys
import tempfile
import time

from .app import InstalledApp, process_identity
from .approval import approve_host_execution
from .lifecycle import SCRIPT, capture, cleanup, handle, scenario, state, witness
from .provider import ProviderFixture
from .workflow import ToolTurn


def kill_engine(identity: dict) -> dict:
    descriptor = os.pidfd_open(identity['pid'], 0)
    try:
        if process_identity(identity['pid']) != identity:
            raise ValueError('installed engine changed before the deliberate crash')
        signal.pidfd_send_signal(descriptor, signal.SIGKILL, None, 0)
        observer = select.poll()
        observer.register(descriptor, select.POLLIN)
        if not observer.poll(5000):
            raise OSError('captured installed engine did not exit after its deliberate crash')
    finally:
        os.close(descriptor)
    return {'identity': identity, 'signal': 'SIGKILL', 'exit_observed': True}


def engine_crash(binding: dict, host_execution: bool) -> tuple[str, dict, dict]:
    owned, unrelated = [], None
    try:
        with tempfile.TemporaryDirectory(prefix='paintedwolf-engine-loss-') as directory, ExitStack() as stack:
            root = Path(directory)
            app = stack.enter_context(InstalledApp(binding))
            unrelated = subprocess.Popen([str(Path(sys.executable).resolve()), '-c',
                'import time; time.sleep(120)'], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL, start_new_session=True)
            outside = process_identity(unrelated.pid)
            owned.append({'pid': outside['pid'], 'start_ticks': str(outside['start_time_ticks'])})
            script = root / 'lifecycle.py'
            script.write_text(SCRIPT)
            arguments = {'command': shlex.join([str(Path(sys.executable).resolve()), str(script), 'detached']),
                         'background': True, 'wait_ms': 1, 'timeout_ms': 90000}
            if host_execution:
                arguments['capability_request'] = {'host_execution': True}
            provider = stack.enter_context(ProviderFixture([{'name': 'command', 'arguments': arguments}]))
            turn = stack.enter_context(ToolTurn(app, provider, root))
            turn.submit()
            if host_execution:
                approve_host_execution(turn, arguments)
            process = handle(turn.wait())
            observation = witness(turn)
            members = capture(observation, host_execution)
            owned.extend(members)
            if len(members) != 2 or observation.get('first_child_reaped') is not True:
                raise ValueError('installed detached command was not live before the deliberate crash')
            receipt = kill_engine(app.observations['engine_identity'])
            # This session lost its actual engine. There is no API terminal
            # outcome to invent, nor a live session to abort during teardown.
            turn.session_id = None
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline and any(state(row) is not None for row in members):
                time.sleep(0.1)
            gone = all(state(row) is None for row in members)
            survived = unrelated.poll() is None and process_identity(unrelated.pid) == outside
            passed = gone and survived
            facts = {'host_execution': host_execution, 'engine_crash': receipt,
                     'process_handle': process, 'command_processes': members,
                     'command_processes_gone': gone, 'unrelated_identity': outside,
                     'unrelated_survives': survived, 'terminal_api_receipt_available': False,
                     'app': app.observations, 'turn': turn.observations}
        return 'passed' if passed else 'failed', facts, app.streams()
    finally:
        cleanup(owned)
        if unrelated is not None:
            unrelated.wait(timeout=5)


def lifetime(binding, *, host_execution):
    initial, initial_facts, initial_streams = scenario(binding, host_execution, host_execution)
    if initial != 'passed':
        return initial, {'normal': {'status': initial, 'observations': initial_facts},
                         'engine_sigkill': {'status': 'unverified', 'reason': 'normal-lifetime-not-passed'}}, initial_streams
    crashed, crash_facts, crash_streams = engine_crash(binding, host_execution)
    streams = {'normal-' + name: value for name, value in initial_streams.items()}
    streams.update({'crash-' + name: value for name, value in crash_streams.items()})
    return ('passed' if initial == crashed == 'passed' else 'failed',
            {'normal': {'status': initial, 'observations': initial_facts},
             'engine_sigkill': {'status': crashed, 'observations': crash_facts}}, streams)


def detached_cleanup(binding):
    return lifetime(binding, host_execution=False)


def host_execution_lifetime(binding):
    return lifetime(binding, host_execution=True)
