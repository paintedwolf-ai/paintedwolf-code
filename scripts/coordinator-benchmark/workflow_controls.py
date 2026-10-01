"""Closed manual trajectories for workflow admission and grading controls."""
import copy


def plan(contract, controls, negative=None):
    receipts = copy.deepcopy(contract['receipts'])
    steps = copy.deepcopy(controls['steps'])
    failed = []
    if negative:
        if 'steps' in negative:
            steps = copy.deepcopy(negative['steps'])
        if 'receipt' in negative:
            index = negative['receipt']
            receipt = receipts[index]
            receipt['verdict'].update(negative.get('values', {}))
            receipt['phase'] = negative.get('phase', receipt['phase'])
            failed.append('workflow-receipt-'+contract['receipts'][index]['id'])
        if 'transition' in negative:
            steps[negative['transition']] = {'transition': negative['choice']}
        failed.extend(negative.get('must_fail', []))
        if not failed:
            raise ValueError('workflow negative control requires a named failed check')
    for step in steps:
        if type(step) is int:
            if not 0 <= step < len(receipts):
                raise ValueError('workflow control names an absent receipt')
        elif not isinstance(step, dict) or set(step) != {'transition'} or not isinstance(step['transition'], str):
            raise ValueError('workflow control requires a receipt index or named transition')
    return receipts, steps, failed
