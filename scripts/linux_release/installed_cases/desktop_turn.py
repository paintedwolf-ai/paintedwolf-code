"""Submit a closed fixture operation through the actual project composer."""
from __future__ import annotations

from .desktop_ui import DesktopUI
from .provider_setup import physical_input


def submit_project_turn(app, *, operation='reply-fixture-prompt-v1'):
    if operation != 'reply-fixture-prompt-v1':
        raise ValueError('project submission requires a declared fixture operation')
    with DesktopUI(app) as ui:
        record = physical_input(app, ui, 'What would you like to work on?', operation)
        ui.click('Send message')
    return record
