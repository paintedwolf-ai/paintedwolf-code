"""Submit a closed fixture operation through the actual project composer."""
from __future__ import annotations

import time

from ..installed_process import process_identity
from .desktop_ui import DesktopUI, retry_inventory
from .profile_setup import first_time_tips_enabled
from .provider_setup import physical_input
from .records import CaseFailed, CaseUnavailable
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


def open_project_configuration(app, ui) -> dict:
    prepare_composer(app, ui)
    controls = [(ui.api.ROLE_COMBO_BOX, 'App menu'), (ui.api.ROLE_MENU_ITEM, 'Window'),
                (ui.api.ROLE_MENU_ITEM, 'Maximize'), (ui.api.ROLE_PUSH_BUTTON, 'Project configuration')]
    root = app.observations['launch_identity']
    actions = app.observations.setdefault('project_configuration_actions', [])
    for role, name in controls:
        deadline = time.monotonic() + 15
        while True:
            node = ui.find(role, name, deadline=deadline)
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
                    time.sleep(0.1)
                phase = 'identity-recheck'
                if role == ui.api.ROLE_MENU_ITEM and (
                        not ui.visible(node) or not node.getState().contains(ui.api.STATE_FOCUSED)):
                    raise accessibility.InventoryChanged()
                if (accessibility.capture(node, list(ui.windows(deadline)))[1] != chain
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
        actions.append({'role': int(role), 'name': name, 'accessible_chain': chain, 'entry_lineage': entry_chain,
                        'frame_lineage': frame_chain, 'visible': True, 'native_action_completed': True})
    witness = {'maximize_actions': actions[:-1], 'project_configuration': actions[-1], 'native_action_completed': True}
    app.observations['project_configuration_navigation'] = witness
    return witness


def submit_project_turn(app, *, operation='reply-fixture-prompt-v1'):
    if operation not in {'reply-fixture-prompt-v1', 'browser-fixture-prompt-v1'}:
        raise ValueError('project submission requires a declared fixture operation')
    with DesktopUI(app) as ui:
        prepare_composer(app, ui)
        record = physical_input(app, ui, 'What would you like to work on?', operation)
        ui.click('Send message')
    return record
