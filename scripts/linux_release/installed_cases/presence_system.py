"""Qualify real system authentication against the host's value-free reveal audit."""
from __future__ import annotations

import os
from pathlib import Path
import time

from .native_session import NativeSessionObserver, kernel_owner
from .polkit_attempt import attempt_rows
from .presence_confirmation import await_confirmation, confirmation_rows
from .profile_observer import _database, identifier, rows
from .records import CaseFailed, CaseUnavailable
from ..installed_environment import graphical_session
from ..installed_input.polkit_client import NativePolkitControl
from ..installed_input.presence_fixture import NAME
from ..installed_process import engine_image


def reveal_audit(app, project_id):
    identifier(project_id)
    required = {'managed_secrets': {'id': 'TEXT', 'project_id': 'TEXT', 'name': 'TEXT',
        'origin': 'TEXT', 'created_by_person_id': 'TEXT'},
        'managed_secret_reveals': {'id': 'TEXT', 'secret_id': 'TEXT', 'version': 'INTEGER',
        'authenticator': 'TEXT', 'window_label': 'TEXT', 'person_id': 'TEXT', 'revealed_at': 'TEXT'}}
    with _database(app, required=required) as database:
        selected = rows(database.execute('SELECT id,created_by_person_id FROM managed_secrets '
            'WHERE project_id=? AND name=? AND origin=? LIMIT 3', (project_id, NAME, 'settings_entered')))
        if len(selected) != 1:
            raise CaseUnavailable('system-presence-fixture-secret-missing-or-ambiguous')
        secret_id, person_id = selected[0]
        identifier(secret_id)
        identifier(person_id)
        observed = rows(database.execute('SELECT id,version,authenticator,window_label,person_id '
            'FROM managed_secret_reveals WHERE secret_id=? ORDER BY id LIMIT 17', (secret_id,)))
    if len(observed) > 16:
        raise ValueError('system presence reveal audit exceeded its fixture bound')
    audits = []
    for challenge, version, authenticator, window, person in observed:
        identifier(challenge)
        if (type(version) is not int or version < 1 or authenticator != 'linux_user_presence'
                or window != 'main' or person != person_id):
            raise CaseFailed('system-presence-host-reveal-audit-has-foreign-authority')
        audits.append({'challenge_id': challenge, 'version': version, 'authenticator': authenticator,
                       'window_label': window, 'person_id': person})
    return {'secret_id': secret_id, 'person_id': person_id, 'reveals': audits}


def native_caller(app, ui):
    expected = app.binding['images'][str(Path(app.binding['install_root']) / 'bin/painted-wolf-code')]
    image = tuple(expected[key] for key in ('device', 'inode', 'bytes'))
    owners = {identity['pid']: identity for identity, _ in ui.windows()}
    selected = [kernel_owner(pid, os.getuid()) for pid in owners if engine_image(pid) == image]
    if len(selected) != 1:
        raise CaseUnavailable('system-presence-native-caller-missing-or-ambiguous')
    return selected[0]


def await_attempt(app, native, after):
    deadline = time.monotonic() + 15
    while True:
        selected = [row for row in attempt_rows(app, native)[after:]
                    if row['phase'] == 'interactive' and row['transport'] == 'pending']
        if len(selected) > 1:
            raise ValueError('system presence has multiple new native interactive attempts')
        if selected:
            return selected[0]
        if time.monotonic() >= deadline:
            raise CaseUnavailable('system-presence-native-interactive-attempt-unverified')
        time.sleep(0.05)


def await_observation(control, initial=None, *, final=False):
    deadline, value = time.monotonic() + 15, initial
    while True:
        if value is not None and ('host_release_verified' in value) == final:
            return value
        if time.monotonic() >= deadline:
            raise CaseUnavailable('system-presence-root-causal-observation-unverified')
        time.sleep(0.05)
        value = control.observe()


def run_reveal(app, ui, project_id, native_input, observations, *, cancel):
    """Keep cancellation, native system success and actual host release separate."""
    kind = 'cancel' if cancel else 'system_authentication'
    record = {'cancel_verified': False, 'system_authentication_verified': False,
              'host_release_verified': False}
    observations[kind] = record
    owner = native_caller(app, ui)
    record['native_caller'] = owner
    before = reveal_audit(app, project_id)
    record['host_audit_before'] = before
    native_after, confirmation_after = len(attempt_rows(app, owner)), len(confirmation_rows(app))
    with NativeSessionObserver(graphical_session()) as session:
        control = NativePolkitControl(app, owner, session, native_input['binding'], native_input['vm_id'])
        record['controls'] = control.records
        try:
            control.start()  # The real system bus collector precedes the public reveal action.
            ui.click('Reveal value')
            attempt = await_attempt(app, owner, native_after)
            record['native_attempt'] = attempt
            root_pending = await_observation(control, control.bind(attempt))
            if root_pending['attempt'] != attempt:
                raise ValueError('system presence root selection changed its native challenge')
            record['physical_delivery'] = control.deliver(cancel=cancel)
            witness = await_observation(control, final=True)
            record['root_witness'] = witness
            challenge = attempt['challenge']
            outcome = await_confirmation(app, challenge['challenge_id'],
                challenge['step_away_generation'], after_count=confirmation_after)
            record['native_confirmation'] = outcome
            after = reveal_audit(app, project_id)
            record['host_audit_after'] = after
            if (after['secret_id'], after['person_id']) != (before['secret_id'], before['person_id']):
                raise ValueError('system presence host audit replaced the selected secret or person')
            if cancel:
                if (witness['agent_cancelled'] is not True or witness['system_authenticated'] is not False
                        or outcome['status'] != 'refused' or after != before):
                    raise CaseFailed('cancelled-system-presence-did-not-refuse-without-host-release')
                record['cancel_verified'] = True
            else:
                if (witness['system_authenticated'] is not True or witness['agent_cancelled'] is not False
                        or outcome['status'] != 'confirmed' or outcome['authenticator'] != 'linux_user_presence'):
                    raise CaseFailed('actual-system-presence-confirmation-not-observed')
                added = [row for row in after['reveals'] if row not in before['reveals']]
                if (len(after['reveals']) != len(before['reveals']) + 1 or len(added) != 1
                        or added[0]['challenge_id'] != challenge['challenge_id']):
                    raise CaseFailed('system-presence-has-no-exact-host-release-audit')
                # Observe the value-free public control; never read or capture the value.
                ui.find(ui.api.ROLE_PUSH_BUTTON, 'Hide now', deadline=time.monotonic() + 10)
                record.update(system_authentication_verified=True, host_release_verified=True)
                ui.click('Hide now')
        finally:
            control.close()
    return record
