"""Closed public facts for native Polkit observation; no secret or input authority."""
from __future__ import annotations

import re
import uuid

ACTION = 'dev.paintedwolf.code.confirm-presence'

FIELDS = {'format_version', 'challenge', 'action_id', 'authority', 'sender', 'request_id',
          'phase', 'transport', 'subject', 'authorization'}
UNIQUE = re.compile(r':[0-9]+\.[0-9]+\Z')


def _uuid(value):
    return isinstance(value, str) and len(value) == 36 and str(uuid.UUID(value)) == value


def validate(row):
    if (not isinstance(row, dict) or set(row) != FIELDS
            or type(row['format_version']) is not int or row['format_version'] != 1
            or row['action_id'] != ACTION or not _uuid(row['request_id'])
            or not isinstance(row['phase'], str) or row['phase'] not in {'probe', 'interactive'}
            or not isinstance(row['transport'], str) or row['transport'] not in {'pending', 'returned', 'timed_out', 'failed'}
            or any(not isinstance(row[key], str) or len(row[key]) > 64 or not UNIQUE.fullmatch(row[key])
                   for key in ('authority', 'sender'))):
        raise ValueError('native Polkit attempt has an invalid fixed shape')
    challenge = row['challenge']
    if (not isinstance(challenge, dict) or set(challenge) != {'challenge_id', 'purpose',
            'window_label', 'step_away_generation'} or not _uuid(challenge['challenge_id'])
            or not isinstance(challenge['purpose'], str) or challenge['purpose'] not in {'reveal', 'unlock'}
            or challenge['window_label'] != 'main'
            or type(challenge['step_away_generation']) is not int or challenge['step_away_generation'] < 0):
        raise ValueError('native Polkit attempt has no exact confirmation challenge')
    subject = row['subject']
    if (not isinstance(subject, dict) or set(subject) != {'pid', 'start_time_ticks', 'uid'}
            or any(type(subject[key]) is not int or subject[key] < 0 for key in subject)
            or subject['pid'] <= 1 or subject['start_time_ticks'] <= 0):
        raise ValueError('native Polkit attempt has no bound process subject')
    authorization = row['authorization']
    if authorization is not None and (row['transport'] != 'returned'
            or not isinstance(authorization, dict) or set(authorization) != {'authorized', 'challenge'}
            or any(type(item) is not bool for item in authorization.values())):
        raise ValueError('native Polkit attempt has contradictory authorization facts')
    return row

