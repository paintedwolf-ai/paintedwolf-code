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
from .records import CaseUnavailable, retain

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


def edit_and_find(app, path: Path) -> dict:
    interactive = interactive_window(app)
    with DesktopUI(app) as ui:
        skipped = False
        try:
            ui.click('Skip')
            skipped = True
        except CaseUnavailable:
            pass
        ui.fill(ui.api.ROLE_TEXT, 'Contents of ' + str(path), AFTER)
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
    return {'interactive': interactive, 'saved_file': disk,
            'normal_find_input_action': True, 'normal_optional_setup_skip': skipped,
            'remaining_requirements': ['search-result-and-navigation-witness', 'applied-edit-review-interaction']}


def execute(binding: dict, capability: str, case: str, root: Path) -> dict:
    if binding.get('case_inputs', {}).get('desktop', {}) not in ({}, {'format_version': 1}):
        raise ValueError('workspace driver refuses arbitrary fixture paths and command hooks')
    if (capability, case) != ('files', 'edit-search-review'):
        return retain(binding, capability, case, root, 'unverified',
                      {'reason': 'remaining-native-workspace-interaction-required'})
    with tempfile.TemporaryDirectory(prefix='paintedwolf-installed-editor-') as directory:
        path = Path(directory) / 'qualification-雪.txt'
        path.write_text(BEFORE)
        app = InstalledApp(binding, arguments=[str(path)])
        try:
            with app:
                observations = edit_and_find(app, path)
        except (OSError, ValueError, TypeError) as error:
            app.stop()
            error.observations = {**app.observations, **getattr(error, 'observations', {})}
            error.streams = app.streams()
            raise
        finally:
            app.stop()
    return retain(binding, capability, case, root, 'unverified', observations, app.streams())
