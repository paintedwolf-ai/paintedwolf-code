"""Bound normal desktop actions to the captured installed application's live windows."""
from __future__ import annotations

import time

from .. import scripts_module
from ..installed_input import accessibility
from .app import process_identity, session_environment
from .records import CaseFailed, CaseUnavailable

_InventoryChanged = accessibility.InventoryChanged


def retry_inventory(app, error) -> bool:
    domain, code = getattr(error, 'domain', None), getattr(error, 'code', None)
    native = (isinstance(domain, str) and domain in {'atspi_error', 'g-dbus-error-quark', 'g-io-error-quark'}
              and type(code) is int and 0 <= code < 1 << 31)
    if not isinstance(error, (IndexError, _InventoryChanged, NotImplementedError, AttributeError)) and not native:
        return False
    expected = app.observations['launch_identity']
    try:
        actual = process_identity(expected['pid'])
    except (FileNotFoundError, ProcessLookupError):
        raise CaseFailed('captured installed desktop disappeared during native observation') from None
    if actual != expected:
        raise CaseFailed('captured installed desktop identity changed during native observation')
    app.observations['desktop_inventory_retry'] = {'exception_type': type(error).__name__,
        **({'domain': domain, 'code': code} if native else {})}
    return True


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


def accessibility_bus(session):
    return scripts_module('harness.atspi_client').session_bus(session_environment(session))


def load_api():
    try:
        scripts_module('harness.atspi_client').load_atspi(1000, 5000)
        import pyatspi
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
        while time.monotonic() < deadline:
            try:
                snapshot = self._window_snapshot(deadline)
                break
            except (ValueError, TypeError):
                raise
            except Exception as error:
                if not retry_inventory(self.app, error):
                    raise
                time.sleep(0.05)
        else:
            raise CaseUnavailable('normal-native-window-observation-deadline')
        # Publish once, after the entire inventory and its owners were checked.
        yield from snapshot

    def _window_snapshot(self, deadline):
        desktop = self.api.Registry.getDesktop(0)
        application_count = desktop.childCount
        if type(application_count) is not int or not 0 <= application_count <= 256:
            raise ValueError('native accessibility application inventory exceeds its bound')
        snapshot = []
        for index in range(application_count):
            if time.monotonic() >= deadline:
                raise CaseUnavailable('normal-native-window-observation-deadline')
            application = desktop[index]
            if application is None:
                raise _InventoryChanged()
            pid = application.get_process_id()
            if not owned_process(pid, self.app):
                continue
            identity = process_identity(pid)
            window_count = application.childCount
            if type(window_count) is not int or not 0 <= window_count <= 64:
                raise ValueError('installed window inventory exceeds its bound')
            for offset in range(window_count):
                if time.monotonic() >= deadline:
                    raise CaseUnavailable('normal-native-window-observation-deadline')
                window = application[offset]
                if window is None:
                    raise _InventoryChanged()
                if window.getRole() in {self.api.ROLE_FRAME, self.api.ROLE_DIALOG} and self.visible(window):
                    snapshot.append((identity, window))
            if process_identity(pid) != identity or not owned_process(pid, self.app):
                raise ValueError('installed desktop owner changed during window observation')
            if application.childCount != window_count:
                raise _InventoryChanged()
        if desktop.childCount != application_count:
            raise _InventoryChanged()
        launch = self.app.observations['launch_identity']
        if process_identity(launch['pid']) != launch:
            raise CaseFailed('captured installed desktop identity changed during window observation')
        return snapshot

    def descendants(self, window, deadline=None, *, include=None):
        return accessibility.descendants(window, deadline, include=include)

    def find(self, role: int, name: str):
        if not isinstance(name, str) or not 0 < len(name) <= 256:
            raise ValueError('desktop action requires an exact bounded control name')
        matches, observed_roles, deadline = [], set(), time.monotonic() + 15
        while self.app.process.poll() is None and time.monotonic() < deadline:
            matches = []
            try:
                for identity, window in self.windows(deadline):
                    for node in self.descendants(window, deadline):
                        if node.name == name and self.visible(node):
                            node_role = node.getRole()
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
                if retry_inventory(self.app, error):
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

    def click(self, name: str, *, tab=False):
        if type(tab) is not bool:
            raise ValueError('normal native action requires an explicit button or page tab')
        role = self.api.ROLE_PAGE_TAB if tab else self.api.ROLE_PUSH_BUTTON
        deadline = time.monotonic() + 15
        while True:
            node = self.find(role, name)
            try:
                action = node.queryAction()
                if action.nActions != 1 or action.doAction(0) is not True:
                    raise ValueError('normal native button refused its action')
                return
            except ValueError:
                raise CaseUnavailable('normal-native-button-action-unavailable') from None
            except Exception as error:
                if time.monotonic() < deadline and retry_inventory(self.app, error):
                    time.sleep(0.05)
                    continue
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

    def interactive(self, deadline=None) -> dict | None:
        controls = {self.api.ROLE_PUSH_BUTTON, self.api.ROLE_ENTRY, self.api.ROLE_PASSWORD_TEXT}
        decoration = {'Close', 'Minimize', 'Maximize', 'Restore'}
        deadline = time.monotonic() + 15 if deadline is None else deadline
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
                try:
                    witness = ui.interactive(deadline)
                except (ValueError, CaseUnavailable):
                    raise
                except Exception as error:
                    if retry_inventory(app, error):
                        time.sleep(0.05)
                        continue
                    raise
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
