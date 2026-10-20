"""Use only the installed window's normal accessible vault form; keep its password in memory."""
from __future__ import annotations

from contextlib import contextmanager
import os
from pathlib import Path
import secrets
import time

from .app import process_identity, session_environment
from .records import CaseUnavailable

_password = None
_password_owner = None


def owned_process(pid: int, app) -> bool:
    if type(pid) is not int or pid <= 1:
        return False
    root = app.observations['launch_identity']
    seen = set()
    try:
        while pid > 1 and pid not in seen and len(seen) < 32:
            seen.add(pid)
            row = process_identity(pid)
            if pid == root['pid']:
                return row == root
            pid = row['parent_pid']
    except (FileNotFoundError, ProcessLookupError):
        pass
    return False


def controls(window, api):
    pending, visited, fields, buttons = [(window, 0)], 0, {}, {}
    while pending:
        node, depth = pending.pop()
        visited += 1
        if visited > 4096 or depth > 64 or not 0 <= node.childCount <= 1024:
            raise ValueError('installed accessible window exceeds its traversal bound')
        role = node.getRole()
        if role in {api.ROLE_PASSWORD_TEXT, api.ROLE_PUSH_BUTTON}:
            state = node.getState()
            if state.contains(api.STATE_SHOWING) and state.contains(api.STATE_ENABLED):
                name = node.name
                destination = fields if role == api.ROLE_PASSWORD_TEXT else buttons
                if name in {'App password', 'Confirm app password', 'Create vault', 'Unlock vault'}:
                    destination.setdefault(name, []).append(node)
        pending.extend((node[index], depth + 1) for index in range(node.childCount))
    if not fields:
        return None
    create = set(fields) == {'App password', 'Confirm app password'} and 'Create vault' in buttons
    unlock = set(fields) == {'App password'} and 'Unlock vault' in buttons
    if create == unlock or any(len(rows) != 1 for rows in fields.values()):
        raise CaseUnavailable('installed vault form is not uniquely identifiable')
    button = 'Create vault' if create else 'Unlock vault'
    if len(buttons[button]) != 1:
        raise CaseUnavailable('installed vault action is not uniquely identifiable')
    return ('create' if create else 'unlock', {name: rows[0] for name, rows in fields.items()}, buttons[button][0])


def form(api, app):
    desktop = api.Registry.getDesktop(0)
    if not 0 <= desktop.childCount <= 256:
        raise ValueError('native accessibility application inventory exceeds its bound')
    matches = []
    for index in range(desktop.childCount):
        application = desktop[index]
        pid = application.get_process_id()
        if not owned_process(pid, app):
            continue
        before = process_identity(pid)
        if not 0 <= application.childCount <= 64:
            raise ValueError('installed accessibility window inventory exceeds its bound')
        for offset in range(application.childCount):
            window = application[offset]
            if window.getRole() not in {api.ROLE_FRAME, api.ROLE_DIALOG}:
                continue
            if not window.getState().contains(api.STATE_SHOWING):
                continue
            selected = controls(window, api)
            if selected:
                matches.append((pid, before, selected))
        if process_identity(pid) != before or not owned_process(pid, app):
            raise ValueError('installed accessible application changed during form selection')
    if len(matches) > 1:
        raise CaseUnavailable('installed application exposes multiple vault forms')
    return matches[0] if matches else None


def complete(selected, owner: tuple) -> dict:
    global _password, _password_owner
    pid, identity, (mode, fields, button) = selected
    if mode not in {'create', 'unlock'} or process_identity(pid) != identity:
        raise ValueError('installed accessible vault owner changed before input')
    if mode == 'create':
        if _password is not None:
            raise CaseUnavailable('campaign already created a vault; preserving its existing custody')
        _password, _password_owner = secrets.token_urlsafe(32), owner
    elif _password is None or _password_owner != owner:
        raise CaseUnavailable('pre-existing password vault has no password owned by this campaign')
    try:
        for field in fields.values():
            if field.queryEditableText().setTextContents(_password) is not True:
                raise ValueError('native editable password field refused the fixed input')
        action = button.queryAction()
        if action.nActions != 1 or action.doAction(0) is not True:
            raise ValueError('native vault button refused its normal action')
        if process_identity(pid) != identity:
            raise ValueError('installed accessible vault owner changed during input')
    except Exception:
        # D-Bus or toolkit exception prose must never carry input values into evidence.
        raise CaseUnavailable('normal native vault form interaction is unavailable') from None
    return {'mode': mode, 'ui_identity': identity, 'password_retained': False, 'normal_action': True}


@contextmanager
def accessibility_bus(session):
    environment = session_environment(session)
    names = ('DBUS_SESSION_BUS_ADDRESS', 'XDG_RUNTIME_DIR')
    previous = {name: os.environ.get(name) for name in names}
    try:
        os.environ.update({name: environment[name] for name in names})
        yield
    finally:
        for name, value in previous.items():
            if value is None:
                os.environ.pop(name, None)
            else:
                os.environ[name] = value


def onboard(app):
    deadline, acted, api = time.monotonic() + min(app.timeout, 60), False, None
    owner = (os.getuid(), str(Path.home()))
    with accessibility_bus(app.observations['session']):
        while time.monotonic() < deadline and app.process.poll() is None:
            if app.try_ready():
                return
            if api is None:
                try:
                    import pyatspi
                    api = pyatspi
                except ImportError:
                    time.sleep(0.1)
                    continue
            if not acted:
                try:
                    selected = form(api, app)
                    if selected:
                        app.observations['onboarding'] = complete(selected, owner)
                        acted = True
                except (ValueError, CaseUnavailable):
                    raise
                except Exception:
                    raise CaseUnavailable('normal native vault accessibility is unavailable') from None
            time.sleep(0.1)
    raise CaseUnavailable('installed application did not finish its normal startup interaction')
