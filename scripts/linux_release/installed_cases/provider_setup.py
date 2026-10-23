"""Configure the fixed loopback provider through normal native desktop input."""
from .desktop_ui import DesktopUI
from .profile_providers import find_gui_provider
from .provider import MODEL
from .records import CaseUnavailable
from ..installed_input.focus import EntryFocus
from ..installed_input.guest import perform
from ..installed_input.protocol import endpoint_operation


def physical_input(app, ui, label, operation, *, password=False):
    native = app.binding.get('native_input')
    if not isinstance(native, dict):
        raise CaseUnavailable('protected-reply-requires-fixed-physical-input')
    record = perform(native['binding'], native['vm_id'], EntryFocus(app, ui, label, password=password), operation=operation)
    app.observations.setdefault('fixed_input', []).append(record)
    return record


def configure_provider(app, fixture):
    with DesktopUI(app) as ui:
        ui.click('Add AI provider')
        ui.click('LiteLLM')
        # WebKit exposes these implicit labels and placeholders as one accessible name.
        physical_input(app, ui, 'Name (optional) e.g. GPU box, Work account', 'provider-name-v1')
        physical_input(app, ui, 'Endpoint URL http://localhost:4000/v1', endpoint_operation(fixture.server.server_port))
        physical_input(app, ui, 'API key Paste your API key…', 'provider-fixture-credential-v1', password=True)
        ui.click('Add AI provider')
        ui.click('Default model')
        ui.click(MODEL)
        # Optional setup can finish without a separate provider test request;
        # the required physical turn below proves the selected provider itself.
        ui.click('Skip')
    witness = find_gui_provider(app, fixture.base_url)
    if witness is None or witness['models'] != [MODEL]:
        raise ValueError('normal GUI did not register the selected fixed provider and model')
    return witness


