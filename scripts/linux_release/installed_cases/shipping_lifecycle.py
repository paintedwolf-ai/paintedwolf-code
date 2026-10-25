"""Prove repeated work on the same publicly launched engine with fresh fixture custody."""
from pathlib import Path
import tempfile
import time

from .app import descends, process_identity
from .application_scope import APP_SCOPE_RE, observe_engine_scope
from .keepawake import await_inhibitor


def selected_mode(binding):
    desktop = binding.get('case_inputs', {}).get('desktop', {})
    mode = desktop.get('sleep_launch_mode', 'controlled')
    if mode not in {'controlled', 'unscoped-direct', 'desktop-entry', 'pw-open-cold-start'}:
        raise ValueError('unsupported keepawake shipping launch mode')
    return mode


def capture(app, native, *, held):
    app.require_running()
    app.require_engine()
    engine = app.observations['engine_identity']
    chain = descends(engine, app.process.pid)
    if not chain:
        raise ValueError('shipping engine lost its real launch owner')
    rows = [*chain, process_identity(app.process.pid)]
    cgroups = []
    for row in rows:
        cgroup = Path(f'/proc/{row["pid"]}/cgroup').read_text().strip()
        if process_identity(row['pid']) != row:
            raise ValueError('shipping process changed during cgroup capture')
        cgroups.append({'identity': row, 'cgroup': cgroup})
    scope = observe_engine_scope(app.observations['application_unit'], engine['pid'])
    if held:
        match = APP_SCOPE_RE.fullmatch(scope['engine_cgroup'].rsplit('/', 1)[-1])
        if match is None or match.group(1) != scope['application_id']:
            raise ValueError('active shipping engine has no application attribution')
    session = native.suspend_inhibition()
    actions = session['inhibited_actions']
    if bool(actions & 4) is not held:
        raise ValueError('GNOME suspend inhibition differs from the active-work phase')
    if actions & 8:
        raise ValueError('active work unexpectedly suppresses GNOME idle locking')
    return {'engine_scope': scope, 'process_cgroups': cgroups,
            'gnome_session_manager': session['owner'], 'gnome_inhibited_actions': actions}


def repeat_turn(app, binding, fixture, provider, native, expected_engine, root, streams):
    from .desktop_turn import submit_project_turn
    from .desktop_ui import DesktopUI
    from .fixture_turn import await_fixture_reply
    from .process import run
    from .profile_setup import await_session, require_active
    from .provider import MODEL

    observed, barrier = {}, None
    try:
        barrier = fixture.begin_next_turn()
        project = Path(tempfile.mkdtemp(prefix='shipping-repeat-project-', dir=root))
        observed['project'] = str(project)
        opened = run(binding, 'pw', ['open', str(project)], timeout=30)
        observed['project_open'] = {key: opened[key] for key in (
            'launch_identity', 'exit_code', 'timed_out', 'output_limit') if key in opened}
        for name in ('stdout', 'stderr'):
            if name in opened:
                streams['shipping-repeat-project-open-' + name] = opened[name]
        if opened['exit_code'] or opened['timed_out'] or opened['output_limit']:
            raise ValueError('repeated shipping work refused its fresh project')
        observed['opened_session'] = await_session(app, project, provider['provider_id'], MODEL,
            status='idle', reason='shipping-repeat-not-opened')
        submit_project_turn(app)
        with DesktopUI(app):
            await_session(app, project, provider['provider_id'], MODEL, reason='shipping-repeat-not-busy')
            active = require_active(app, project, provider['provider_id'], MODEL)
            observed.update(active=active, held=barrier.await_held())
            observed['inhibitor'] = await_inhibitor(app, native, held=True,
                expected=expected_engine, deadline=time.monotonic() + 15)
            observed['during_work'] = capture(app, native, held=True)
            barrier.release()
            observed['released'] = barrier.await_response_written()
            observed['reply'] = await_fixture_reply(app, project, provider['provider_id'],
                fixture.acquisition, selected_session=active['id'])
            observed['after_completion'] = await_inhibitor(app, native, held=False,
                expected=expected_engine, deadline=time.monotonic() + 15)
            observed['scope_after_completion'] = capture(app, native, held=False)
        return observed
    except (OSError, ValueError, TypeError) as error:
        if barrier is not None:
            observed['response_control'] = barrier.snapshot()
        error.observations = {**getattr(error, 'observations', {}), 'shipping_repeat': observed}
        raise
