"""Observe only the source-fixed credentialed GUI fixture in its owned provider catalog."""
from __future__ import annotations

import re

from .profile_observer import profile
from .provider import GUI_LABEL, GUI_KIND, MODEL
from .profile_documents import read_private, yaml_document


def document(raw: bytes):
    value = yaml_document(raw)
    if set(value) != {'providers'} or not isinstance(value['providers'], list) or len(value['providers']) > 256:
        raise ValueError('owned provider catalog differs from its declared provider inventory')
    return value['providers']


def selected(rows, endpoint: str):
    seen, matches = set(), []
    for row in rows:
        if (not isinstance(row, dict) or not isinstance(row.get('id'), str)
                or not re.fullmatch(r'[A-Za-z0-9_.-]{1,128}', row['id']) or row['id'] in seen):
            raise ValueError('owned provider inventory has ambiguous identities')
        seen.add(row['id'])
        if row.get('label') == GUI_LABEL:
            matches.append(row)
    if len(matches) > 1:
        raise ValueError('owned GUI fixture label has ambiguous provider identity')
    if not matches:
        return None
    row = matches[0]
    if (row.get('kind') != GUI_KIND or row.get('base_url') != endpoint
            or row.get('requires_api_key') is not True or row.get('api_key_env', '') not in {'', 'LITELLM_MASTER_KEY'}
            or row.get('ambient_auth') not in (None, '')):
        raise ValueError('owned GUI provider differs from its fixed local credential transport')
    models = row.get('models', [])
    if (not isinstance(models, list) or len(models) > 256 or any(not isinstance(model, dict)
            or not isinstance(model.get('id'), str) or not 1 <= len(model['id']) <= 128 for model in models)):
        raise ValueError('owned GUI provider models exceed their typed bound')
    identifiers = [model['id'] for model in models]
    if len(set(identifiers)) != len(identifiers) or any(value != MODEL for value in identifiers):
        raise ValueError('owned GUI provider model identities are ambiguous')
    return {'provider_id': row['id'], 'kind': GUI_KIND, 'requires_api_key': True,
            'configured_models': identifiers, 'observer': 'owned-provider-YAML'}


def find_gui_provider(app, endpoint: str):
    """The GUI fixture provider registered for `endpoint`, the running fixture's bound URL."""
    with profile(app) as directory:
        raw = read_private(directory / 'providers.local.yaml')
        return None if raw is None else selected(document(raw), endpoint)
