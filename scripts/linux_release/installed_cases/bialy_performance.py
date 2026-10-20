"""Observe the shipped Bialy reference task without substituting a quality baseline."""
from __future__ import annotations

import json
import math
from pathlib import Path


from .bialy_data import unchanged
from .process import run

POLICY = Path(__file__).resolve().parents[3] / 'lycaon/config/packs/painted-wolf/platform/host/decisions.yaml'


def limits() -> dict:
    try:
        import yaml
    except ImportError as error:
        from .records import CaseUnavailable
        raise CaseUnavailable('installed Bialy policy observation requires the YAML catalog loader') from error
    policy = yaml.safe_load(POLICY.read_text())
    values = {'reference_ms': policy['engine']['reference_ms'], 'turn_ms': policy['turn']['deadline_ms']}
    if any(type(value) is not int or value <= 0 for value in values.values()):
        raise ValueError('existing Bialy performance policy has invalid absolute limits')
    return values


def reference(binding: dict) -> tuple[dict, dict]:
    data = unchanged(binding)
    absolute = limits()
    # CPU is explicitly observed; it is not the application's automatically selected device.
    result = run(binding, 'bialy', ['probe', '--model', data['model'], '--device', 'cpu'], timeout=120)
    unchanged(binding)
    if result['timed_out'] or result['output_limit'] or result['exit_code'] != 0:
        raise ValueError('selected installed Bialy reference probe did not complete')
    observed = json.loads(result['stdout'])
    value = observed.get('probe_ms') if isinstance(observed, dict) and set(observed) == {'probe_ms'} else None
    if type(value) not in (int, float) or not math.isfinite(value) or value < 0:
        raise ValueError('installed Bialy reference probe returned an invalid measurement')
    return {'device': 'cpu', 'reference_ms': value, 'absolute_limits': absolute,
            'reference_within_existing_budget': value <= absolute['reference_ms'],
            'probe_semantics': 'shipped-reference-task-untimed-warmup-median-three',
            'launch_identity': result['launch_identity'], 'model_data': data,
            'selected_application_device_qualified': False, 'quality_baseline_qualified': False,
            'remaining_requirements': ['selected-device-reference', 'uncached-turn-latency',
                                       'independent-quality-baseline', 'qualified-macos-reference']}, \
           {'bialy-stdout': result['stdout'], 'bialy-stderr': result['stderr']}
