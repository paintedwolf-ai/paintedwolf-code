"""Observe explicit onboarding completion and the persisted coordinator model reference."""
from __future__ import annotations
import hashlib
import json
from .profile_observer import profile, physical
from .profile_documents import read_private, yaml_document

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
