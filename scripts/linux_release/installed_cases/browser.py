"""Qualify the actual installed managed browser's outer process boundary."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import tempfile

from .app import InstalledApp
from .browser_boundary import observe, validate
from .kernel import namespace_observer
from .lifecycle import cleanup, state
from .provider import ProviderFixture
from .workflow import ToolTurn, wait_settled

HTML = '<!doctype html><meta charset="utf-8"><title>Installed browser fixture</title><h1>Painted Wolf Code</h1><p>Owned browser boundary fixture.</p>'


def opened(results: list[dict]) -> dict:
    rows = [row for row in results if row.get('tool') == 'page_open']
    if len(rows) != 1:
        raise ValueError('browser fixture omitted its unique opened page')
    row = rows[0]
    invocation = row.get('invocation', {})
    if (row.get('outcome') != 'completed' or invocation.get('tool') != 'page_open'
            or invocation.get('invoked') is not True or invocation.get('status') != 'completed'):
        raise ValueError('browser fixture did not actually invoke its installed page tool')
    document = json.loads(row['content'])
    identifier = document.get('id')
    if (not isinstance(identifier, str) or not identifier or len(identifier) > 128
            or not isinstance(document.get('live_pages'), list) or identifier not in document['live_pages']):
        raise ValueError('browser fixture did not retain its actual live page')
    return {'invocation': invocation, 'page': document}


def browser_sandbox(binding: dict) -> tuple[str, dict, dict]:
    identities, facts = [], {}
    try:
        with tempfile.TemporaryDirectory(prefix='paintedwolf-installed-browser-') as directory:
            project = Path(directory)
            (project / 'fixture.html').write_text(HTML)
            with ProviderFixture([{'name': 'page_open', 'arguments': {'path': 'fixture.html'}}]) as provider:
                with InstalledApp(binding) as app, ToolTurn(app, provider, project) as turn:
                    turn.submit()
                    results = turn.wait(timeout=90)
                    facts['opened'] = opened(results)
                    wait_settled(app, turn.session_id)
                    engine = app.observations['engine_identity']
                    host = namespace_observer(engine)
                    observation = observe(binding, engine)
                    identities = validate(observation, engine, host, os.getuid())
                    facts.update({'engine_namespaces': host, 'browser_observation': observation,
                                  'turn': turn.observations, 'app': app.observations,
                                  'fixture_sha256': hashlib.sha256(HTML.encode()).hexdigest()})
                remaining = [identity for identity in identities if state(identity) not in {None, 'Z', 'X'}]
                facts['browser_processes_remaining_after_stop'] = remaining
                passed = not remaining and app.observations.get('capture_complete') is True and not app.observations.get('output_limit')
                return 'passed' if passed else 'failed', facts, app.streams()
    finally:
        # Exact captured descendants only; a PID reused after observation is never signaled.
        cleanup(identities)
