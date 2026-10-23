"""Configure the fixed loopback provider through normal native desktop input."""
import time

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


def await_catalog(app, fixture):
    deadline, witness = time.monotonic() + 90, None
    while time.monotonic() < deadline:
        app.require_running()
        witness = find_gui_provider(app, fixture.base_url)
        discovered = any(row.get('path') == '/v1/model/info' and row.get('model') == MODEL
                         and row.get('fixture_credential_observed') is True
                         for row in fixture.discovery_observations)
        if witness is not None and witness['models'] == [MODEL] and discovered:
            app.observations['provider_catalog'] = witness
            app.observations['provider_discovery'] = list(fixture.discovery_observations)
            return witness
        time.sleep(0.1)
    refusal = CaseUnavailable('fixed-provider-current-discovery-or-catalog-unavailable')
    refusal.observations = {'provider_discovery': list(fixture.discovery_observations),
                           'provider_catalog': witness}
    raise refusal


def configure_provider(app, fixture):
    # Existing configured fixtures must keep their exact admitted endpoint and credential transport.
    existing = find_gui_provider(app, fixture.base_url)
    with DesktopUI(app) as ui:
        if existing is None:
            ui.click('Add AI provider')
            ui.click('LiteLLM')
            # WebKit exposes implicit labels and placeholders as one accessible name.
            physical_input(app, ui, 'Name (optional) e.g. GPU box, Work account', 'provider-name-v1')
            physical_input(app, ui, 'Endpoint URL http://localhost:4000/v1', endpoint_operation(fixture.server.server_port))
            physical_input(app, ui, 'API key Paste your API key…', 'provider-fixture-credential-v1', password=True)
            ui.click('Add AI provider')
        witness = await_catalog(app, fixture)
        ui.click('Default model')
        ui.click(MODEL)
        ui.click('Skip')
    return witness
