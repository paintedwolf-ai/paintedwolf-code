"""Read native confirmation outcomes without treating them as host release authority."""
from __future__ import annotations

import json
import time
import uuid

from .records import CaseUnavailable

PREFIX = 'Native presence confirmation: '
FIELDS = {'format_version', 'challenge_id', 'purpose', 'window_label', 'generation',
          'status', 'authenticator', 'code', 'resolution'}
AUTHENTICATORS = {'linux_user_presence'}
CODES = {'presence_unavailable', 'presence_denied', 'presence_failed'}


def unique_fields(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError('native presence confirmation record repeats a field')
        value[key] = item
    return value


def confirmation_rows(app) -> list[dict]:
    app.require_running()
    app.require_engine()
    launch, engine = dict(app.observations['launch_identity']), dict(app.observations['engine_identity'])
    rows = []
    for line in app.streams().get('app-stderr', b'').splitlines(keepends=True):
        if not line.endswith(b'\n') or not line.startswith(PREFIX.encode()):
            continue
        if len(line) > 16384 or len(rows) >= 256:
            raise ValueError('native presence confirmation records exceed their bound')
        row = json.loads(line[len(PREFIX):], object_pairs_hook=unique_fields)
        if (not isinstance(row, dict) or set(row) != FIELDS
                or type(row['format_version']) is not int or row['format_version'] != 1
                or type(row['generation']) is not int or row['generation'] < 0
                or not isinstance(row['challenge_id'], str)
                or str(uuid.UUID(row['challenge_id'])) != row['challenge_id']
                or not isinstance(row['purpose'], str) or row['purpose'] not in {'reveal', 'unlock'}
                or not isinstance(row['window_label'], str) or not 0 < len(row['window_label']) <= 128
                or (row['resolution'] is not None and row['resolution'] != 'step_away')):
            raise ValueError('native presence confirmation record has an invalid shape')
        if row['status'] == 'confirmed':
            valid = isinstance(row['authenticator'], str) and row['authenticator'] in AUTHENTICATORS and row['code'] is None and row['resolution'] is None
        elif row['status'] == 'refused':
            valid = row['authenticator'] is None and isinstance(row['code'], str) and row['code'] in CODES
        else:
            valid = False
        if not valid:
            raise ValueError('native presence confirmation record has contradictory outcome facts')
        rows.append(row)
    app.require_running()
    app.require_engine()
    if app.observations['launch_identity'] != launch or app.observations['engine_identity'] != engine:
        raise ValueError('native presence confirmation runtime changed during observation')
    return rows


def await_confirmation(app, challenge_id: str | None = None, generation: int | None = None, *, after_count: int) -> dict:
    if (challenge_id is None) != (generation is None):
        raise ValueError('native presence confirmation requires both selected challenge facts')
    if type(after_count) is not int or after_count < 0:
        raise ValueError('native presence confirmation requires a captured record boundary')
    deadline = time.monotonic() + 15
    while True:
        rows = [row for row in confirmation_rows(app)[after_count:]
                if challenge_id is None or row['challenge_id'] == challenge_id]
        if len(rows) > 1:
            raise ValueError('native presence confirmation challenge has duplicate outcomes')
        if rows:
            row = rows[0]
            if ((generation is not None and row['generation'] != generation)
                    or row['purpose'] != 'reveal' or row['window_label'] != 'main'):
                raise ValueError('native presence confirmation differs from the selected reveal challenge')
            return row
        if time.monotonic() >= deadline:
            raise CaseUnavailable('native-presence-confirmation-outcome-unverified')
        time.sleep(0.05)
