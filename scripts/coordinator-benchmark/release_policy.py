"""Evaluate an explicitly selected target against an optional benchmark cadence."""
import argparse
import copy
import json
import pathlib
import sys
import yaml

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
from release_semver import parse


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


def decision(version, cadence='selected'):
    parsed = parse(version)
    if cadence not in {'selected', 'major-minor'}:
        raise ValueError('unknown benchmark cadence')
    if cadence == 'selected':
        reason = 'selected_target'
    elif parsed.prerelease:
        reason = 'prerelease'
    elif parsed.patch:
        reason = 'patch_release'
    else:
        reason = 'major_or_minor_release'
    return {'version': version, 'cadence': cadence,
            'eligible': reason in {'selected_target', 'major_or_minor_release'}, 'reason': reason}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', help='SemVer without a tag prefix; defaults to repository VERSION')
    parser.add_argument('--cadence', choices=['selected', 'major-minor'], default='selected')
    args = parser.parse_args()
    version = args.version or (pathlib.Path(__file__).resolve().parents[2] / 'VERSION').read_text().strip()
    try:
        print(json.dumps(decision(version, args.cadence)))
    except ValueError as exc:
        parser.error(str(exc))
