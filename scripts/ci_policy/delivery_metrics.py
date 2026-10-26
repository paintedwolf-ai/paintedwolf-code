"""Count original changes delivered by ordinary and automatic batch merges."""
from .queue_batching.selection import manifest, PREFIX


def delivered(pulls):
    originals, batches, unresolved = set(), [], []
    for pull in pulls:
        if not pull.get('headRefName', '').startswith(PREFIX):
            originals.add(pull['number'])
            continue
        if (pull.get('author') or {}).get('login') != 'github-actions':
            unresolved.append(pull['number'])
            continue
        try:
            record = manifest(pull.get('body', ''))
            if record is None:
                raise ValueError('missing batch manifest')
        except (ValueError, KeyError, TypeError):
            unresolved.append(pull['number'])
            continue
        originals.update(row['number'] for row in record['sources'])
        batches.append(pull['number'])
    return {'original_prs': sorted(originals), 'batch_prs': sorted(batches), 'unresolved_batch_prs': sorted(unresolved)}
