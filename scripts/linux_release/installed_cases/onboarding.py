"""Use only the installed window's normal accessible vault form; keep its password in memory."""
from __future__ import annotations

import os
from pathlib import Path
import secrets
import re
import time

from .app import process_identity
from .desktop_ui import accessibility_bus, owned_process, load_api
from .records import CaseUnavailable

_password = None
_password_owner = None


class _Ready(Exception):
    """The owned engine became ready while its startup form was being inventoried."""


def controls(window, api, deadline=None, ready=None):
    deadline = time.monotonic() + 15 if deadline is None else deadline
    pending, visited, fields, buttons = [(window, 0)], 0, {}, {}
    while pending:
        if ready is not None:
            ready()
        if time.monotonic() >= deadline:
            raise CaseUnavailable('normal native vault inventory deadline exceeded')
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


def form(api, app, deadline=None, ready=None):
    deadline = time.monotonic() + 15 if deadline is None else deadline
    desktop = api.Registry.getDesktop(0)
    if not 0 <= desktop.childCount <= 256:
        raise ValueError('native accessibility application inventory exceeds its bound')
    matches = []
    for index in range(desktop.childCount):
        if time.monotonic() >= deadline:
            raise CaseUnavailable('normal native vault inventory deadline exceeded')
        if ready is not None:
            ready()
        application = desktop[index]
        pid = application.get_process_id()
        if not owned_process(pid, app):
            continue
        before = process_identity(pid)
        if not 0 <= application.childCount <= 64:
            raise ValueError('installed accessibility window inventory exceeds its bound')
        for offset in range(application.childCount):
            if time.monotonic() >= deadline:
                raise CaseUnavailable('normal native vault inventory deadline exceeded')
            window = application[offset]
            if window.getRole() not in {api.ROLE_FRAME, api.ROLE_DIALOG}:
                continue
            if not window.getState().contains(api.STATE_SHOWING):
                continue
            selected = controls(window, api, deadline, ready)
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
    return {'mode': mode, 'ui_identity': identity, 'password_in_evidence': False, 'normal_action': True}


def native_error(error):
    facts = {'phase': 'vault-form-inventory', 'exception_type': type(error).__name__}
    for name in ('domain', 'code'):
        value = getattr(error, name, None)
        if ((name == 'code' and type(value) is int and 0 <= value < 1 << 31)
                or (name == 'domain' and isinstance(value, str)
                    and re.fullmatch(r'[A-Za-z0-9_.:-]{1,128}', value))):
            facts[name] = value
    if 'domain' in facts:
        try:
            from gi.repository import Gio
            remote = Gio.DBusError.get_remote_error(error)
            if isinstance(remote, str) and re.fullmatch(r'[A-Za-z0-9_.]{1,128}', remote):
                facts['remote_error'] = remote
        except (ImportError, TypeError, AttributeError):
            pass
    frames, frame = [], error.__traceback__
    while frame is not None and len(frames) < 8:
        frames.append({'file': Path(frame.tb_frame.f_code.co_filename).name,
                       'function': frame.tb_frame.f_code.co_name, 'line': frame.tb_lineno})
        frame = frame.tb_next
    facts['stack'] = frames
    return facts


def onboard(app):
    deadline, acted, api = time.monotonic() + min(app.timeout, 60), False, None
    owner = (os.getuid(), str(Path.home()))
    next_readiness = 0.0

    def ready_during_inventory():
        nonlocal next_readiness
        now = time.monotonic()
        if now >= next_readiness:
            next_readiness = now + 0.1
            if app.try_ready():
                raise _Ready()

    with accessibility_bus(app.observations['session']):
        while time.monotonic() < deadline:
            if app.try_ready():
                return
            if api is None:
                try:
                    api = load_api()
                except CaseUnavailable:
                    time.sleep(0.1)
                    continue
            if not acted:
                try:
                    selected = form(api, app, deadline, ready_during_inventory)
                    if selected:
                        app.observations['onboarding'] = complete(selected, owner)
                        acted = True
                except _Ready:
                    return
                except IndexError as error:
                    app.observations['native_accessibility_last_retry'] = native_error(error)
                    key = 'native_accessibility_inventory_retries'
                    app.observations[key] = app.observations.get(key, 0) + 1
                except (ValueError, CaseUnavailable):
                    raise
                except Exception as error:
                    facts = native_error(error)
                    if facts.get('domain') in {'atspi_error', 'g-dbus-error-quark', 'g-io-error-quark'} and type(facts.get('code')) is int:
                        app.observations['native_accessibility_last_retry'] = facts
                        key = 'native_accessibility_inventory_retries'
                        app.observations[key] = app.observations.get(key, 0) + 1
                    else:
                        app.observations['native_accessibility_error'] = facts
                        raise CaseUnavailable('normal native vault accessibility is unavailable') from None
            time.sleep(0.1)
    app.require_running()
    raise CaseUnavailable('installed application did not finish its normal startup interaction')
