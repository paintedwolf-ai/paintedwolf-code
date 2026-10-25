"""Create the public managed-secret fixture once through the owned desktop."""
from __future__ import annotations

import time

from .desktop_turn import open_project_configuration
from .presence_alert import alerts
from .provider_setup import physical_input
from .records import CaseUnavailable
from ..installed_input.presence_fixture import FIELDS


def prepare_secret(app, ui, reservation, project, selected) -> dict:
    if 'presence_secret_setup' in app.observations:
        raise CaseUnavailable('presence-secret-setup-already-attempted')
    witness = {'phase': 'navigation', 'create_action_completed': False, 'reveal_ready': False}
    app.observations['presence_secret_setup'] = witness
    reservation.check_launch(app)
    witness['navigation'] = open_project_configuration(app, ui, project, selected)
    witness['phase'] = 'secret-list'
    ui.click('Secrets', tab=True)
    witness['phase'] = 'secret-form'
    ui.click('Add secret')
    for index, (label, operation, _, password) in enumerate(FIELDS):
        witness['phase'], witness['field_index'] = 'fixture-input', index
        reservation.check_launch(app)
        physical_input(app, ui, label, operation, password=password)
    reservation.check_launch(app)
    witness['phase'] = 'create-dispatch'
    # An uncertain create must end the attempt, never replay the transaction.
    ui.click('Add secret')
    witness['create_action_completed'], witness['phase'] = True, 'reveal-observation'
    ui.find(ui.api.ROLE_PUSH_BUTTON, 'Reveal value', deadline=time.monotonic() + 15)
    reservation.check_launch(app)
    if alerts(app, ui, time.monotonic() + 15):
        witness['phase'] = 'existing-alert'
        raise CaseUnavailable('presence-reveal-prerequisite-already-has-an-owned-alert')
    witness.update(phase='ready', reveal_ready=True)
    return witness
