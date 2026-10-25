"""Submit a closed fixture operation through the actual project composer."""
from __future__ import annotations

import time

from ..installed_process import process_identity
from .desktop_ui import DesktopUI, retry_inventory
from .profile_setup import first_time_tips_enabled, project_configuration_identity
from .provider_setup import physical_input
from .records import CaseFailed, CaseUnavailable
from .settings_query import SettingsQuery
from ..installed_input import accessibility, lineage


def prepare_composer(app, ui):
    if not first_time_tips_enabled(app):
        return
    ui.click('Disable tips')
    deadline = time.monotonic() + 10
    while first_time_tips_enabled(app):
        if time.monotonic() >= deadline:
            raise CaseUnavailable('normal-first-time-tips-disable-did-not-persist')
        time.sleep(0.05)


def open_project_configuration(app, ui, project, selected) -> dict:
    prepare_composer(app, ui)
    target = project_configuration_identity(app, project, selected)
    actions = app.observations.setdefault('project_configuration_actions', [])
    deadline = time.monotonic() + 15
    controls = [(ui.api.ROLE_COMBO_BOX, 'App menu'),
                (ui.api.ROLE_MENU_ITEM, 'Go'),
                (ui.api.ROLE_MENU_ITEM, 'Crossbar…')]
    for role, name in controls:
        _require_configuration_target(app, project, selected, target)
        actions.append(_project_configuration_action(app, ui, deadline, role, name))
        _require_configuration_target(app, project, selected, target)
    _require_configuration_target(app, project, selected, target)
    query = SettingsQuery(app, ui, deadline)
    record = query.enter()
    _require_configuration_target(app, project, selected, target)
    if record.get('operation') != 'settings-providers-query-v1' or record.get('completed') is not True:
        raise ValueError('project configuration query did not complete its fixed operation')
    app.observations['project_configuration_query'] = {
        'operation': record['operation'], 'completed': record['completed'],
        'fixed_query_matches': True, 'explicit_actions_arm': True,
        'expected_catalog_id': 'painted-wolf/platform:settings-section-providers',
        'accessibility_exposes_catalog_id': False}
    for role, name, command in [(ui.api.ROLE_PUSH_BUTTON, 'Settings: AI providers', True),
                                (ui.api.ROLE_PUSH_BUTTON, target['display_name'] + ' → AI providers', False)]:
        _require_configuration_target(app, project, selected, target)
        actions.append(_project_configuration_action(app, ui, deadline, role, name, command=command,
                                                     query=query if command else None))
        _require_configuration_target(app, project, selected, target)
    witness = {'project': target, 'actions': actions, 'native_action_completed': True}
    app.observations['project_configuration_navigation'] = witness
    return witness


def _require_configuration_target(app, project, selected, target):
    if project_configuration_identity(app, project, selected) != target:
        raise ValueError('selected project changed during configuration navigation')


def _project_configuration_action(app, ui, deadline, role, name, *, command=False, query=None):
    root = app.observations['launch_identity']
    while True:
        node = ui.settings_provider_command(deadline) if command else ui.find(role, name, deadline=deadline)
        observed_name = node.name
        if not isinstance(observed_name, str) or not 0 < len(observed_name) <= 256:
            raise ValueError('project configuration control name exceeds its bound')
        phase = 'inventory-ownership'
        try:
            frame, chain = accessibility.capture(node, list(ui.windows(deadline)))
            control = process_identity(node.get_process_id())
            surface = process_identity(frame.get_process_id())
            entry_chain, frame_chain = lineage.capture(surface, control), lineage.capture(root, surface)
            phase = 'action-interface'
            action = node.queryAction()
            if action.nActions != 1:
                phase = 'action-count'
                raise CaseUnavailable('normal-project-configuration-action-is-not-unique')
            if role == ui.api.ROLE_MENU_ITEM:
                phase = 'component-interface'
                if not node.queryComponent().grabFocus():
                    raise accessibility.InventoryChanged()
                if not node.getState().contains(ui.api.STATE_FOCUSED):
                    raise accessibility.InventoryChanged()
            if command:
                phase = 'query-readback'
                query.require_current()
            phase = 'identity-recheck'
            if (node.name != observed_name or not ui.visible(node)
                    or (command and not ui.settings_provider_command_matches(node, deadline))
                    or accessibility.capture(node, list(ui.windows(deadline)))[1] != chain
                    or lineage.capture(surface, control) != entry_chain
                    or lineage.capture(root, surface) != frame_chain):
                raise ValueError('normal project configuration identity changed before its action')
            break
        except (ValueError, TypeError, CaseUnavailable, CaseFailed):
            raise
        except Exception as error:
            if (time.monotonic() < deadline and retry_inventory(
                    app, error, interface_mounting=(phase in {'action-interface', 'component-interface'}))):
                time.sleep(0.05)
                continue
            refusal = CaseUnavailable('normal-project-configuration-action-unavailable')
            refusal.observations = {'project_configuration_mutation': {
                'phase': phase, 'name': name, 'role': int(role), 'exception_type': type(error).__name__}}
            raise refusal from None
    phase = 'do-action'
    if time.monotonic() >= deadline:
        raise CaseUnavailable('normal-project-configuration-action-deadline')
    try:
        if action.doAction(0) is not True:
            phase = 'do-action-refused'
            raise CaseUnavailable('normal-project-configuration-action-refused')
    except (CaseUnavailable, CaseFailed):
        raise
    except Exception as error:
        refusal = CaseUnavailable('normal-project-configuration-action-unavailable')
        refusal.observations = {'project_configuration_mutation': {
            'phase': phase, 'name': name, 'role': int(role), 'exception_type': type(error).__name__}}
        raise refusal from None
    if lineage.capture(root, surface) != frame_chain or lineage.capture(surface, control) != entry_chain:
        raise ValueError('normal project configuration process identity changed after its action')
    return {'role': int(role), 'name': name, 'accessible_name': observed_name, 'accessible_chain': chain, 'entry_lineage': entry_chain,
            'frame_lineage': frame_chain, 'visible': True, 'native_action_completed': True}


def submit_project_turn(app, *, operation='reply-fixture-prompt-v1'):
    if operation not in {'reply-fixture-prompt-v1', 'browser-fixture-prompt-v1'}:
        raise ValueError('project submission requires a declared fixture operation')
    with DesktopUI(app) as ui:
        prepare_composer(app, ui)
        record = physical_input(app, ui, 'What would you like to work on?', operation)
        ui.click('Send message')
    return record
