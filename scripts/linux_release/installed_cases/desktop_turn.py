"""Submit a closed fixture operation through the actual project composer."""
from __future__ import annotations

import time

from .desktop_ui import DesktopUI
from .profile_setup import first_time_tips_enabled
from .provider_setup import physical_input
from .records import CaseUnavailable


def prepare_composer(app, ui):
    if not first_time_tips_enabled(app):
        return
    ui.click('Disable tips')
    deadline = time.monotonic() + 10
    while first_time_tips_enabled(app):
        if time.monotonic() >= deadline:
            raise CaseUnavailable('normal-first-time-tips-disable-did-not-persist')
        time.sleep(0.05)


def submit_project_turn(app, *, operation='reply-fixture-prompt-v1'):
    if operation not in {'reply-fixture-prompt-v1', 'browser-fixture-prompt-v1'}:
        raise ValueError('project submission requires a declared fixture operation')
    with DesktopUI(app) as ui:
        prepare_composer(app, ui)
        record = physical_input(app, ui, 'What would you like to work on?', operation)
        ui.click('Send message')
    return record
