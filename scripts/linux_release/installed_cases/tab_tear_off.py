"""Tear a file tab off the installed app with physical pointer input.

On a compositor that advertises xdg_toplevel_drag_manager_v1, a released tab
must leave its strip for a new window and Escape must keep it in place. Without
the protocol, both tear off: that is Den's documented platform difference, and
the case qualifies it as such rather than as macOS parity.
"""
from __future__ import annotations

import time
from pathlib import Path

from .app import InstalledApp, process_identity, session_environment
from .desktop_ui import DesktopUI
from .records import CaseFailed, CaseUnavailable, retain
from .process import run
from . import wayland_globals
from ..installed_input.protocol import tear_off_operation

CASE = ('windowing', 'file-tab-tear-off')
PROTOCOL = 'xdg_toplevel_drag_manager_v1'
SETTLE_SECONDS = 10


def expected_outcomes(globals_: dict) -> dict:
    """Whether each ending leaves the tab torn off, from the compositor's structured facts."""
    live = PROTOCOL in globals_
    return {'mode': 'live' if live else 'fallback', 'drop': True, 'escape': not live}


def evaluate(expected: dict, observed: dict) -> list[str]:
    """Differences between the observed endings and the expected ones; empty when the case passes."""
    problems = []
    for ending in ('escape', 'drop'):
        row = observed.get(ending)
        if row is None:
            problems.append(f'{ending}: not observed')
            continue
        torn = row['torn_windows'] == 1 and not row['tab_in_strip']
        kept = row['torn_windows'] == 0 and row['tab_in_strip']
        if expected[ending] and not torn:
            problems.append(f'{ending}: expected the tab in one new window')
        if not expected[ending] and not kept:
            problems.append(f'{ending}: expected the tab to stay in its strip')
    return problems


def physical(point: tuple[float, float], mode: list[int], logical: list[int]) -> tuple[int, int]:
    x = int(point[0] * mode[0] / logical[0])
    y = int(point[1] * mode[1] / logical[1])
    if not (0 <= x < mode[0] and 0 <= y < mode[1]):
        raise ValueError('file tab is outside the fullscreen display')
    return x, y


class FullscreenFrame:
    """The pointer proof: the launched app's visible frame still fills the display."""

    def __init__(self, app, ui, frame, logical: list[int]):
        self.app, self.ui, self.frame, self.logical = app, ui, frame, logical
        self.launch = dict(app.observations['launch_identity'])

    def observe(self) -> dict:
        self.app.require_running()
        if process_identity(self.launch['pid']) != self.launch:
            raise CaseFailed('selected installed application changed during the pointer interaction')
        extents = self.frame.queryComponent().getExtents(self.ui.api.CoordType.WINDOW)
        return {'launch': self.launch, 'frame': process_identity(self.frame.get_process_id()),
                'frame_visible': bool(self.ui.visible(self.frame)),
                'frame_fullscreen': [extents.width, extents.height] == list(self.logical)}


def tab_nodes(ui, name: str):
    """Visible file tabs for `name`, by owning frame."""
    found = []
    for _, window in ui.windows():
        for node in ui.descendants(window):
            label = node.name or ''
            if node.getRole() == ui.api.ROLE_PAGE_TAB and ui.visible(node) and (label == name or label.startswith(name + ', ')):
                found.append((window, node))
    return found


def windows_titled(ui, name: str) -> int:
    return sum(1 for _, window in ui.windows() if (window.name or '').startswith(name + ' — Window '))


def activate(ui, role: int, name: str):
    node = ui.find(role, name)
    action = node.queryAction()
    if action.nActions < 1 or action.doAction(0) is not True:
        raise CaseUnavailable('normal-native-control-action-unavailable')


def settle(ui, frame, name: str, *, torn_before: int) -> dict:
    deadline = time.monotonic() + SETTLE_SECONDS
    while True:
        tabs = tab_nodes(ui, name)
        row = {'torn_windows': windows_titled(ui, name) - torn_before,
               'tab_in_strip': any(window == frame for window, _ in tabs)}
        if row['torn_windows'] or time.monotonic() >= deadline:
            return row
        time.sleep(0.2)


