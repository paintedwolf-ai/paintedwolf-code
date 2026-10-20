"""Normal owned-window provider setup with independent installed API witnesses."""
from __future__ import annotations

from pathlib import Path
import re
import time
import uuid

from .app import InstalledApp
from .desktop_ui import DesktopUI, interactive_window
from .provider import ProviderFixture
from .records import CaseUnavailable, retain
from .workflow import accepted


def provider_record(app, label: str, endpoint: str) -> dict | None:
    document = accepted(app, 'GET', '/v1/providers', None, {200})
    rows = document.get('providers')
    if not isinstance(rows, list) or len(rows) > 256 or any(not isinstance(row, dict) for row in rows):
        raise ValueError('installed provider inventory exceeds its typed bound')
    matches = [row for row in rows if row.get('label') == label]
    if len(matches) > 1:
        raise ValueError('normal provider setup created ambiguous identity')
    if not matches:
        return None
    row = matches[0]
    if (not isinstance(row.get('id'), str) or re.fullmatch(r'[a-zA-Z0-9_.-]{1,128}', row['id']) is None
            or row.get('kind') != 'openai-compatible' or row.get('base_url') != endpoint
            or row.get('configured') is not True or row.get('requires_api_key') is not False
            or row.get('credential_present') is not False):
        raise ValueError('normal provider setup differs from its keyless fixture identity')
    return {'provider_id': row['id'], 'kind': row['kind'], 'configured': True,
            'requires_api_key': False, 'credential_present': False}


def add_keyless_provider(app, fixture) -> dict:
    label = 'Installed UI ' + uuid.uuid4().hex
    with DesktopUI(app) as ui:
        # These are the public provider picker and labeled native form controls.
        # A different screen is unavailable, never a reason to invoke a hidden route.
        ui.click('Add AI provider')
        ui.click('OpenAI-compatible')
        ui.fill(ui.api.ROLE_ENTRY, 'Name (optional)', label)
        ui.fill(ui.api.ROLE_ENTRY, 'Endpoint URL', fixture.base_url)
        ui.click('Add AI provider')
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        witness = provider_record(app, label, fixture.base_url)
        if witness is not None:
            return witness
        time.sleep(0.1)
    raise ValueError('normal provider form did not create the selected installed API identity')


def execute(binding: dict, capability: str, case: str, root: Path) -> dict:
    if binding.get('case_inputs', {}).get('desktop', {}) not in ({}, {'format_version': 1}):
        raise ValueError('conversation driver refuses arbitrary fixture and command inputs')
    if capability != 'conversation' or case != 'provider-onboarding':
        return retain(binding, capability, case, root, 'unverified',
                      {'reason': 'remaining-native-conversation-interaction-required'})
    app = InstalledApp(binding)
    # The provider fixture accepts only a fixed ordinary tool invocation; this
    # scenario adds it through the UI and never invokes a real model.
    with ProviderFixture([{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]) as provider:
        try:
            with app:
                interactive = interactive_window(app)
                witness = add_keyless_provider(app, provider)
                try:
                    skipped = False
                    with DesktopUI(app) as ui:
                        try:
                            ui.click('Skip')
                            skipped = True
                        except CaseUnavailable:
                            # Optional setup Skip is not completed onboarding.
                            pass
                    observations = {'interactive': interactive, 'normal_provider_setup': witness,
                                    'provider_requests': provider.observations,
                                    'normal_optional_setup_skip': skipped,
                                    'remaining_requirements': ['default-model-selection-and-first-turn',
                                                               'credential-provider-onboarding-and-reconnect']}
                finally:
                    removed = app.request('DELETE', '/v1/providers/' + witness['provider_id'])
                    if removed['status'] != 204:
                        raise ValueError('installed UI fixture provider cleanup was refused')
        finally:
            app.stop()
    return retain(binding, capability, case, root, 'unverified', observations, app.streams())
