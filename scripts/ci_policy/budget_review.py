"""Architectural review evidence and bounded warning acceptance."""
import re

KINDS = frozenset({'runtime_service', 'construction', 'data_record', 'projection',
                   'algorithm', 'test_suite', 'module'})
STATES = frozenset({'refactor', 'retained', 'ceiling_exceeded', 'boundary_changed', 'shape_changed'})
FIELDS = frozenset({'kind', 'workstream', 'disposition', 'reason', 'reviewed_commit',
                    'ceiling', 'boundaries', 'state', 'reviewed_shape'})


def validate_review(review, row, relative):
    if not isinstance(review, dict) or set(review) != FIELDS:
        raise ValueError('invalid architectural review shape')
    if (not isinstance(review['kind'], str) or review['kind'] not in KINDS
            or not isinstance(review['disposition'], str) or review['disposition'] not in {'refactor', 'retain'}):
        raise ValueError('invalid architectural review classification')
    if not isinstance(review['workstream'], str) or not re.fullmatch('[a-z0-9-]{1,80}', review['workstream']):
        raise ValueError('invalid architectural workstream')
    if (not isinstance(review['reason'], str) or not review['reason'].strip()
            or len(review['reason']) > 4096 or not isinstance(review['reviewed_commit'], str)
            or not re.fullmatch('[0-9a-f]{40}', review['reviewed_commit'])):
        raise ValueError('missing architectural review provenance')
    if not isinstance(review['reviewed_shape'], str):
        raise ValueError('invalid reviewed type shape')
    if type(review['ceiling']) is not int or not isinstance(review['boundaries'], dict):
        raise ValueError('invalid architectural review bounds')
    if not isinstance(review['state'], str) or review['state'] not in STATES:
        raise ValueError('invalid architectural review state')
    if review['disposition'] == 'refactor':
        if review['ceiling'] or review['boundaries'] or review['reviewed_shape'] or review['state'] != 'refactor':
            raise ValueError('refactor review cannot accept a warning')
        return
    if not row['warn'] < review['ceiling'] <= row['limit'] or not 1 <= len(review['boundaries']) <= 32:
        raise ValueError('retained warning exceeds admission limit or lacks boundaries')
    for path, digest in review['boundaries'].items():
        if not relative(path) or not isinstance(digest, str) or not re.fullmatch('[0-9a-f]{64}', digest):
            raise ValueError('invalid architectural review boundary')
    shape = review['reviewed_shape']
    if row['category'].startswith('go_'):
        if not isinstance(shape, str) or not re.fullmatch('[0-9a-f]{64}', shape):
            raise ValueError('retained Go artifact requires its reviewed shape')
    elif shape:
        raise ValueError('unexpected reviewed type shape')
    if review['state'] == 'retained' and shape != row.get('shape', ''):
        raise ValueError('retained review has a changed type shape')
    if review['state'] == 'shape_changed' and (not shape or shape == row.get('shape', '')):
        raise ValueError('shape change lacks different type evidence')
    exceeded = row['measured'] > review['ceiling']
    if (review['state'] == 'refactor' or exceeded != (review['state'] == 'ceiling_exceeded')):
        raise ValueError('architectural review contradicts its measurement')


def retained(row):
    return row is not None and row.get('review', {}).get('state') == 'retained'


def unchanged(row, previous):
    fields = ('category', 'id', 'measured', 'warn', 'limit', 'effective_cap', 'exception_reason', 'review', 'shape')
    return (all(row.get(k) == previous.get(k) for k in fields)
            and row['sources'][:20] == previous.get('sources'))


def dismissed(issue, row, previous, intake_keys, identity):
    return (row is not None and not row.get('review') and issue['state'] == 'closed'
            and issue.get('state_reason') == 'not_planned' and identity not in intake_keys
            and unchanged(row, previous))
