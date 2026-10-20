"""Observe the shipped Bialy reference task without substituting a quality baseline."""
from __future__ import annotations

import json
import math
from pathlib import Path


from .bialy_data import unchanged
from .process import run
from .bialy_stdio import exchange, ExchangeFailure

POLICY = Path(__file__).resolve().parents[3] / 'lycaon/config/packs/painted-wolf/platform/host/decisions.yaml'


def policy() -> dict:
    try:
        import yaml
    except ImportError as error:
        from .records import CaseUnavailable
        raise CaseUnavailable('installed Bialy policy observation requires the YAML catalog loader') from error
    policy = yaml.safe_load(POLICY.read_text())
    values = {'reference_ms': policy['engine']['reference_ms'], 'turn_ms': policy['turn']['deadline_ms']}
    if any(type(value) is not int or value <= 0 for value in values.values()):
        raise ValueError('existing Bialy performance policy has invalid absolute limits')
    return policy


def limits() -> dict:
    document = policy()
    return {"reference_ms": document["engine"]["reference_ms"], "turn_ms": document["turn"]["deadline_ms"]}


def reference(binding: dict) -> tuple[dict, dict]:
    data = unchanged(binding)
    absolute = limits()
    # CPU is explicitly observed; it is not the application's automatically selected device.
    result = run(binding, 'bialy', ['probe', '--model', data['model'], '--device', 'cpu'], timeout=120)
    unchanged(binding)
    report = {'device': 'cpu', 'absolute_limits': absolute, 'launch_identity': result['launch_identity'],
              'model_data': data, 'selected_application_device_qualified': False,
              'quality_baseline_qualified': False,
              'remaining_requirements': ['selected-device-reference', 'uncached-turn-latency',
                                         'independent-quality-baseline', 'qualified-macos-reference']}
    streams = {'bialy-stdout': result['stdout'], 'bialy-stderr': result['stderr']}
    if result['timed_out'] or result['output_limit'] or result['exit_code'] != 0:
        return dict(report, status='failed', failure={'code': 'probe-process-failed',
            **{key: result[key] for key in ('exit_code', 'timed_out', 'output_limit')}}), streams
    try:
        observed = json.loads(result['stdout'])
    except ValueError:
        observed = None
    value = observed.get('probe_ms') if isinstance(observed, dict) and set(observed) == {'probe_ms'} else None
    if type(value) not in (int, float) or not math.isfinite(value) or value < 0:
        return dict(report, status='failed', failure={'code': 'invalid-probe-response'}), streams
    return dict(report, status='observed', reference_ms=value,
                reference_within_existing_budget=value <= absolute['reference_ms'],
                probe_semantics='shipped-reference-task-untimed-warmup-median-three'), streams


def turn_latency(binding: dict) -> tuple[dict, dict]:
    from scripts.bialy.latency_workload import STATES
    from .performance import distribution
    data = unchanged(binding)
    release = Path(data['root']) / 'heads/release.json'
    head = Path(data['root']) / 'heads/turn-load.safetensors'
    if 'heads/release.json' not in data['files'] or 'heads/turn-load.safetensors' not in data['files']:
        from .records import CaseUnavailable
        raise CaseUnavailable('selected final payload has no sealed turn-load head release')
    if release.stat().st_size > 1 << 20:
        raise ValueError('installed Bialy head release exceeds its metadata bound')
    manifest = json.loads(release.read_text())
    encoding = manifest['preload'].get('encoding')
    if encoding not in ('joint', 'independent'):
        raise ValueError('installed Bialy release has an unsupported question encoding')
    options = manifest['preload']['options']
    if (not isinstance(options, dict) or len(options) < 16
            or any(not isinstance(key, str) or not isinstance(value, str) for key, value in options.items())):
        raise ValueError('installed Bialy release has no bounded reference roster')
    questions = {'tools': {'type': 'multi', 'independent': encoding == 'independent',
        'instructions': policy()['turn']['tools']['question'],
        'options': {key: options[key] for key in sorted(options)[:16]}}}
    calls = [{'method': 'hello'}]
    for index in range(21):
        calls.append({'method': 'decide', 'head': 'turn-load', 'questions': questions,
                      'state': {'host': 'coordinator', 'user': STATES[index % len(STATES)]}})
    try:
        result = exchange(binding, ['serve', '--model', data['model'], '--device', 'cpu',
                                   '--head', 'turn-load=' + str(head)], calls)
    except ExchangeFailure as error:
        unchanged(binding)
        return {'device': 'cpu', 'status': 'failed', 'failure': error.observations,
                'diagnostic': str(error), 'quality_baseline_qualified': False,
                'selected_application_device_qualified': False}, error.streams
    unchanged(binding)
    if len(result['responses']) != 22 or 'engine' not in result['responses'][0]:
        raise ValueError('installed Bialy did not identify the actual serving engine')
    for response in result['responses'][1:]:
        if not isinstance(response.get('answers'), dict) or set(response['answers']) != {'tools'}:
            raise ValueError('installed Bialy did not answer the fixed reference question')
        answer = response['answers']['tools']
        probabilities = answer.get('probabilities') if isinstance(answer, dict) else None
        if (not isinstance(probabilities, dict) or set(probabilities) != set(questions['tools']['options'])
                or answer.get('type') != 'multi'
                or any(type(value) not in (int, float) or not math.isfinite(value) or not 0 <= value <= 1
                       for value in [answer.get('confidence'), *probabilities.values()])):
            raise ValueError('installed Bialy returned incomplete or invalid reference probabilities')
    measured = distribution(result['roundtrip_ms'][2:])
    return {'device': 'cpu', 'engine': result['responses'][0]['engine'], 'samples_ms': result['roundtrip_ms'][2:],
            'warmup_excluded': True, 'single_inflight': True, 'measurements_ms': measured,
            'absolute_limit_ms': limits()['turn_ms'], 'within_existing_budget': measured['p95'] <= limits()['turn_ms'],
            'workload': 'fixed-six-states-sixteen-release-options', 'encoding': encoding, 'quality_baseline_qualified': False,
            'selected_application_device_qualified': False}, \
           {'bialy-turn-stdout': result['stdout'], 'bialy-turn-stderr': result['stderr']}
