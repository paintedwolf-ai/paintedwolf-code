"""Exercise filesystem and local-network denial through the installed command tool."""
from __future__ import annotations

import json
from pathlib import Path
import shlex
import socket
import sys
import tempfile
import uuid

from .app import InstalledApp
from .provider import ProviderFixture
from .records import CaseUnavailable
from .workflow import ToolTurn

SCRIPT = '''import errno, json, pathlib, socket, sys
outside, protected, port = sys.argv[1:]
result = {}
for name, operation in [('outside_write', lambda: pathlib.Path(outside).write_text('fixture')),
                        ('protected_read', lambda: pathlib.Path(protected).read_bytes())]:
    try:
        operation()
    except OSError as error:
        result[name] = error.errno in (errno.EACCES, errno.EPERM)
        result[name + '_errno'] = error.errno
    else:
        result[name] = False
with socket.socket() as stream:
    stream.settimeout(3)
    try:
        stream.connect(('127.0.0.1', int(port)))
    except OSError as error:
        result['local_connect'] = error.errno in (errno.EACCES, errno.EPERM)
        result['local_connect_errno'] = error.errno
    else:
        result['local_connect'] = False
pathlib.Path('qualification-observation.json').write_text(json.dumps(result))
'''


def successful_invocation(results: list[dict]) -> bool:
    matches = [row for row in results if row.get('tool') == 'command']
    if len(matches) != 1:
        return False
    result = matches[0]
    invocation = result.get('invocation', {})
    return (result.get('outcome') == 'completed' and invocation.get('tool') == 'command'
            and invocation.get('invoked') is True and invocation.get('status') == 'completed')


def filesystem_egress(binding: dict) -> tuple[str, dict, dict]:
    identifier = uuid.uuid4().hex
    outside = Path.home() / ('paintedwolf-outside-' + identifier)
    protected = Path.home() / '.ssh' / ('paintedwolf-qualification-' + identifier)
    if outside.exists() or protected.exists():
        raise ValueError('installed security fixture paths already exist')
    if not protected.parent.is_dir():
        raise CaseUnavailable('owned guest lacks its existing SSH credential directory')
    # This file contains only a public fixture marker, never real key material.
    with protected.open('xb') as stream:
        stream.write(b'public qualification fixture')
    protected.chmod(0o600)
    try:
        with outside.open('xb') as stream:
            stream.write(b'public write fixture')
        baseline = {'outside_write': outside.read_bytes() == b'public write fixture',
                    'protected_read': protected.read_bytes() == b'public qualification fixture'}
        outside.unlink()
        with tempfile.TemporaryDirectory(prefix='paintedwolf-command-') as directory, socket.socket() as listener:
            project = Path(directory)
            script = project / 'boundary.py'
            script.write_text(SCRIPT)
            listener.bind(('127.0.0.1', 0))
            listener.listen(1)
            port = listener.getsockname()[1]
            with socket.create_connection(('127.0.0.1', port), timeout=3):
                peer, _ = listener.accept()
                peer.close()
            baseline['local_connect'] = True
            command = shlex.join([str(Path(sys.executable).resolve()), str(script), str(outside), str(protected), str(port)])
            with ProviderFixture([{'name': 'command', 'arguments': {'command': command}}]) as provider:
                with InstalledApp(binding) as app, ToolTurn(app, provider, project) as turn:
                    turn.submit()
                    results = turn.wait()
                    witness = turn.workspace / 'qualification-observation.json'
                    if not witness.is_file() or witness.is_symlink() or witness.stat().st_size > 4096:
                        passed = False
                        facts = {'reason': 'installed-command-produced-no-bounded-observation',
                                 'turn': turn.observations, 'app': app.observations, 'unconfined_baseline': baseline}
                    else:
                        observation = json.loads(witness.read_bytes())
                        passed = (successful_invocation(results) and not outside.exists()
                                  and all(baseline.values()) and all(observation.get(name) is True for name in
                                          ('outside_write', 'protected_read', 'local_connect')))
                        facts = {'kernel_observation': observation, 'turn': turn.observations,
                                 'provider': provider.observations, 'app': app.observations,
                                 'unconfined_baseline': baseline}
                return 'passed' if passed else 'failed', facts, app.streams()
    finally:
        protected.unlink(missing_ok=True)
        outside.unlink(missing_ok=True)
