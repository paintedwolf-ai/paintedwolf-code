"""Closed app-frame input proofs, separate from focused editable-entry authority."""
from __future__ import annotations

import re
from . import lineage

OPERATIONS = {
    'workspace-open-peer-v1': (('ctrl', 'shift', 'semicolon'), ('n',)),
    'workspace-hide-main-v1': (('ctrl', 'shift', 'w'),),
    'workspace-request-quit-v1': (('ctrl', 'q'),),
    'workspace-cancel-quit-v1': (('esc',),),
    'workspace-focus-quit-v1': (('shift', 'tab'),),
    'workspace-approve-quit-v1': (('ret',),),
    'presence-cancel-v1': (('esc',),),
}


def object_identity(value, root):
    if (not isinstance(value, dict) or set(value) != {'bus_name', 'object_path', 'process', 'lineage'}
            or not isinstance(value['bus_name'], str)
            or not re.fullmatch(r':[0-9]+\.[0-9]+', value['bus_name'])
            or len(value['bus_name']) > 255
            or not isinstance(value['object_path'], str) or len(value['object_path']) > 512
            or not re.fullmatch(r'/(?:[A-Za-z0-9_]+/)*[A-Za-z0-9_]+', value['object_path'])):
        raise ValueError('surface proof requires its actual unique bus and object path')
    lineage.validate(value['lineage'], root, value['process'])
    return value


def proof(value):
    if not isinstance(value, dict) or set(value) != {'launch', 'engine', 'main', 'surfaces', 'control'}:
        raise ValueError('surface proof has an unknown shape')
    for key in ('launch', 'engine'):
        lineage.identity(value[key])
    object_identity(value['main'], value['launch'])
    surfaces = value['surfaces']
    if not isinstance(surfaces, list) or not 1 <= len(surfaces) <= 16:
        raise ValueError('surface proof exceeds its fixed frame inventory')
    seen = set()
    for row in surfaces:
        if (not isinstance(row, dict) or set(row) != {'identity', 'kind', 'visible', 'active', 'defunct', 'modal', 'parent'}
                or row['kind'] not in {'frame', 'quit-dialog', 'presence-dialog'}
                or any(type(row[key]) is not bool for key in ('visible', 'active', 'defunct', 'modal'))):
            raise ValueError('surface observation has an unknown shape')
        identity = object_identity(row['identity'], value['launch'])
        if (identity['process'] != value['main']['process']
                or identity['bus_name'] != value['main']['bus_name']):
            raise ValueError('surface proof escaped its captured desktop process')
        marker = identity['bus_name'], identity['object_path']
        if marker in seen:
            raise ValueError('surface proof repeats an accessible object')
        seen.add(marker)
        if row['parent'] is not None:
            object_identity(row['parent'], value['launch'])
        if row['kind'] != 'frame' and (row['parent'] != value['main']
                or not row['defunct'] and not row['modal']):
            raise ValueError('native dialog omitted its captured modal parent')
    main = [row for row in surfaces if row['identity'] == value['main']]
    if len(main) != 1 or main[0]['kind'] != 'frame' or main[0]['parent'] is not None:
        raise ValueError('surface proof lost its captured original frame')
    control = value['control']
    if control is not None:
        if (not isinstance(control, dict) or set(control) != {'identity', 'kind', 'parent', 'focused'}
                or control['kind'] not in {'quit-button', 'keep-working-button'} or control['focused'] is not True):
            raise ValueError('surface input has an unknown focused control')
        object_identity(control['identity'], value['launch'])
        if (control['identity']['process'] != value['main']['process']
                or control['identity']['bus_name'] != value['main']['bus_name']
                or control['identity'] in [row['identity'] for row in surfaces] or not any(
                row['identity'] == control['parent'] and row['kind'] == 'quit-dialog' and active(row)
                for row in surfaces)):
            raise ValueError('focused Quit control escaped its owned active native dialog')
    return value


def same_runtime(before, after):
    proof(before)
    proof(after)
    old_objects = [row['identity'] for row in before['surfaces']]
    new_objects = [row['identity'] for row in after['surfaces']]
    if (any(before[key] != after[key] for key in ('launch', 'engine', 'main'))
            or any(identity not in new_objects for identity in old_objects)):
        raise ValueError('surface transition replaced its captured runtime or original frame')


def active(row):
    return row['active'] and row['visible'] and not row['defunct']


def before(operation, value):
    proof(value)
    selected = [row for row in value['surfaces'] if active(row)]
    if len(selected) != 1:
        raise ValueError('surface input requires exactly one owned active visible frame')
    row = selected[0]
    if operation in {'workspace-cancel-quit-v1', 'workspace-focus-quit-v1', 'workspace-approve-quit-v1', 'presence-cancel-v1'}:
        expected = 'presence-dialog' if operation == 'presence-cancel-v1' else 'quit-dialog'
        if row['kind'] != expected:
            raise ValueError('dialog input requires the actual selected owned modal dialog')
        if operation in {'workspace-focus-quit-v1', 'workspace-approve-quit-v1'}:
            kind = 'quit-button' if operation == 'workspace-approve-quit-v1' else 'keep-working-button'
            if value['control'] is None or value['control']['kind'] != kind:
                raise ValueError('Quit input requires its actual declared focused native button')
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
    if operation == 'workspace-approve-quit-v1':
        proof(old)
        if (not isinstance(new, dict) or set(new) != {'launch', 'engine', 'main', 'exited', 'exit_code'}
                or any(new[key] != old[key] for key in ('launch', 'engine', 'main'))
                or new['exited'] != {'launch': True, 'engine': True, 'frame': True}
                or any(type(value) is not bool for value in new['exited'].values())
                or type(new['exit_code']) is not int or new['exit_code'] != 0):
            raise ValueError('Quit approval requires independent owned-process exit observations')
        return True
    same_runtime(old, new)
    main = next(row for row in new['surfaces'] if row['identity'] == new['main'])
    if operation == 'workspace-open-peer-v1':
        previous = [row['identity'] for row in old['surfaces']]
        created = [row for row in new['surfaces'] if row['kind'] == 'frame'
                   and row['identity'] not in previous]
        return (main['visible'] and not main['defunct'] and len(created) == 1
                and active(created[0]))
    if operation == 'workspace-hide-main-v1':
        return not main['visible'] and not main['defunct'] and any(
            row['kind'] == 'frame' and row['identity'] != new['main'] and active(row)
            for row in new['surfaces'])
    if operation == 'workspace-focus-quit-v1':
        return new['control'] is not None and new['control']['kind'] == 'quit-button'
    if operation == 'workspace-request-quit-v1':
        return any(row['kind'] == 'quit-dialog' and active(row) for row in new['surfaces'])
    if operation in {'workspace-cancel-quit-v1', 'presence-cancel-v1'}:
        kind = 'presence-dialog' if operation == 'presence-cancel-v1' else 'quit-dialog'
        return active(main) and not any(row['kind'] == kind and row['visible']
                                       for row in new['surfaces'])
    raise ValueError('surface input requires a fixed declared operation')
