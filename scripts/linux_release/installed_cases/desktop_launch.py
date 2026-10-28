"""Select the authenticated installed bootstrap for protected desktop cases."""
from pathlib import Path
import subprocess

from .app import process_identity, session_environment
from ..installed_environment import graphical_session
from ..installed_runtime import file_identity

from .records import CaseUnavailable
from ..campaign.runtime import SETUID_LAUNCHER, elf


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
