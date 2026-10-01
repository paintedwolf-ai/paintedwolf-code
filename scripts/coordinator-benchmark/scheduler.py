"""Admit episodes only when every shared execution resource has capacity."""
import math
import time
from collections import Counter
from concurrent.futures import FIRST_COMPLETED, ThreadPoolExecutor, wait
from urllib.parse import urlsplit


def positive(value):
    if type(value) is not int or value < 1:
        raise ValueError('execution limits must be positive integers')
    return value


def execution_policy(models, providers, max_active=5, cloud_concurrency=2, overrides=None):
    positive(max_active)
    positive(cloud_concurrency)
    overrides = overrides or {}
    by_id = {p['id']: p for p in providers}
    used = {}
    requirements = {}
    for model in models:
        config = model['configuration']
        refs = [config['coordinator'], config['lite'], *config['workers']]
        requirements[model['id']] = [ref['provider'] for ref in refs]
        for ref in refs:
            provider = by_id[ref['provider']]
            url = urlsplit(provider['base_url'])
            if not url.hostname:
                raise ValueError('provider requires an absolute endpoint')
            # Aliases on one server share capacity, including different API paths.
            key = (url.scheme.lower(), url.hostname.lower(), url.port or (443 if url.scheme == 'https' else 80))
            used[provider['id']] = (key, provider['kind'] == 'ollama')
    if set(overrides) - set(used):
        raise ValueError('provider limit names an unused provider')
    for value in overrides.values():
        positive(value)
    groups = {}
    for provider, (key, local) in sorted(used.items()):
        group = groups.setdefault(key, {'providers': [], 'local': False})
        group['providers'].append(provider)
        group['local'] |= local
    resources, mapping = [], {}
    for group in groups.values():
        limits = {overrides[p] for p in group['providers'] if p in overrides}
        if len(limits) > 1:
            raise ValueError('aliases for one endpoint have conflicting provider limits')
        limit = next(iter(limits), 1 if group['local'] else max_active)
        if group['local'] and limit != 1:
            raise ValueError('Ollama endpoints require one episode at a time')
        resource = {'id': 'provider-' + str(len(resources)), 'providers': group['providers'],
                    'kind': 'ollama' if group['local'] else 'cloud', 'max_active': limit,
                    'max_coordinators': 1 if group['local'] else min(cloud_concurrency, limit)}
        resources.append(resource)
        mapping.update({p: resource['id'] for p in group['providers']})
    return {'scheduler': 'resource-admission-v2', 'max_active': max_active, 'resources': resources,
            'models': {model: {'coordinator': mapping[refs[0]], 'resources': sorted({mapping[p] for p in refs})}
                       for model, refs in requirements.items()}}


class Admission:
    def __init__(self, policy):
        self.policy = policy
        self.used = Counter()
        self.limits = {'host': policy['max_active']}
        for resource in policy['resources']:
            self.limits[resource['id']] = resource['max_active']
            self.limits['coordinator:' + resource['id']] = resource['max_coordinators']

    def keys(self, item):
        model = self.policy['models'][item['model']]
        return ['host', 'coordinator:' + model['coordinator'], *model['resources']]

    def available(self, item):
        return all(self.used[key] < self.limits[key] for key in self.keys(item))

    def acquire(self, item):
        self.used.update(self.keys(item))

    def release(self, item):
        self.used.subtract(self.keys(item))


def dispatch(pending, policy, execute, cancelled, started, finished, heartbeat=lambda: None):
    """Keep FIFO order among eligible episodes without parking executor threads."""
    pending = list(pending)
    admission = Admission(policy)
    active = {}
    eligible_at = {}
    halted = False
    with ThreadPoolExecutor(max_workers=policy['max_active']) as pool:
        try:
            while pending or active:
                if not cancelled.is_set() and not halted:
                    for item in pending[:]:
                        if cancelled.is_set():
                            break
                        if eligible_at.get(item['index'], 0) > time.time() or not admission.available(item):
                            continue
                        # Persist admission before a thread can spend tokens.
                        started(item)
                        admission.acquire(item)
                        active[pool.submit(execute, item)] = item
                        pending.remove(item)
                if not active:
                    if not pending or cancelled.is_set() or halted:
                        break
                    delay = min(eligible_at.get(item['index'], 0) for item in pending) - time.time()
                    cancelled.wait(max(0, min(1, delay)))
                    heartbeat()
                    continue
                done, _ = wait(active, timeout=1, return_when=FIRST_COMPLETED)
                for future in done:
                    item = active.pop(future)
                    admission.release(item)
                    try:
                        record = future.result()
                    except Exception as exc:
                        halted = True
                        record = {'error': 'runner_failed', 'error_type': type(exc).__name__}
                    if record and (record.get('error') or record.get('blocked') == 'execution_unknown'):
                        halted = True
                    if record and 'retry_at' in record:
                        retry_at = record['retry_at']
                        if type(retry_at) not in {int, float} or not math.isfinite(retry_at):
                            raise ValueError('episode retry requires a finite timestamp')
                        eligible_at[item['index']] = retry_at
                        pending.append(item)
                    finished(item, record)
                heartbeat()
        finally:
            # Checkpoint failure cancels active episodes.
            if active:
                cancelled.set()

    return not halted and not pending and not cancelled.is_set()
