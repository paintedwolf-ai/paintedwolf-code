"""Select the authenticated installed bootstrap for protected desktop cases."""
from pathlib import Path
import subprocess
import time

from .app import session_environment
from ..installed_process import process_identity
from ..installed_environment import graphical_session
from ..installed_runtime import file_identity

from .records import CaseUnavailable
from ..installed_runtime import SETUID_LAUNCHER, elf


def protected_launcher(binding):
    # Select only the setuid image bound to these final package bytes. Its actual
    # execution performs the production policy and namespace admission.
    root = Path(binding['install_root'])
    entry = binding['images'].get(str(root / 'bin/pw-launch-entry'))
    expected = binding['images'].get(str(SETUID_LAUNCHER))
    if entry is None or expected is None:
        raise CaseUnavailable('protected-reply-requires-sealed-bootstrap-and-entry')
    if entry['mode'] != 0o755:
        raise ValueError('protected reply launch entry has the wrong mode')
    actual = elf(SETUID_LAUNCHER, binding['artifact']['architecture'], setuid=True)
    if actual != expected:
        raise ValueError('protected reply bootstrap differs from selected final bytes')
    return {'path': str(SETUID_LAUNCHER), **actual}


def activate(app):
    """Run the bound public launcher once without assuming a second engine or API bearer."""
    app.require_running()
    app.require_engine()
    selected = protected_launcher(app.binding)
    path = Path(selected.pop('path'))
    actual = file_identity(path)
    actual.pop('header')
    if actual != selected:
        raise ValueError('public activation image changed after artifact selection')
    session = graphical_session()
    if session != app.observations['session']:
        raise ValueError('public activation changed its captured graphical session')
    child = subprocess.Popen([str(path)], env=session_environment(session),
        stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        start_new_session=True)
    try:
        identity = process_identity(child.pid)
        try:
            code = child.wait(timeout=15)
        except subprocess.TimeoutExpired:
            raise CaseUnavailable('public-launcher-activation-did-not-complete') from None
        if code != 0:
            raise ValueError('public launcher refused repeated activation')
        app.require_running()
        app.require_engine()
        return {'launch_identity': identity, 'exit_code': code, 'same_graphical_session': True}
    finally:
        # This function owns only the secondary launcher it created.
        if child.poll() is None:
            child.terminate()
            try:
                child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=5)


def _visible_native_frame(app, ui, deadline):
    from ..installed_input import accessibility, lineage, surface_wire
    app.require_running()
    app.require_engine()
    launch, engine = (dict(app.observations[name]) for name in ('launch_identity', 'engine_identity'))
    frames = [node for _, node in ui.windows(deadline) if node.getRole() == ui.api.ROLE_FRAME]
    if len(frames) != 1:
        refusal = CaseUnavailable('public-activation-visible-frame-is-missing-or-ambiguous')
        refusal.observations = {'public_activation_frame_inventory': {'count': len(frames),
                                'launch': launch, 'engine': engine}}
        raise refusal
    node = frames[0]
    native = accessibility.identity(node)
    process = process_identity(native['pid'])
    chain = lineage.capture(launch, process)
    selected = surface_wire.object_identity({'bus_name': native['bus_name'], 'object_path': native['path'],
        'process': process, 'lineage': chain}, launch)
    active = bool(node.getState().contains(ui.api.STATE_ACTIVE))
    if (accessibility.identity(node) != native or process_identity(process['pid']) != process
            or lineage.capture(launch, process) != chain or not ui.visible(node)
            or process_identity(launch['pid']) != launch or process_identity(engine['pid']) != engine):
        raise ValueError('public activation native frame or runtime changed during observation')
    app.require_running()
    app.require_engine()
    if app.observations['launch_identity'] != launch or app.observations['engine_identity'] != engine:
        raise ValueError('public activation selected runtime changed during frame observation')
    return {'launch': launch, 'engine': engine, 'frame': selected, 'active': active}


def activate_visible_frame(app, ui):
    """Activate one already visible native frame and witness that exact frame becoming active."""
    deadline = time.monotonic() + 15
    before = _visible_native_frame(app, ui, deadline)
    launched, after, phase = None, None, 'launch'
    try:
        launched = activate(app)
        phase = 'observe'
        while time.monotonic() < deadline:
            after = _visible_native_frame(app, ui, deadline)
            phase = 'compare'
            if any(after[name] != before[name] for name in ('launch', 'engine', 'frame')):
                raise ValueError('public activation replaced its pinned visible frame or runtime')
            if after['active']:
                return {'before': before, 'after': after, 'public_launcher': launched, 'verified': True}
            time.sleep(0.05)
            phase = 'observe'
        phase = 'deadline'
        raise CaseUnavailable('public-activation-same-visible-frame-did-not-become-active')
    except Exception as error:
        failure_type = ('unavailable' if isinstance(error, CaseUnavailable) else
            'identity-refused' if isinstance(error, (ValueError, TypeError, KeyError)) else
            'operating-system-error' if isinstance(error, OSError) else 'native-observation-error')
        failure = error if isinstance(error, (ValueError, TypeError, KeyError, OSError)) else CaseUnavailable('public-activation-native-observation-unavailable')
        failure.observations = {'public_activation': {'before': before, 'public_launcher': launched,
            'after': after, 'phase': phase, 'failure_type': failure_type, 'verified': False}}
        raise failure from None
