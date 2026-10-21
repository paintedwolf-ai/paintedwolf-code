"""Observe only the source-fixed keyless GUI fixture in its owned provider catalog."""
from __future__ import annotations

import os
import re
import stat

from .profile_observer import profile
from .provider import GUI_ENDPOINT, GUI_LABEL, GUI_KIND, MODEL
from .package_payload import stable
from .records import CaseUnavailable

LIMIT = 1 << 20


def document(raw: bytes):
    try:
        import yaml
    except ImportError:
        raise CaseUnavailable('bounded-provider-YAML-observer-unavailable') from None
    if len(raw) > LIMIT:
        raise ValueError('owned provider catalog exceeds its fixed bound')
    try:
        root = yaml.compose(raw, Loader=yaml.SafeLoader)
        pending, seen = [(root, 0)], set()
        while pending:
            node, depth = pending.pop()
            if node is None or id(node) in seen or depth > 32 or len(seen) >= 8192:
                raise ValueError('provider catalog has aliases or exceeds its node bound')
            seen.add(id(node))
            if isinstance(node, yaml.MappingNode):
                keys = set()
                for key, value in node.value:
                    if key.tag != 'tag:yaml.org,2002:str' or len(key.value) > 128 or key.value in keys:
                        raise ValueError('provider catalog has ambiguous mapping keys')
                    keys.add(key.value)
                    pending.extend(((key, depth + 1), (value, depth + 1)))
            elif isinstance(node, yaml.SequenceNode):
                pending.extend((value, depth + 1) for value in node.value)
            elif not isinstance(node, yaml.ScalarNode) or len(node.value) > 4096:
                raise ValueError('provider catalog scalar exceeds its bound')
        value = yaml.safe_load(raw)
    except (yaml.YAMLError, RecursionError, UnicodeError):
        raise ValueError('owned provider catalog has an unsupported YAML shape') from None
    if not isinstance(value, dict) or set(value) != {'providers'} or not isinstance(value['providers'], list) or len(value['providers']) > 256:
        raise ValueError('owned provider catalog differs from its declared provider inventory')
    return value['providers']


def selected(rows):
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
    if (row.get('kind') != GUI_KIND or row.get('base_url') != GUI_ENDPOINT
            or row.get('requires_api_key') is not True or row.get('api_key_env', '') not in {'', 'LITELLM_MASTER_KEY'}
            or row.get('ambient_auth', '') != ''):
        raise ValueError('owned GUI provider differs from its fixed local credential transport')
    models = row.get('models', [])
    if (not isinstance(models, list) or len(models) > 256 or any(not isinstance(model, dict)
            or not isinstance(model.get('id'), str) or not 1 <= len(model['id']) <= 128 for model in models)):
        raise ValueError('owned GUI provider models exceed their typed bound')
    identifiers = [model['id'] for model in models]
    if len(set(identifiers)) != len(identifiers) or any(value != MODEL for value in identifiers):
        raise ValueError('owned GUI provider model identities are ambiguous')
    return {'provider_id': row['id'], 'kind': GUI_KIND, 'requires_api_key': True,
            'models': identifiers, 'observer': 'owned-provider-YAML'}


def find_gui_provider(app):
    with profile(app) as directory:
        path = directory / 'providers.local.yaml'
        if not os.path.lexists(path):
            return None
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor, 'rb') as stream:
            before = os.fstat(stream.fileno())
            if (not stat.S_ISREG(before.st_mode) or before.st_uid != os.getuid()
                    or before.st_mode & 0o077 or before.st_size > LIMIT or path.resolve(strict=True) != path):
                raise ValueError('owned provider catalog requires private physical storage')
            raw = stream.read(LIMIT + 1)
            if stable(before) != stable(os.fstat(stream.fileno())) or stable(before) != stable(path.lstat()):
                raise CaseUnavailable('owned-provider-catalog-changed-during-observation')
        return selected(document(raw))
