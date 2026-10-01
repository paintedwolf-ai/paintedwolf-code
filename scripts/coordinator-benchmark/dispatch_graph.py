"""Declared worker prerequisites and their independent execution waves."""
import json


def dependency_check_id(prerequisite, dependent):
    return 'dispatch-after-integration:' + json.dumps([prerequisite, dependent], separators=(',', ':'))


def waves(dispatches, dependencies):
    by_label = {d['label']: d for d in dispatches}
    if len(by_label) != len(dispatches):
        raise ValueError('dispatch labels must be unique')
    if not set(dependencies) <= set(by_label):
        raise ValueError('a dependent worker is undeclared')
    for label, prerequisites in dependencies.items():
        if (not isinstance(prerequisites, list) or not prerequisites
                or len(set(prerequisites)) != len(prerequisites)
                or not set(prerequisites) <= set(by_label) or label in prerequisites):
            raise ValueError('worker prerequisites must name distinct other assignments')
        if any(by_label[p]['mode'] != 'write' for p in prerequisites):
            raise ValueError('integration prerequisites must be write assignments')
    done = set()
    result = []
    while len(done) < len(by_label):
        ready = [d for d in dispatches if d['label'] not in done
                 and set(dependencies.get(d['label'], [])) <= done]
        if not ready:
            raise ValueError('worker dependencies contain a cycle')
        result.append(ready)
        done.update(d['label'] for d in ready)
    return result
