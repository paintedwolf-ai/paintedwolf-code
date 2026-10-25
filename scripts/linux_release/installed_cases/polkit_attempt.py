"""Parse bounded native Polkit observations; logs never authorize input or host release."""
from __future__ import annotations

import json

from .desktop_ui import owned_process
from .native_session import kernel_owner
from .presence_confirmation import unique_fields
from ..installed_input.polkit_wire import FIELDS, validate

PREFIX = b'Native polkit attempt: '

def attempt_rows(app, native_owner):
    app.require_running()
    app.require_engine()
    launch, engine = dict(app.observations['launch_identity']), dict(app.observations['engine_identity'])
    if (kernel_owner(native_owner['pid'], native_owner['uid']) != native_owner
            or not owned_process(native_owner['pid'], app)):
        raise ValueError('native Polkit caller escaped the selected installed runtime')
    result, counts = [], {}
    for line in app.streams().get('app-stderr', b'').splitlines(keepends=True):
        if not line.endswith(b'\n') or not line.startswith(PREFIX):
            continue
        if len(line) > 8192 or len(result) >= 128:
            raise ValueError('native Polkit attempts exceed their observation bound')
        row = validate(json.loads(line[len(PREFIX):], object_pairs_hook=unique_fields))
        if row['subject'] != {key: native_owner[key] for key in ('pid', 'start_time_ticks', 'uid')}:
            raise ValueError('native Polkit subject differs from the independently selected native caller')
        request = row['request_id']
        prior = counts.setdefault(request, [])
        if prior and ({key: prior[0][key] for key in FIELDS - {'transport', 'authorization'}}
                      != {key: row[key] for key in FIELDS - {'transport', 'authorization'}}):
            raise ValueError('native Polkit request replaced its captured identity')
        if (not prior and row['transport'] != 'pending' or prior and
                (len(prior) != 1 or prior[0]['transport'] != 'pending' or row['transport'] == 'pending')):
            raise ValueError('native Polkit request replayed or omitted its call boundary')
        prior.append(row)
        result.append(row)
    app.require_running()
    app.require_engine()
    if (app.observations['launch_identity'] != launch or app.observations['engine_identity'] != engine
            or kernel_owner(native_owner['pid'], native_owner['uid']) != native_owner):
        raise ValueError('native Polkit observation changed its installed runtime')
    return result
