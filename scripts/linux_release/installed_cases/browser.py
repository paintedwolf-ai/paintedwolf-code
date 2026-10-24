"""Drive the protected browser fixture through normal GUI input and durable receipts."""
from __future__ import annotations

import hashlib
import os
from pathlib import Path
import subprocess
import tempfile

from .app import InstalledApp
from .browser_boundary import observe, validate
from .browser_result import opened
from .desktop_launch import protected_launcher
from .desktop_ui import interactive_window
from .desktop_turn import submit_project_turn
from .fixture_turn import await_fixture_reply
from .kernel import namespace_observer
from .lifecycle import cleanup, state
from .process import run
from .provider import ProviderFixture
from .provider_acquisition import fixed_call
from .provider_recovery import load as load_provider_recovery
from .provider_setup import configure_provider
from .records import CaseFailed, CaseUnavailable

HTML = '<!doctype html><meta charset="utf-8"><title>Installed browser fixture</title><h1>Painted Wolf Code</h1><p>Owned browser boundary fixture.</p>'


def browser_sandbox(binding: dict) -> tuple[str, dict, dict]:
    launch_binding = {**binding, 'launcher': protected_launcher(binding)}
    identities, facts, streams = [], {'fixture_sha256': hashlib.sha256(HTML.encode()).hexdigest(),
        'startup_phase': 'launch-and-wait-ready-without-onboarding',
        'protected_browser_ancestry_verified': False}, {}
    app = provider = None
    # Preserve the actual owned project on all outcomes for retained receipt diagnosis.
    project = Path(tempfile.mkdtemp(prefix='browser-project-', dir=Path.home()))
    (project / 'fixture.html').write_text(HTML)
    facts['project_root'] = str(project)
    try:
        recovery = load_provider_recovery(binding)
        if recovery is not None:
            facts['provider_recovery'], streams['provider-registration'] = recovery
        with ProviderFixture.gui([fixed_call('page_open')], acquire='page_open',
                endpoint=recovery[0]['endpoint'] if recovery is not None else None) as provider:
            app = InstalledApp(launch_binding)
            app.launch()
            app.wait_ready()
            facts['interactive'] = interactive_window(app)
            selected = configure_provider(app, provider)
            project_open = run(launch_binding, 'pw', ['open', str(project)], timeout=30)
            facts['project_open'] = project_open
            if project_open['exit_code'] or project_open['timed_out'] or project_open['output_limit']:
                raise CaseUnavailable('normal installed CLI refused the owned browser fixture project')
            submit_project_turn(app, operation='browser-fixture-prompt-v1')
            turn = await_fixture_reply(app, project, selected['provider_id'], provider.acquisition)
            facts['turn'] = turn
            facts['opened'] = opened(turn)
            if not any(row['completed'] for row in provider.observations):
                raise ValueError('browser fixture never completed its full normal engine turn')
            engine = app.observations['engine_identity']
            host = namespace_observer(engine)
            observation = observe(binding, engine)
            facts.update(engine_namespaces=host, browser_observation=observation)
            identities = validate(observation, engine, host, os.getuid())
    except (OSError, subprocess.SubprocessError, ValueError, TypeError, KeyError) as error:
        facts.update(getattr(error, 'observations', {}))
        facts['failure_type'] = type(error).__name__
        failure = error if isinstance(error, CaseUnavailable) else CaseFailed('protected browser fixture did not complete')
        failure.observations, failure.streams = facts, streams
        raise failure from None
    finally:
        if app is not None:
            app.stop()
            facts['launch'] = app.observations
            streams.update(app.streams())
        if provider is not None:
            facts['provider_requests'] = list(provider.observations)
            facts['provider_discovery'] = list(provider.discovery_observations)
            facts['provider_transport'] = {'requests': list(provider.transport_observations),
                                           'clipped': provider.transport_clipped}
        facts['browser_processes_remaining_after_stop'] = [
            identity for identity in identities if state(identity) not in {None, 'Z', 'X'}]
        # Only exact captured descendants; never signal a PID reused after observation.
        cleanup(identities)
    complete = (not facts['browser_processes_remaining_after_stop'] and app is not None
                and app.observations.get('capture_complete') is True and not app.observations.get('output_limit'))
    # This declared base has only the engine-descendant observer. Its separately
    # owned protected-bootstrap dependency must land before this case can pass.
    facts['reason'] = 'protected-browser-observer-dependency-not-reconciled'
    return 'unverified' if complete else 'failed', facts, streams
