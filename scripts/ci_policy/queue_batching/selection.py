"""Bounded selection preserves queue order, active groups and explicit campaign holds."""
from fnmatch import fnmatchcase
import hashlib
import json
import re

PREFIX = 'automation/queue-batch-'
HOLD = 'no-combine'
MARKER = '\n<!-- queue-batch-v1\n'


def manifest(body):
    if MARKER not in (body or ''):
        return None
    raw = body.split(MARKER, 1)[1].split('\n-->', 1)[0]
    value = json.loads(raw)
    if value.get('version') != 1 or not 2 <= len(value.get('sources', [])) <= 4:
        raise ValueError('invalid automatic batch manifest')
    if not re.fullmatch('[0-9a-f]{40}', value.get('batch_head', '')):
        raise ValueError('batch head identity is missing')
    for row in value['sources']:
        if type(row.get('number')) is not int or not re.fullmatch('[0-9a-f]{40}', row.get('headRefOid', '')):
            raise ValueError('invalid source identity')
    return value


def identity(base, sources):
    raw = json.dumps([base, [(p['number'], p['headRefOid']) for p in sources]], separators=(',', ':'))
    return PREFIX + hashlib.sha256(raw.encode()).hexdigest()[:20]


def held(pull, files, policy):
    return (HOLD in pull.get('labels', [])
            or any(fnmatchcase(pull['headRefName'], pattern) for pattern in policy['held_branches'])
            or any(path.startswith('.github/workflows/') for path in files)
            or any(fnmatchcase(path, pattern) for path in files for pattern in policy['held_paths']))


def select(entries, policy, files, excluded):
    selected = []
    # GitHub can build cumulative groups ahead of the first entry. Reserve the
    # configured build window even when a group's jobs have not started yet.
    for entry in entries[policy['preserve_front']:]:
        pull = entry['pullRequest']
        key = (pull['number'], pull['headRefOid'])
        if (entry['state'] != 'QUEUED' or pull['isDraft'] or pull['headRefName'].startswith(PREFIX)
                or key in excluded or held(pull, files(pull['number']), policy)):
            break  # Never leapfrog a held, active or individually isolated entry.
        selected.append(pull)
        if len(selected) == policy['maximum']:
            break
    return selected if len(selected) > 1 else []


def unchanged(current, recorded):
    return current['state'] == 'OPEN' and not current['isDraft'] and current['headRefOid'] == recorded['headRefOid']
