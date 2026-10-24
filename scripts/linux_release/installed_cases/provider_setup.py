"""Configure the fixed loopback provider through normal native desktop input."""
import time

from .desktop_ui import DesktopUI
from .app import process_identity
from .profile_providers import find_gui_provider
from .profile_setup import state as setup_state
from .provider import MODEL, GUI_LABEL
from .records import CaseUnavailable
from ..installed_input.focus import EntryFocus
from ..installed_input import accessibility, lineage
from ..installed_input.guest import perform
from ..installed_input.protocol import endpoint_operation


def physical_input(app, ui, label, operation, *, password=False):
    native = app.binding.get('native_input')
    if not isinstance(native, dict):
        raise CaseUnavailable('protected-reply-requires-fixed-physical-input')
    record = perform(native['binding'], native['vm_id'], EntryFocus(app, ui, label, password=password), operation=operation)
    app.observations.setdefault('fixed_input', []).append(record)
    return record


def await_discovery(app, fixture):
    deadline, witness = time.monotonic() + 90, None
    while time.monotonic() < deadline:
        app.require_running()
        witness = find_gui_provider(app, fixture.base_url)
        discovered = any(row.get('path') == '/v1/model/info' and row.get('model') == MODEL
                         and row.get('fixture_credential_observed') is True
                         for row in fixture.discovery_observations)
        if witness is not None and discovered:
            app.observations['provider_registration'] = witness
            app.observations['provider_discovery'] = list(fixture.discovery_observations)
            return witness
        time.sleep(0.1)
    refusal = CaseUnavailable('fixed-provider-current-discovery-or-registration-unavailable')
    refusal.observations = {'provider_discovery': list(fixture.discovery_observations),
                           'provider_registration': witness}
    raise refusal



def select_default_model(app, ui):
    # WebKit maps the dialog trigger to a combo box and aria-pressed options to toggle buttons.
    controls = [(ui.api.ROLE_COMBO_BOX, 'Default model', 'select'),
                (ui.api.ROLE_TOGGLE_BUTTON, f'{GUI_LABEL} — {MODEL}', None)]
    root = app.observations['launch_identity']
    actions = app.observations.setdefault('provider_model_actions', [])
    for role, name, expected_action in controls:
        node = ui.find(role, name)
        frame, chain = accessibility.capture(node, list(ui.windows()))
        control = process_identity(node.get_process_id())
        surface = process_identity(frame.get_process_id())
        entry_chain = lineage.capture(surface, control)
        frame_chain = lineage.capture(root, surface)
        action = node.queryAction()
        if action.nActions != 1 or (expected_action is not None and action.getName(0) != expected_action):
            raise CaseUnavailable('normal-model-picker-action-is-not-unique')
        if (accessibility.capture(node, list(ui.windows()))[1] != chain
                or lineage.capture(surface, control) != entry_chain
                or lineage.capture(root, surface) != frame_chain):
            raise ValueError('normal model picker identity changed before its action')
        if action.doAction(0) is not True:
            raise CaseUnavailable('normal-model-picker-action-refused')
        # Selecting an option closes its popup; the captured native surface and launch must survive.
        if lineage.capture(root, surface) != frame_chain or lineage.capture(surface, control) != entry_chain:
            raise ValueError('normal model picker process identity changed after its action')
        actions.append({'role': int(role), 'name': name, 'accessible_chain': chain,
                        'entry_lineage': entry_chain, 'frame_lineage': frame_chain,
                        'native_action_completed': True})
    app.observations['provider_model_selection'] = {'model': MODEL, 'native_action_completed': True}


def configure_provider(app, fixture):
    # Existing configured fixtures must keep their exact admitted endpoint and credential transport.
    existing = find_gui_provider(app, fixture.base_url)
    configured = setup_state(app)
    if configured['onboarding_completed']:
        if existing is None or configured['coordinator'] != {'provider_id': existing['provider_id'], 'model': MODEL}:
            raise CaseUnavailable('configured-profile-does-not-select-the-fixed-provider-model')
        app.observations['provider_setup_state'] = configured
        return await_discovery(app, fixture)
    with DesktopUI(app) as ui:
        if existing is None:
            ui.click('Add AI provider')
            ui.click('LiteLLM')
            # WebKit exposes implicit labels and placeholders as one accessible name.
            physical_input(app, ui, 'Name (optional) e.g. GPU box, Work account', 'provider-name-v1')
            physical_input(app, ui, 'Endpoint URL http://localhost:4000/v1', endpoint_operation(fixture.server.server_port))
            physical_input(app, ui, 'API key Paste your API key…', 'provider-fixture-credential-v1', password=True)
            ui.click('Add AI provider')
        witness = await_discovery(app, fixture)
        if existing is None:
            ui.click('AI providers')
        select_default_model(app, ui)
        ui.click('Skip')
    return witness
