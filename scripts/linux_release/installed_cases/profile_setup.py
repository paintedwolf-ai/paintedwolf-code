"""Observe explicit onboarding completion and the persisted coordinator model reference."""
from __future__ import annotations
import hashlib
import json
import os
from pathlib import Path
import time
from .profile_observer import profile, physical, _database, identifier, rows, project_for_owned_root, sessions_for_project
from .profile_documents import read_private, yaml_document
from .records import CaseUnavailable

ONBOARDING = '6f6e626f617264696e67.json'
LAST_ACTIVE_PROJECT = '6c61737441637469766550726f6a6563744964.json'
LAST_SESSION_SNAPSHOT = '6c61737453657373696f6e536e617073686f74.json'
FIRST_TIME_TIPS = '666972737454696d6554697073.json'


def unique_pairs(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError('onboarding state has duplicate keys')
        value[key] = item
    return value


def slice_document(slices: Path, filename: str, key: str, *, limit: int = 4096):
    raw = read_private(slices / filename)
    if raw is None:
        return None, None
    if len(raw) > limit:
        raise ValueError(f'{key} state exceeds its declared bound')
    try:
        document = json.loads(raw, object_pairs_hook=unique_pairs)
    except (UnicodeError, ValueError, RecursionError):
        raise ValueError(f'{key} state is not its declared JSON envelope') from None
    if not isinstance(document, dict) or set(document) != {'key', 'value'} or document['key'] != key:
        raise ValueError(f'{key} state differs from its declared envelope')
    return document['value'], hashlib.sha256(raw).hexdigest()


def state(app):
    with profile(app) as directory:
        slices = directory / 'app-state-v1'
        if slices.exists() or slices.is_symlink():
            physical(slices, True)
            onboarding, onboarding_digest = slice_document(slices, ONBOARDING, 'onboarding')
        else:
            onboarding, onboarding_digest = None, None
        completed = False
        if onboarding is not None:
            if not isinstance(onboarding, dict) or set(onboarding) - {'firstRunSetupCompleted'}:
                raise ValueError('onboarding state differs from its declared envelope')
            completed = onboarding.get('firstRunSetupCompleted', False)
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
                'onboarding_sha256': onboarding_digest,
                'model_policy_sha256': None if policy is None else hashlib.sha256(policy).hexdigest(),
                'observer': 'owned-persisted-setup'}


def first_time_tips_enabled(app) -> bool:
    with profile(app) as directory:
        slices = directory / 'app-state-v1'
        if not (slices.exists() or slices.is_symlink()):
            return True
        physical(slices, True)
        value, _ = slice_document(slices, FIRST_TIME_TIPS, 'firstTimeTips')
    if value is None:
        return True
    if not isinstance(value, dict) or set(value) - {'enabled', 'dismissed'}:
        raise ValueError('first-time tips state differs from its declared shape')
    enabled = value.get('enabled', True)
    if type(enabled) is not bool:
        raise ValueError('first-time tips enabled state is not a boolean')
    return enabled


def renderer_session(app, project, project_id, session_id):
    identifier(project_id)
    identifier(session_id)
    root = str(Path(project))
    with profile(app) as directory:
        slices = directory / 'app-state-v1'
        if not (slices.exists() or slices.is_symlink()):
            return None
        physical(slices, True)
        active_project, active_digest = slice_document(slices, LAST_ACTIVE_PROJECT, 'lastActiveProjectId')
        snapshot, snapshot_digest = slice_document(slices, LAST_SESSION_SNAPSHOT, 'lastSessionSnapshot', limit=262144)
    if active_project is not None and (not isinstance(active_project, str) or len(active_project) > 128):
        raise ValueError('active project state differs from its declared shape')
    if active_project != project_id or snapshot is None:
        return None
    if not isinstance(snapshot, dict):
        raise ValueError('renderer session snapshot differs from its declared shape')
    scope = snapshot.get('scope')
    if not isinstance(scope, dict) or set(scope) != {'projectId', 'sessionId'}:
        raise ValueError('renderer session scope differs from its declared shape')
    if scope != {'projectId': project_id, 'sessionId': session_id}:
        return None
    session = snapshot.get('session')
    if (not isinstance(session, dict) or session.get('id') != session_id
            or session.get('project_id') != project_id or session.get('workspace_path') != root):
        raise ValueError('renderer session snapshot differs from its selected project session')
    if session.get('status') == 'error':
        raise ValueError('renderer session snapshot failed before observation')
    if session.get('status') not in {'preparing', 'idle'}:
        return None
    workflow = snapshot.get('activeWorkflowRun')
    if workflow is None:
        runs = snapshot.get('workflowRuns')
        if not isinstance(runs, list) or len(runs) != 1:
            return None
        workflow = runs[0]
    if (not isinstance(workflow, dict) or workflow.get('project_id') != project_id
            or workflow.get('session_id') != session_id):
        raise ValueError('renderer workflow snapshot differs from its selected project session')
    ui_state = workflow.get('ui')
    if ui_state is None and (workflow.get('workflow_id'), workflow.get('attach_policy'),
                             workflow.get('status')) == ('implement', 'session_create', 'running'):
        question = 'What would you like to work on?'
    else:
        request = ui_state.get('request') if isinstance(ui_state, dict) else None
        question = request.get('question') if isinstance(request, dict) else None
    if question != 'What would you like to work on?':
        return None
    return {'observer': 'owned-app-state-JSON', 'project_id': project_id, 'session_id': session_id,
            'status': session['status'], 'workspace_path': root, 'composer_question': question,
            'last_active_project_sha256': active_digest, 'last_session_snapshot_sha256': snapshot_digest}


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


def active_session(app, project, provider, model, *, status='busy'):
    if status not in {'idle', 'busy'}:
        raise ValueError('fixed active-work fixture requires an idle or busy session phase')
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
    if rows and rows[0]['status'] == status:
        rendered = renderer_session(app, project, witness['project_id'], rows[0]['id']) if status == 'idle' else None
        if status == 'idle' and rendered is None:
            return None
        if fixture_selection(app, project, witness['project_id'], provider, model) != selection:
            raise ValueError('fixed active-work fixture model selection changed during observation')
        result = {**witness, **rows[0], 'model_selection': selection}
        if rendered is not None:
            result['renderer_session'] = rendered
        return result
    return None


def await_session(app, project, provider, model, *, status='busy',
                  reason='protected-active-work-session-did-not-settle'):
    deadline = time.monotonic() + 30
    while (session := active_session(app, project, provider, model, status=status)) is None:
        if time.monotonic() >= deadline:
            raise CaseUnavailable(reason)
        time.sleep(0.05)
    return session


def require_active(app, project, provider, model, expected=None):
    current = active_session(app, project, provider, model)
    if current is None:
        raise CaseUnavailable('protected-active-work-has-no-observed-session')
    if expected is not None and current != expected:
        raise ValueError('active-work interaction replaced its active durable session')
    return current
