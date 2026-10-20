"""Exercise an owned fixture through the installed native file editor."""
from __future__ import annotations

import hashlib
import os
from pathlib import Path
import stat
import tempfile
import time

from .app import InstalledApp
from .desktop_ui import DesktopUI, interactive_window
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
            opened = run(app.binding, 'pw', ['open', str(path.parent)], timeout=30)
            observations['project_open'] = {key: value for key, value in opened.items() if key not in {'stdout', 'stderr'}}
            streams = {'project-open-' + name: opened[name][:512 << 10] for name in ('stdout', 'stderr')}
            truncated = any(len(opened[name]) > 512 << 10 for name in ('stdout', 'stderr'))
            observations['project_open']['retained_output_truncated'] = truncated
            if opened['exit_code'] or opened['timed_out'] or opened['output_limit'] or truncated:
                raise CaseFailed('installed CLI refused the owned fixture project')
            ui.click('Files')
            ui.click(path.name)
            ui.fill(ui.api.ROLE_TEXT, 'Contents of ' + path.name, AFTER)
            ui.click('Save')
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


def fixture_path(root: Path) -> Path:
    base = root.parent / 'workspace-fixtures'
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
