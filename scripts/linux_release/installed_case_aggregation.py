"""Combine disjoint, controller-bound case executions without replacing failures."""
from __future__ import annotations

import hashlib
import json

from .installed_input.protocol import binding
from .installed_coverage import mismatches
from .installed_variants import requirements


def platform_facts(facts):
    """Session/account incarnation is retained per execution, not a platform conflict."""
    result = dict(facts)
    for name in ('desktop-compositor-display-protocol', 'session-and-portal-provider-versions'):
        if name not in result:
            continue
        value = json.loads(result[name])
        session = value if name == 'desktop-compositor-display-protocol' else value['session']
        # Dedicated accounts and sessions are fresh for each one-use OS credential.
        for key in ('id', 'User', 'DesktopOwner', 'Display'):
            session.pop(key, None)
        result[name] = json.dumps(value, sort_keys=True)
    return result


def merge_variant(variant, samples, selected):
    identifier = variant['id']
    first, first_execution = samples[0]
    identity = {key: first_execution[key] for key in ('source_commit', 'driver_tree', 'artifact_id', 'environment_id')}
    claims, seen, facts = {}, set(), {}
    explicit_failure, independent_unverified = False, False
    expected_facts = platform_facts(first['environment_facts'])
    if len(first['variants']) != 1:
        raise ValueError('case execution requires one exact support variant')
    observed = first['variants'][0]['observed']
    platform_valid = not mismatches(observed, variant)
    fallback = {case['id']: [] for case in first['cases']}
    for row, execution in samples:
        binding(execution)
        if (execution['variant_id'] != identifier
                or any(execution[key] != value for key, value in identity.items())
                or row['source_commit'] != execution['source_commit']
                or row['artifact_id'] != execution['artifact_id']
                or execution['environment_id'] != selected['environment']
                or row['row_id'] != selected['id']):
            raise ValueError('case execution changed its artifact/source/driver/environment binding')
        nonce = execution['execution_nonce']
        if nonce in seen:
            raise ValueError('case aggregation reuses an execution nonce or input custody')
        seen.add(nonce)
        if (platform_facts(row['environment_facts']) != expected_facts
                or len(row['variants']) != 1 or row['variants'][0]['id'] != identifier
                or row['variants'][0]['observed'] != observed):
            raise ValueError('case executions conflict on observed platform facts or support variant')
        # The selection can include other capabilities; declaredness is validated
        # by the controller plan before execution, and only this row's claims merge.
        selection = execution['case_selection']
        requested = ({item['case'] for item in selection['cases'] if item['capability'] == selected['capability']}
                     if selection is not None else set(selected['cases']))
        if set(claims) & requested or requested - set(selected['cases']):
            raise ValueError('case executions overlap or invent a declared case claim')
        explicit_failure = explicit_failure or row['status'] == 'failed' or row['variants'][0]['status'] == 'failed'
        independent_unverified = independent_unverified or row['status'] == 'skipped' or row['variants'][0]['status'] == 'skipped'
        if all(case['status'] == 'passed' for case in row['cases']):
            independent_unverified = independent_unverified or row['status'] == 'unverified' or row['variants'][0]['status'] == 'unverified'
        elif row['status'] == 'passed':
            raise ValueError('complete row pass contradicts a non-passing case result')
        actual = {case['id']: case for case in row['cases']}
        if len(actual) != len(row['cases']) or set(actual) != set(selected['cases']):
            raise ValueError('case execution omitted or repeated a declared case result')
        for key, case in actual.items():
            fallback[key].extend(case['evidence'])
            if key in requested:
                claims[key] = case
            elif case['status'] != 'unverified':
                raise ValueError('unselected case execution made a success or failure claim')
        facts[nonce] = row['environment_facts']
    cases = [claims.get(key, {'id': key, 'status': 'unverified', 'evidence': fallback[key]})
             for key in selected['cases']]
    status = ('failed' if explicit_failure or any(case['status'] == 'failed' for case in cases) else
              'unverified' if independent_unverified or not platform_valid or any(case['status'] != 'passed' for case in cases) else 'passed')
    return {**first, 'cases': cases, 'status': status,
            'variants': [{'id': identifier, 'status': status, 'observed': observed}]}, facts


def aggregate(catalog, observations, known):
    results = []
    for row_id, executions in sorted(observations.items()):
        selected = known[row_id]
        required = {item['id']: item for item in requirements(catalog, selected['environment'], selected['capability'])}
        grouped, nonces, authority, mode = {}, set(), None, None
        for identifier, row, execution in executions:
            binding(execution)
            current_mode = row['tier'], row['execution']
            if (row['tier'] != selected['tier'] or row['execution'] not in {'native', 'emulated'}
                    or row['execution'] == 'emulated' and not selected['emulation_allowed']
                    or mode is not None and mode != current_mode):
                raise ValueError('case execution changed its tier/execution or violated its native floor')
            mode = current_mode
            current = tuple(execution[key] for key in ('source_commit', 'driver_tree', 'artifact_id', 'environment_id'))
            if authority is not None and current != authority:
                raise ValueError('case execution changed its artifact/source/driver/environment binding')
            authority = current
            if execution['execution_nonce'] in nonces:
                raise ValueError('case aggregation reuses an execution nonce or input custody')
            nonces.add(execution['execution_nonce'])
            if identifier not in required:
                raise ValueError('campaign invents a required support variant')
            grouped.setdefault(identifier, []).append((row, execution))
        actual, captured_facts = {}, {}
        for identifier, samples in grouped.items():
            actual[identifier], captured_facts[identifier] = merge_variant(required[identifier], samples, selected)
        snapshots = {}
        for identifier, row in actual.items():
            snapshot = row['variants'][0]['observed']['snapshot']['value']
            release = required[identifier]['release']['id']
            snapshots.setdefault(snapshot, []).append((release, identifier))
        for samples in snapshots.values():
            if len({release for release, _ in samples}) > 1:
                for _, identifier in samples:
                    if actual[identifier]['status'] != 'failed':
                        actual[identifier]['status'] = 'unverified'
                        actual[identifier]['variants'][0]['status'] = 'unverified'
        first = next(iter(actual.values()))
        cases = []
        for key in selected['cases']:
            samples = [next(case for case in row['cases'] if case['id'] == key) for row in actual.values()]
            status = ('failed' if any(case['status'] == 'failed' for case in samples) else
                      'unverified' if set(actual) != set(required) or any(case['status'] != 'passed' for case in samples) else 'passed')
            cases.append({'id': key, 'status': status, 'evidence': [record for case in samples for record in case['evidence']]})
        status = ('failed' if any(row['status'] == 'failed' for row in actual.values()) or any(case['status'] == 'failed' for case in cases) else
                  'unverified' if any(row['status'] != 'passed' for row in actual.values()) or any(case['status'] != 'passed' for case in cases) else 'passed')
        facts = {name: json.dumps({identifier: {nonce: {
                    'sha256': hashlib.sha256(values[name].encode()).hexdigest(),
                    'bytes': len(values[name].encode())} for nonce, values in executions.items()}
                    for identifier, executions in sorted(captured_facts.items())}, sort_keys=True)
                 for name in first['environment_facts']}
        results.append({**first, 'status': status, 'cases': cases, 'environment_facts': facts,
                        'variants': [entry for row in actual.values() for entry in row['variants']]})
    return results
