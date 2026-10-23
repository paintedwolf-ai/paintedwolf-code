"""Closed app-frame input proofs, separate from focused editable-entry authority."""
from __future__ import annotations

import re

OPERATIONS = {
    'workspace-open-peer-v1': (('ctrl', 'shift', 'semicolon'), ('n',)),
    'workspace-hide-main-v1': (('ctrl', 'shift', 'w'),),
    'workspace-request-quit-v1': (('ctrl', 'q'),),
    'workspace-cancel-quit-v1': (('esc',),),
}


def object_identity(value):
    if (not isinstance(value, dict) or set(value) != {'bus_name', 'object_path', 'process'}
            or not isinstance(value['bus_name'], str)
            or not re.fullmatch(r':[0-9]+\.[0-9]+', value['bus_name'])
            or len(value['bus_name']) > 255
            or not isinstance(value['object_path'], str) or len(value['object_path']) > 512
            or not re.fullmatch(r'/(?:[A-Za-z0-9_]+/)*[A-Za-z0-9_]+', value['object_path'])):
        raise ValueError('surface proof requires its actual unique bus and object path')
    from .protocol import process_identity
    process_identity(value['process'])
    return value


def proof(value):
    from .protocol import process_identity
    if not isinstance(value, dict) or set(value) != {'launch', 'engine', 'main', 'surfaces'}:
        raise ValueError('surface proof has an unknown shape')
    for key in ('launch', 'engine'):
        process_identity(value[key])
    object_identity(value['main'])
    surfaces = value['surfaces']
    if not isinstance(surfaces, list) or not 1 <= len(surfaces) <= 16:
        raise ValueError('surface proof exceeds its fixed frame inventory')
    seen = set()
    for row in surfaces:
        if (not isinstance(row, dict) or set(row) != {'identity', 'kind', 'visible', 'active', 'defunct', 'modal', 'parent'}
                or row['kind'] not in {'frame', 'quit-dialog', 'presence-dialog'}
                or any(type(row[key]) is not bool for key in ('visible', 'active', 'defunct', 'modal'))):
            raise ValueError('surface observation has an unknown shape')
        identity = object_identity(row['identity'])
        if identity['process'] != value['launch']:
            raise ValueError('surface proof escaped its captured desktop process')
        marker = identity['bus_name'], identity['object_path']
        if marker in seen:
            raise ValueError('surface proof repeats an accessible object')
        seen.add(marker)
        if row['parent'] is not None:
            object_identity(row['parent'])
        if row['kind'] != 'frame' and (not row['modal'] or row['parent'] != value['main']):
            raise ValueError('native dialog omitted its captured modal parent')
    if value['main'] not in [row['identity'] for row in surfaces]:
        raise ValueError('surface proof lost its captured original frame')
    return value


def same_runtime(before, after):
    proof(before)
    proof(after)
    if any(before[key] != after[key] for key in ('launch', 'engine', 'main')):
        raise ValueError('surface transition replaced its captured runtime or original frame')


def active(row):
    return row['active'] and row['visible'] and not row['defunct']


def before(operation, value):
    proof(value)
    selected = [row for row in value['surfaces'] if active(row)]
    if len(selected) != 1:
        raise ValueError('surface input requires exactly one owned active visible frame')
    row = selected[0]
    if operation == 'workspace-cancel-quit-v1':
        if row['kind'] != 'quit-dialog':
            raise ValueError('Quit cancellation requires the actual owned modal dialog')
    elif operation in OPERATIONS:
        if row['kind'] != 'frame' or row['identity'] != value['main']:
            raise ValueError('workspace input requires its captured original main frame')
        if operation == 'workspace-hide-main-v1' and not any(
                other['kind'] == 'frame' and other['identity'] != value['main'] and other['visible']
                and not other['defunct'] for other in value['surfaces']):
            raise ValueError('workspace hiding requires an independently observed visible peer')
    else:
        raise ValueError('surface input requires a fixed declared operation')
    return value


def finished(operation, old, new):
    same_runtime(old, new)
    main = next(row for row in new['surfaces'] if row['identity'] == new['main'])
    if operation == 'workspace-open-peer-v1':
        previous = [row['identity'] for row in old['surfaces']]
        return main['visible'] and not main['defunct'] and any(
            row['kind'] == 'frame' and row['identity'] not in previous and active(row)
            for row in new['surfaces'])
    if operation == 'workspace-hide-main-v1':
        return not main['visible'] and not main['defunct'] and any(
            row['kind'] == 'frame' and row['identity'] != new['main'] and active(row)
            for row in new['surfaces'])
    if operation == 'workspace-request-quit-v1':
        return any(row['kind'] == 'quit-dialog' and active(row) for row in new['surfaces'])
    if operation == 'workspace-cancel-quit-v1':
        return active(main) and not any(row['kind'] == 'quit-dialog' and row['visible']
                                       for row in new['surfaces'])
    raise ValueError('surface input requires a fixed declared operation')
