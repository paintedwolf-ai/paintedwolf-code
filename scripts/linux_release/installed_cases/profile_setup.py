"""Observe explicit onboarding completion and the persisted coordinator model reference."""
from __future__ import annotations
import hashlib
import json
import os
from pathlib import Path
from .profile_observer import profile, physical, _database, identifier, rows, project_for_owned_root, sessions_for_project
from .profile_documents import read_private, yaml_document
from .records import CaseUnavailable

ONBOARDING = '6f6e626f617264696e67.json'


def unique_pairs(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError('onboarding state has duplicate keys')
        value[key] = item
    return value


def state(app):
    with profile(app) as directory:
        slices = directory / 'app-state-v1'
        if slices.exists() or slices.is_symlink():
            physical(slices, True)
            raw = read_private(slices / ONBOARDING)
        else:
            raw = None
        completed = False
        if raw is not None:
            if len(raw) > 4096:
                raise ValueError('onboarding state exceeds its declared bound')
            try:
                document = json.loads(raw, object_pairs_hook=unique_pairs)
            except (UnicodeError, ValueError, RecursionError):
                raise ValueError('onboarding state is not its declared JSON envelope') from None
            if (not isinstance(document, dict) or set(document) != {'key', 'value'}
                    or document['key'] != 'onboarding' or not isinstance(document['value'], dict)
                    or set(document['value']) - {'firstRunSetupCompleted'}):
                raise ValueError('onboarding state differs from its declared envelope')
            completed = document['value'].get('firstRunSetupCompleted', False)
            if type(completed) is not bool:
                raise ValueError('onboarding completion is not a boolean')
        policy = read_private(directory / 'model-policy.yaml')
        coordinator = None
        if policy is not None:
            document = yaml_document(policy)
            if set(document) - {'coordinator', 'lite', 'agent_pool', 'thinking_overrides'}:
                raise ValueError('model policy contains undeclared fields')
            coordinator = document.get('coordinator')
            if (not isinstance(coordinator, dict) or set(coordinator) != {'provider_id', 'model'}
                    or any(not isinstance(value, str) or len(value) > 128 for value in coordinator.values())):
                raise ValueError('coordinator model reference differs from its declared shape')
        return {'onboarding_completed': completed, 'coordinator': coordinator,
                'onboarding_sha256': None if raw is None else hashlib.sha256(raw).hexdigest(),
                'model_policy_sha256': None if policy is None else hashlib.sha256(policy).hexdigest(),
                'observer': 'owned-persisted-setup'}


def fixture_selection(app, project, project_id, provider_id, model):
    """Bind the one-root fixture to its persisted coordinator without overlay merging."""
    identifier(project_id)
    path = Path(project)
    identity = physical(path, True)
    if os.path.lexists(path / '.paintedwolf'):
        raise ValueError('fixed reply project must have no project overlay')
    with _database(app, required={'project_roots': {'is_primary': 'INTEGER', 'kind': 'TEXT'}}) as connection:
        roots = rows(connection.execute('SELECT id,path,is_primary,kind FROM project_roots WHERE project_id=? LIMIT 257',
                                        (project_id,)))
    if len(roots) != 1 or roots[0][1:] != (str(path), 1, 'attached'):
        raise ValueError('fixed reply requires exactly its owned primary attached root')
    selected = state(app)
    if (not selected['onboarding_completed']
            or selected['coordinator'] != {'provider_id': provider_id, 'model': model}
            or selected['model_policy_sha256'] is None):
        raise ValueError('fixed reply coordinator differs from its persisted selection')
    return {'provider_id': provider_id, 'model': model,
            'model_policy_sha256': selected['model_policy_sha256'],
            'root_id': identifier(roots[0][0]), 'root_path': str(path), 'root_identity': identity, 'project_overlay_absent': True}


def active_session(app, project, provider, model):
    witness = project_for_owned_root(app, project)
    if witness is None:
        return None
    selection = fixture_selection(app, project, witness['project_id'], provider, model)
    rows = sessions_for_project(app, witness['project_id'])
    if len(rows) > 1:
        raise ValueError('fixed active-work fixture has ambiguous durable session identity')
    if rows and (rows[0]['parent_session_id'] is not None
            or (rows[0]['provider_id'], rows[0]['model']) not in {(None, None), (provider, model)}):
        raise ValueError('fixed active-work fixture session differs from its selected root coordinator')
    if rows and rows[0]['status'] == 'error':
        raise ValueError('fixed active-work fixture work failed before observation')
    if rows and rows[0]['status'] == 'busy':
        if fixture_selection(app, project, witness['project_id'], provider, model) != selection:
            raise ValueError('fixed active-work fixture model selection changed during observation')
        return {**witness, **rows[0], 'model_selection': selection}
    return None


def require_active(app, project, provider, model, expected=None):
    current = active_session(app, project, provider, model)
    if current is None:
        raise CaseUnavailable('protected-active-work-has-no-observed-session')
    if expected is not None and current != expected:
        raise ValueError('active-work interaction replaced its active durable session')
    return current
