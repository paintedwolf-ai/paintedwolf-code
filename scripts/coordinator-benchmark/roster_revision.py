"""Replace an explicitly withdrawn candidate without resampling retained models."""
import copy
import uuid
from snapshot import identity


def replace_candidate(plan, removed, replacement):
    previous = next(m for m in plan['models'] if m['id'] == removed)
    if replacement['id'] in {m['id'] for m in plan['models']}:
        raise ValueError('a replacement needs a new candidate identity')
    common = lambda config: {k: v for k, v in config.items() if k not in {'coordinator', 'configuration_sha256'}}
    if common(replacement['configuration']) != common(previous['configuration']):
        raise ValueError('replacement changes the application or supporting models')
    revised = copy.deepcopy(plan)
    revised['models'] = [copy.deepcopy(replacement) if m['id'] == removed else m for m in revised['models']]
    public_ref = {k: v for k, v in replacement.items() if k != 'configuration'}
    revised['roster']['models'] = [public_ref if m['id'] == removed else m for m in revised['roster']['models']]
    policy = revised['execution']['policy']
    mapping = {p: resource['id'] for resource in policy['resources'] for p in resource['providers']}
    config = replacement['configuration']
    refs = [config['coordinator'], config['lite'], *config['workers']]
    if any(ref['provider'] not in mapping for ref in refs):
        raise ValueError('replacement requires an unplanned provider capacity')
    policy['models'].pop(removed)
    policy['models'][replacement['id']] = {'coordinator': mapping[refs[0]['provider']],
        'resources': sorted({mapping[ref['provider']] for ref in refs})}
    used = {resource for model in policy['models'].values() for resource in model['resources']}
    policy['resources'] = [r for r in policy['resources'] if r['id'] in used]
    next_index = max(item['index'] for item in revised['episodes']) + 1
    for item in revised['episodes']:
        if item['model'] == removed:
            item['model'], item['index'] = replacement['id'], next_index
            next_index += 1
    revised['benchmark']['execution'] = copy.deepcopy(revised['execution'])
    revised['comparison_id'] = identity({'benchmark': revised['benchmark'],
                                        'application_configuration': common(replacement['configuration'])})
    revised['run_id'] = str(uuid.uuid4())
    return revised


def retained_records(previous, revised, records):
    old_items = {str(i['index']): i for i in previous['episodes']}
    retained = {}
    for item in revised['episodes']:
        key = str(item['index'])
        if key not in records:
            continue
        if old_items.get(key) != item:
            raise ValueError('completed capture identity changed')
        if 'error' in records[key]:
            raise ValueError('an unresolved attempt cannot become completed')
        retained[key] = copy.deepcopy(records[key])
    return retained
