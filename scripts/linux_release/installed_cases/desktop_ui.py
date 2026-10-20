"""Bound normal desktop actions to the captured installed application's live windows."""
from __future__ import annotations

from contextlib import contextmanager
import os
import time

from .app import process_identity, session_environment
from .records import CaseFailed, CaseUnavailable


def owned_process(pid: int, app) -> bool:
    if type(pid) is not int or pid <= 1:
        return False
    root, seen = app.observations['launch_identity'], set()
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


def load_api():
    try:
        import pyatspi
        from gi.repository import Atspi
        Atspi.set_timeout(1000, 5000)
        return pyatspi
    except (ImportError, AttributeError):
        raise CaseUnavailable('normal-native-accessibility-client-unavailable') from None


class DesktopUI:
    def __init__(self, app, api=None):
        self.app, self.api = app, api
        self.environment = accessibility_bus(app.observations['session'])

    def __enter__(self):
        self.environment.__enter__()
        try:
            self.api = self.api or load_api()
            return self
        except BaseException:
            self.environment.__exit__(None, None, None)
            raise

    def __exit__(self, kind, value, traceback):
        return self.environment.__exit__(kind, value, traceback)

    def visible(self, node) -> bool:
        state = node.getState()
        return (not state.contains(self.api.STATE_DEFUNCT) and state.contains(self.api.STATE_SHOWING)
                and state.contains(self.api.STATE_SENSITIVE) and state.contains(self.api.STATE_ENABLED))

    def windows(self, deadline=None):
        deadline = time.monotonic() + 15 if deadline is None else deadline
        desktop = self.api.Registry.getDesktop(0)
        if not 0 <= desktop.childCount <= 256:
            raise ValueError('native accessibility application inventory exceeds its bound')
        for index in range(desktop.childCount):
            if time.monotonic() >= deadline:
                raise CaseUnavailable('normal-native-window-observation-deadline')
            application = desktop[index]
            pid = application.get_process_id()
            if not owned_process(pid, self.app):
                continue
            identity = process_identity(pid)
            if not 0 <= application.childCount <= 64:
                raise ValueError('installed window inventory exceeds its bound')
            for offset in range(application.childCount):
                if time.monotonic() >= deadline:
                    raise CaseUnavailable('normal-native-window-observation-deadline')
                window = application[offset]
                if window.getRole() in {self.api.ROLE_FRAME, self.api.ROLE_DIALOG} and self.visible(window):
                    yield identity, window
            if process_identity(pid) != identity or not owned_process(pid, self.app):
                raise ValueError('installed desktop owner changed during window observation')

    def descendants(self, window, deadline=None):
        deadline = time.monotonic() + 15 if deadline is None else deadline
        pending, visited = [(window, 0)], 0
        while pending:
            if time.monotonic() >= deadline:
                raise CaseUnavailable('normal-native-control-observation-deadline')
            node, depth = pending.pop()
            visited += 1
            count = node.childCount
            if type(count) is not int or visited > 4096 or depth > 64 or not 0 <= count <= 1024:
                error = CaseUnavailable('installed accessible window exceeds its fixed observer traversal bound')
                error.observations = {'desktop_inventory_bound': {'visited': visited, 'depth': depth,
                    'child_count': count if type(count) is int else None,
                    'limits': {'visited': 4096, 'depth': 64, 'child_count': 1024}}}
                raise error
            yield node
            pending.extend((node[index], depth + 1) for index in range(count))

    def find(self, role: int, name: str):
        if not isinstance(name, str) or not 0 < len(name) <= 256:
            raise ValueError('desktop action requires an exact bounded control name')
        matches, observed_roles, deadline = [], set(), time.monotonic() + 15
        while self.app.process.poll() is None and time.monotonic() < deadline:
            matches = []
            try:
                for identity, window in self.windows(deadline):
                    for node in self.descendants(window, deadline):
                        node_role = node.getRole()
                        if self.visible(node) and node.name == name:
                            observed_roles.add(int(node_role))
                            if node_role == role:
                                matches.append((identity, node))
            except CaseUnavailable as error:
                error.observations = {**getattr(error, 'observations', {}), 'desktop_control': {
                    'role': int(role), 'name': name, 'match_count': len(matches),
                    'inventory_complete': False, 'observed_roles': sorted(observed_roles)}}
                raise
            except (ValueError, TypeError):
                raise
            except Exception as error:
                domain, code = getattr(error, 'domain', None), getattr(error, 'code', None)
                native = (isinstance(domain, str) and domain in {'atspi_error', 'g-dbus-error-quark', 'g-io-error-quark'}
                          and type(code) is int and 0 <= code < 1 << 31)
                if isinstance(error, IndexError) or native:
                    self.app.observations['desktop_inventory_retry'] = {
                        'exception_type': type(error).__name__,
                        **({'domain': domain, 'code': code} if native else {})}
                    time.sleep(0.05)
                    continue
                refusal = CaseUnavailable('normal-native-control-inventory-unavailable')
                refusal.observations = {'desktop_control': {'role': int(role), 'name': name},
                                        'exception_type': type(error).__name__}
                raise refusal from None
            if len(matches) > 1:
                break
            if len(matches) == 1:
                identity, node = matches[0]
                if process_identity(identity['pid']) != identity or not owned_process(identity['pid'], self.app):
                    raise ValueError('installed control owner changed before its normal action')
                return node
            time.sleep(0.05)
        code = self.app.process.poll()
        if code is not None:
            error = CaseFailed('selected installed desktop exited before its required control')
            error.observations = {'launcher_exit_code': code}
            raise error
        error = CaseUnavailable('normal-native-control-is-missing-or-ambiguous')
        error.observations = {'desktop_control': {'role': int(role), 'name': name, 'match_count': len(matches),
            'observed_roles': sorted(observed_roles)}}
        raise error

    def click(self, name: str):
        node = self.find(self.api.ROLE_PUSH_BUTTON, name)
        try:
            action = node.queryAction()
            if action.nActions != 1 or action.doAction(0) is not True:
                raise ValueError('normal native button refused its action')
        except Exception:
            raise CaseUnavailable('normal-native-button-action-unavailable') from None

    def fill(self, role: int, name: str, value: str):
        node = self.find(role, name)
        if not isinstance(value, str) or len(value.encode()) > 65536:
            raise ValueError('normal native editable input exceeds its fixture bound')
        phase = 'editable-interface'
        try:
            editable = node.queryEditableText()
            phase = 'set-fixed-content'
            if editable.setTextContents(value) is not True:
                phase = 'set-fixed-content-refused'
                raise ValueError('normal native editable input refused its content')
        except Exception as error:
            refusal = CaseUnavailable('normal-native-editable-action-unavailable')
            refusal.observations = {'desktop_mutation': {'phase': phase, 'exception_type': type(error).__name__}}
            raise refusal from None

    def interactive(self) -> dict | None:
        controls = {self.api.ROLE_PUSH_BUTTON, self.api.ROLE_ENTRY, self.api.ROLE_PASSWORD_TEXT}
        decoration = {'Close', 'Minimize', 'Maximize', 'Restore'}
        deadline = time.monotonic() + 15
        for identity, window in self.windows(deadline):
            if window.getRole() != self.api.ROLE_FRAME:
                continue
            for node in self.descendants(window, deadline):
                role = node.getRole()
                if role in controls and self.visible(node) and node.name and node.name not in decoration:
                    if role == self.api.ROLE_PUSH_BUTTON and node.queryAction().nActions != 1:
                        continue
                    if process_identity(identity['pid']) != identity:
                        raise ValueError('interactive desktop owner changed during observation')
                    return {'ui_identity': identity, 'frame_role': int(window.getRole()),
                            'control_role': int(role), 'visible': True, 'sensitive': True,
                            'defunct': False, 'normal_actionable_control': True}
        return None


def interactive_window(app, timeout=30) -> dict:
    if type(timeout) not in {int, float} or not 0 < timeout <= 60:
        raise ValueError('interactive desktop observation requires a bounded timeout')
    deadline = time.monotonic() + timeout
    try:
        with DesktopUI(app) as ui:
            while app.process.poll() is None and time.monotonic() < deadline:
                witness = ui.interactive()
                if witness is not None:
                    return witness
                time.sleep(0.05)
    except (ValueError, CaseUnavailable):
        raise
    except Exception:
        raise CaseUnavailable('normal-interactive-desktop-observation-unavailable') from None
    code = app.process.poll()
    if code is not None:
        failure = CaseFailed('selected installed desktop exited before interactive readiness')
        failure.observations = {'launcher_exit_code': code, 'interactive_window_ready': False}
        raise failure
    raise CaseUnavailable('installed-desktop-did-not-become-interactive')
