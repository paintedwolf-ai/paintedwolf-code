"""Apply availability patience only to isolated benchmark providers."""
import copy
import yaml


def availability_policy(provider, catalog, availability):
    if provider.get('http_retry') is not None:
        policy = copy.deepcopy(provider['http_retry'])
    else:
        kind = provider.get('kind', provider['id'])
        shipped = next((p for p in catalog['providers'] if p.get('kind', p['id']) == kind), None)
        if shipped is None:
            raise ValueError('provider has no retry policy: ' + provider['id'])
        profile = provider.get('http_retry_profile', shipped.get('http_retry_profile'))
        policy = copy.deepcopy(catalog['http_retry_profiles'][profile]['http_retry'] if profile else shipped['http_retry'])
    capacity_statuses = set((policy.get('capacity') or {}).get('statuses', []))
    policy['statuses'] = sorted(set(policy.get('statuses', [])) | (set(availability['statuses']) - capacity_statuses))
    transport = policy.get('transport')
    if transport and transport.get('faults'):
        transport['faults'] = sorted(set(transport['faults']) | set(availability['transport_faults']))
    policy['availability'] = {key: availability[key] for key in ('initial_ms', 'max_backoff_ms')}
    provider.pop('http_retry_profile', None)
    provider['http_retry'] = policy


def configure_availability(providers, used, source, availability):
    catalog = yaml.safe_load((source / 'lycaon/config/packs/painted-wolf/platform/host/providers.yaml').read_text())
    for provider in providers:
        if provider['id'] in used:
            availability_policy(provider, catalog, availability)
