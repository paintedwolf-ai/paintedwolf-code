"""Bind required support variants to actual release, snapshot and monitor facts."""
from __future__ import annotations

import hashlib
import json
import re

from .installed_environment import query
from .installed_cases.records import CaseUnavailable


def requirements(catalog: dict, environment_id: str, capability: str) -> list[dict]:
    # The typed catalog owner lands independently before this execution consumer.
    from .support_variants import variants
    environments = {item['id']: item for item in catalog['environments']}
    if environment_id not in environments:
        raise ValueError('qualification selected an undeclared environment')
    return variants(environments[environment_id], capability, {item['id'] for item in catalog['capabilities']})


def snapshot(value: object, variant: dict) -> dict:
    if not isinstance(value, dict) or set(value) != {'kind', 'value', 'channel'}:
        raise ValueError('qualification snapshot requires an exact typed identity')
    release = variant['release']
    pattern = r'[a-f0-9]{40}' if release['snapshot_kind'] == 'nixos-revision' else r'[a-f0-9]{64}'
    if (value['kind'] != release['snapshot_kind'] or value['channel'] != release['channel']
            or not isinstance(value['value'], str) or not re.fullmatch(pattern, value['value'])):
        raise ValueError('qualification snapshot differs from the selected release authority')
    return value


def observe(variant: dict, selected_snapshot: dict, observations: dict, binding: dict) -> dict:
    snapshot(selected_snapshot, variant)
    release, actual = variant['release'], observations['os_release']
    if actual['ID'] not in release['os_ids']:
        raise ValueError('actual distribution differs from the selected qualification variant')
    version = actual.get('VERSION_ID')
    if actual['ID'] in {'rhel', 'rocky', 'almalinux', 'centos'} and version:
        version = version.split('.')[0]
    if release['version_id'] is not None and version != release['version_id']:
        raise ValueError('actual release differs from the selected qualification variant')
    if observations['session']['Type'] != variant['display']['protocol']:
        raise ValueError('actual session protocol differs from the selected qualification variant')
    if selected_snapshot['kind'] == 'nixos-revision':
        from .installed_cases.nix_factory import observe as observe_factory
        factory = observe_factory(binding, selected_snapshot)
    else:
        factory = None
        inventory = json.dumps(observations['packages'], sort_keys=True).encode()
        if hashlib.sha256(inventory).hexdigest() != selected_snapshot['value']:
            raise ValueError('actual installed package snapshot differs from the independently selected plan')
    if variant['display']['xwayland'] == 'disabled' or variant['display']['clipboard_manager'] == 'absent':
        raise CaseUnavailable('actual-xwayland-and-clipboard-absence-observer-required')
    monitors = gnome_monitors(observations['session'])
    if monitors['scales_percent'] != sorted(variant['scales_percent']):
        raise ValueError('actual logical monitor scales differ from the selected qualification variant')
    return {'format_version': 1, 'variant': variant, 'snapshot': selected_snapshot, 'monitors': monitors, 'factory': factory}


def gnome_monitors(session: dict) -> dict:
    if session.get('Desktop', '').casefold() not in {'gnome', 'ubuntu', 'gnome-xorg'}:
        raise CaseUnavailable('actual-compositor-monitor-observer-required')
    import os
    response = query(['busctl', '--address=unix:path=/run/user/' + str(os.getuid()) + '/bus', '--json=short', 'call',
                      'org.gnome.Mutter.DisplayConfig', '/org/gnome/Mutter/DisplayConfig',
                      'org.gnome.Mutter.DisplayConfig', 'GetCurrentState'])
    if response['exit_code'] != 0:
        raise CaseUnavailable('actual-mutter-monitor-state-unavailable')
    state = json.loads(response['stdout'])
    signature = 'ua((ssss)a(siiddada{sv})a{sv})a(iiduba(ssss)a{sv})a{sv}'
    if set(state) != {'type', 'data'} or state['type'] != signature or len(state['data']) != 4:
        raise ValueError('Mutter monitor state has an unexpected typed shape')
    logical = state['data'][2]
    if not isinstance(logical, list) or not 1 <= len(logical) <= 16:
        raise ValueError('Mutter logical monitor count is invalid')
    scales = []
    for monitor in logical:
        if (not isinstance(monitor, list) or len(monitor) != 7 or type(monitor[2]) not in {int, float}
                or not 1 <= monitor[2] <= 4 or not isinstance(monitor[5], list) or not monitor[5]):
            raise ValueError('Mutter logical monitor has an invalid scale or physical binding')
        percent = monitor[2] * 100
        if percent != int(percent):
            raise ValueError('Mutter monitor scale is not an exact declared percentage')
        scales.append(int(percent))
    return {'scales_percent': sorted(set(scales)), 'state': state}


def aggregate(catalog: dict, observations: dict[str, list[tuple[str, dict]]], known: dict) -> list[dict]:
    results = []
    for row_id, variants in sorted(observations.items()):
        selected = known[row_id]
        required = {item['id'] for item in requirements(catalog, selected['environment'], selected['capability'])}
        actual = {identifier: row for identifier, row in variants}
        if len(actual) != len(variants) or set(actual) - required:
            raise ValueError('campaign repeats or invents a required support variant')
        first = next(iter(actual.values()))
        cases = []
        for case_id in selected['cases']:
            samples = [next(case for case in row['cases'] if case['id'] == case_id) for row in actual.values()]
            status = ('failed' if any(case['status'] == 'failed' for case in samples) else
                      'unverified' if set(actual) != required or any(case['status'] != 'passed' for case in samples) else 'passed')
            cases.append({'id': case_id, 'status': status, 'evidence': [record for case in samples for record in case['evidence']]})
        status = ('failed' if any(case['status'] == 'failed' for case in cases) else
                  'unverified' if any(case['status'] != 'passed' for case in cases) else 'passed')
        facts = {name: json.dumps({identifier: {'sha256': hashlib.sha256(row['environment_facts'][name].encode()).hexdigest(),
                                               'bytes': len(row['environment_facts'][name].encode())}
                                  for identifier, row in sorted(actual.items())}, sort_keys=True)
                 for name in first['environment_facts']}
        results.append({**first, 'status': status, 'cases': cases, 'environment_facts': facts})
    return results
