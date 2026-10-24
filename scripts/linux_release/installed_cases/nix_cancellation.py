"""Cancel an actual Nix daemon build through the installed application command API."""
from __future__ import annotations

import json
from pathlib import Path
import shlex
import tempfile
import time

from .app import InstalledApp
from .approval import approve_host_execution
from .lifecycle import handle, mirror
from .nix_fixture import load, observe, realized
from .provider import ProviderFixture
from .records import CaseUnavailable
from .workflow import ToolTurn, accepted
from ..installed_runtime import nix_command


def stopped(process: dict, before: dict, after: dict) -> bool:
    return (bool(before.get('owned')) and not after.get('live')
            and process.get('running') is False and type(process.get('exit_code')) is int
            and process['exit_code'] != 0)


def scenario(binding: dict) -> tuple[str, dict, dict]:
    from .nix_workflows import exercise
    authority, baseline, baseline_streams = exercise(binding, 'daemon')
    if authority != 'passed':
        return 'failed', {'daemon_authority': baseline}, baseline_streams
    fixture = load(binding)
    selected = fixture['cancellation']
    program = Path(fixture['client']['path'])
    # A previously completed fixture cannot prove a fresh daemon-side cancellation.
    if realized(program, selected['output']):
        raise CaseUnavailable('selected cancellation derivative was already realized')
    observations = {'daemon_authority': baseline, 'derivation': selected, 'builder': fixture['static_builder'],
                    'client': fixture['client'], 'cleanup_required': False}
    owned = None
    streams = {}
    try:
        with tempfile.TemporaryDirectory(prefix='paintedwolf-nix-cancellation-') as directory:
            arguments = {'command': shlex.join([str(program), '--extra-experimental-features',
                         'nix-command', '--offline', '--option', 'substituters', '', '--option',
                         'extra-substituters', '', '--option', 'builders', '', '--store', 'daemon',
                         'build', '--no-link', '--max-jobs', '1', selected['derivation'] + '^out']),
                         'background': True, 'wait_ms': 1, 'timeout_ms': 180000,
                         'capability_request': {'host_execution': True}}
            with InstalledApp(binding) as app, ProviderFixture([{'name': 'command', 'arguments': arguments}]) as provider:
                with ToolTurn(app, provider, Path(directory)) as turn:
                    turn.submit()
                    approve_host_execution(turn, arguments)
                    process = handle(turn.wait())
                    deadline = time.monotonic() + 45
                    while time.monotonic() < deadline:
                        owned = observe(fixture, 'observe')
                        if owned['owned']:
                            break
                        time.sleep(0.2)
                    if not owned or not owned['owned']:
                        raise CaseUnavailable('actual selected Nix daemon build did not start its fixed builder')
                    observations['before'] = owned
                    stop = accepted(app, 'POST', f'/v1/sessions/{turn.session_id}/background/{process}/stop', {}, {200})
                    if stop.get('process_id') != process or stop.get('stop_requested') is not True:
                        raise ValueError('installed cancellation did not select its exact Nix client')
                    deadline = time.monotonic() + 15
                    while time.monotonic() < deadline:
                        final = mirror(app, turn.session_id, process)
                        after = observe(fixture, 'state')
                        if final['running'] is False and not after['live']:
                            break
                        time.sleep(0.1)
                    passed = stopped(final, owned, after)
                    observations.update({'after': after, 'client_process': final,
                                         'turn': turn.observations, 'provider': provider.observations,
                                         'app': app.observations})
                streams = app.streams()
        observations['incomplete_output_refused'] = not realized(program, selected['output'])
        passed = passed and observations['incomplete_output_refused']
        ping = json.loads(nix_command(program, 'store', 'ping', '--json'))
        observations['daemon_after_cancel'] = ping
        passed = passed and isinstance(ping, dict) and isinstance(ping.get('version'), str)
        if load(binding) != fixture:
            raise ValueError('Nix fixture changed while the installed command was running')
        return 'passed' if passed else 'failed', observations, streams
    finally:
        if owned and owned.get('owned'):
            state = observe(fixture, 'state')
            if state['live']:
                observations['cleanup_required'] = True
                observations['cleanup'] = observe(fixture, 'cleanup')
