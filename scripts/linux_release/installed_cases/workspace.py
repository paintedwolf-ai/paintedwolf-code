"""Exercise an owned fixture through the installed native file editor."""
from __future__ import annotations

import hashlib
import os
from pathlib import Path
import stat
import tempfile
import time

from .app import InstalledApp, process_identity
from .desktop_ui import DesktopUI, interactive_window, owned_process
from .records import CaseFailed, CaseUnavailable, retain
from .process import run

BEFORE = 'Installed editor fixture.\nFind the qualification needle.\n'
AFTER = 'Installed editor fixture changed through the native UI.\nFind the qualification needle.\n'


def observed_file(path: Path) -> dict:
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        before = os.fstat(descriptor)
        if not stat.S_ISREG(before.st_mode) or before.st_uid != os.getuid() or before.st_size > 65536:
            raise ValueError('installed editor fixture lost its bounded owned-file identity')
        raw = os.read(descriptor, 65537)
        after = os.fstat(descriptor)
        identity = lambda value: (value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns)
        if identity(before) != identity(after) or identity(after) != identity(path.lstat()):
            raise ValueError('installed editor fixture changed during observation')
        return {'sha256': hashlib.sha256(raw).hexdigest(), 'bytes': len(raw),
                'expected_content': raw == AFTER.encode(), 'regular_owned_file': True}
    finally:
        os.close(descriptor)


def fixed_editor_keyboard(app, ui, path: Path, native=None) -> dict:
    if native is None:
        import gi
        gi.require_version('Atspi', '2.0')
        from gi.repository import Atspi
        native = Atspi
    node = ui.find(ui.api.ROLE_ENTRY, 'Contents of ' + path.name)
    pid = node.get_process_id()
    identity = process_identity(pid)

    def require_focus():
        if (process_identity(pid) != identity or not owned_process(pid, app)
                or not ui.visible(node) or not node.getState().contains(ui.api.STATE_FOCUSED)):
            raise CaseUnavailable('normal-owned-editor-focus-unavailable')
        frames = [window for owner, window in ui.windows()
                  if owner == identity and window.getState().contains(ui.api.STATE_ACTIVE)]
        if len(frames) != 1:
            raise CaseUnavailable('normal-owned-editor-active-frame-unavailable')

    if node.queryComponent().grabFocus() is not True:
        raise CaseUnavailable('normal-owned-editor-focus-refused')
    require_focus()
    control = 1 << int(native.ModifierType.CONTROL)

    def event(value, text, kind):
        require_focus()
        if native.generate_keyboard_event(value, text, kind) is not True:
            raise CaseUnavailable('normal-editor-keyboard-synthesis-unavailable')
        require_focus()

    def chord(symbol):
        locked = False
        try:
            locked = True
            event(control, None, native.KeySynthType.LOCKMODIFIERS)
            event(ord(symbol), None, native.KeySynthType.SYM)
        finally:
            if locked and native.generate_keyboard_event(control, None, native.KeySynthType.UNLOCKMODIFIERS) is not True:
                raise ValueError('normal editor modifier release failed')
        require_focus()

    chord('a')
    event(0, AFTER, native.KeySynthType.STRING)
    chord('s')
    return {'normal_keyboard_replace_and_save': True, 'focused_entry': True,
            'active_owned_frame': True, 'ui_identity': identity, 'text_readback': False}


def edit_and_find(app, path: Path) -> tuple[dict, dict]:
    observations, streams = {'interactive': interactive_window(app)}, {}
    try:
        with DesktopUI(app) as ui:
            skipped = False
            try:
                ui.click('Skip')
                skipped = True
            except CaseUnavailable:
                pass
            observations['normal_optional_setup_skip'] = skipped
            if skipped:
                ui.find(ui.api.ROLE_PUSH_BUTTON, 'Open a folder…')
            opened = run(app.binding, 'pw', ['open', str(path.parent)], timeout=30)
            observations['project_open'] = {key: value for key, value in opened.items() if key not in {'stdout', 'stderr'}}
            streams = {'project-open-' + name: opened[name][:512 << 10] for name in ('stdout', 'stderr')}
            truncated = any(len(opened[name]) > 512 << 10 for name in ('stdout', 'stderr'))
            observations['project_open']['retained_output_truncated'] = truncated
            if opened['exit_code'] or opened['timed_out'] or opened['output_limit'] or truncated:
                raise CaseFailed('installed CLI refused the owned fixture project')
            ui.click('Files')
            ui.click(path.name)
            try:
                ui.fill(ui.api.ROLE_ENTRY, 'Contents of ' + path.name, AFTER)
            except CaseUnavailable as error:
                if 'desktop_mutation' not in getattr(error, 'observations', {}):
                    raise
                observations.update(error.observations)
            observations['editor_keyboard'] = fixed_editor_keyboard(app, ui, path)
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                disk = observed_file(path)
                if disk['expected_content']:
                    break
                time.sleep(0.1)
            else:
                raise ValueError('normal native editor save did not produce the fixed fixture bytes')
            ui.click('Find in file')
            ui.fill(ui.api.ROLE_ENTRY, 'Editor command or search', 'qualification needle')
        observations.update(saved_file=disk, normal_find_input_action=True,
            remaining_requirements=['search-result-and-navigation-witness', 'applied-edit-review-interaction',
                                    'owned-project-cleanup'])
        return observations, streams
    except (OSError, ValueError, TypeError) as error:
        error.observations = {**observations, **getattr(error, 'observations', {})}
        error.streams = {**streams, **getattr(error, 'streams', {})}
        raise
    except Exception as error:
        refusal = CaseUnavailable('normal-native-editor-interaction-unavailable')
        refusal.observations = {**observations, 'native_editor_exception_type': type(error).__name__}
        refusal.streams = streams
        raise refusal from None


def fixture_path(root: Path) -> Path:
    base = root / 'workspace-fixtures'
    base.mkdir(mode=0o700, exist_ok=True)
    identity = base.lstat()
    if (not stat.S_ISDIR(identity.st_mode) or identity.st_uid != os.getuid()
            or identity.st_mode & 0o077 or base.resolve() != base):
        raise ValueError('editor fixture requires its physical private output directory')
    directory = Path(tempfile.mkdtemp(prefix='editor-', dir=base))
    path = directory / 'qualification-雪.txt'
    path.write_text(BEFORE)
    return path


def execute(binding: dict, capability: str, case: str, root: Path) -> dict:
    if binding.get('case_inputs', {}).get('desktop', {}) not in ({}, {'format_version': 1}):
        raise ValueError('workspace driver refuses arbitrary fixture paths and command hooks')
    if (capability, case) != ('files', 'edit-search-review'):
        return retain(binding, capability, case, root, 'unverified',
                      {'reason': 'remaining-native-workspace-interaction-required'})
    path = fixture_path(root)
    app = InstalledApp(binding)
    app.observations['fixture_root'] = str(path.parent)
    try:
        with app:
            observations, streams = edit_and_find(app, path)
    except (OSError, ValueError, TypeError) as error:
        app.stop()
        error.observations = {**app.observations, **getattr(error, 'observations', {})}
        error.streams = {**app.streams(), **getattr(error, 'streams', {})}
        raise
    finally:
        app.stop()
    observations['fixture_root'] = str(path.parent)
    return retain(binding, capability, case, root, 'unverified', observations, {**app.streams(), **streams})