def drag(app, ui, native: dict, frame, path: Path, facts: dict, ending: str) -> dict:
    tabs = [node for window, node in tab_nodes(ui, path.name) if window == frame]
    if len(tabs) != 1:
        raise CaseUnavailable('file-tab-is-missing-or-ambiguous')
    extents = tabs[0].queryComponent().getExtents(ui.api.CoordType.WINDOW)
    logical, mode = facts['logical_size'], facts['mode']
    start = physical((extents.x + extents.width / 2, extents.y + extents.height / 2), mode, logical)
    # The middle of the lower content area: inside the initiator, outside every tab strip.
    end = physical((logical[0] / 2, logical[1] * 3 / 4), mode, logical)
    operation = tear_off_operation(ending, start, end, tuple(mode))
    torn_before = windows_titled(ui, path.name)
    from ..installed_input.guest import perform
    delivery = perform(native['binding'], native['vm_id'], FullscreenFrame(app, ui, frame, logical), operation=operation)
    return {'operation': operation, 'native_input': delivery, **settle(ui, frame, path.name, torn_before=torn_before)}


def execute(binding: dict, capability: str, case: str, root: Path) -> dict:
    if (capability, case) != CASE:
        return retain(binding, capability, case, root, 'unverified', {'reason': 'native-desktop-interaction-driver-required'})
    native = binding.get('native_input')
    if native is None:
        return retain(binding, capability, case, root, 'unverified', {'reason': 'fixed-physical-pointer-required'})
    from .workspace import fixture_path
    path = fixture_path(root)
    observations, streams, status = {}, {}, 'failed'
    app = InstalledApp(binding)
    try:
        with app:
            session = app.observations['session']
            if session.get('Type') != 'wayland':
                raise CaseUnavailable('x11-display-geometry-observer-required')
            facts = wayland_globals.observe(session_environment(session))
            observations['compositor'] = facts
            if facts['outputs'] != 1 or not facts['mode'] or not facts['logical_size']:
                raise CaseUnavailable('single-output-display-geometry-required')
            expected = expected_outcomes(facts['globals'])
            observations['expected'] = expected
            with DesktopUI(app) as ui:
                try:
                    ui.click('Skip')
                except CaseUnavailable:
                    pass
                opened = run(binding, 'pw', ['open', str(path.parent)], timeout=30)
                streams = {'project-open-' + name: opened[name][:512 << 10] for name in ('stdout', 'stderr')}
                if opened['exit_code'] or opened['timed_out'] or opened['output_limit']:
                    raise CaseFailed('installed CLI refused the owned fixture project')
                ui.click('Files')
                ui.click(path.name)
                frame = next(window for window, _ in tab_nodes(ui, path.name))
                ui.click('App menu')
                activate(ui, ui.api.ROLE_MENU_ITEM, 'Enter full screen')
                proof = FullscreenFrame(app, ui, frame, facts['logical_size'])
                deadline = time.monotonic() + 10
                while not proof.observe()['frame_fullscreen']:
                    if time.monotonic() >= deadline:
                        raise CaseUnavailable('fullscreen-frame-not-observed')
                    time.sleep(0.1)
                observed = {}
                for ending in ('escape', 'drop'):
                    if not any(window == frame for window, _ in tab_nodes(ui, path.name)):
                        ui.click(path.name)
                    observed[ending] = drag(app, ui, native, frame, path, facts, ending)
                observations['observed'] = observed
                problems = evaluate(expected, observed)
                observations['problems'] = problems
                status = 'passed' if not problems else 'failed'
    except CaseUnavailable as error:
        observations.update(getattr(error, 'observations', {}), reason=str(error))
        status = 'unverified'
    finally:
        app.stop()
        observations['desktop'] = app.observations
    return retain(binding, capability, case, root, status, observations, {**streams, **app.streams()})
