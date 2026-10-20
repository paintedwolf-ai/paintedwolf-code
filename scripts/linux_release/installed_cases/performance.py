"""Measure the selected installed app against existing absolute service budgets."""
from __future__ import annotations

import json
import math
import os
from pathlib import Path
import platform
import statistics
import tempfile
import time

from .app import InstalledApp
from .bialy_performance import reference as bialy_reference, turn_latency as bialy_turn_latency
from .provider import ProviderFixture
from .performance_resources import TreeSampler, resources, tree_summary
from .records import CaseUnavailable, retain
from .security_boundary import successful_invocation
from .workflow import ToolTurn, accepted, wait_settled

SAMPLES = 20
BUDGET_PATH = Path(__file__).resolve().parents[3] / 'lycaon/cmd/lycaon-perf/budgets.json'


def distribution(values: list[float]) -> dict:
    if not values or any(type(value) not in (int, float) or not math.isfinite(value) or value < 0 for value in values):
        raise ValueError('performance samples must be finite nonnegative measurements')
    ordered = sorted(values)
    result = {'samples': len(values), 'p50': statistics.median(ordered), 'maximum': ordered[-1]}
    for name, quantile, minimum in [('p95', 0.95, 20), ('p99', 0.99, 100)]:
        if len(values) >= minimum:
            result[name] = ordered[math.ceil(len(values) * quantile) - 1]
    return result


def limits() -> dict:
    document = json.loads(BUDGET_PATH.read_text())
    latency, resources = document['latency_p95_ms'], document['resources']
    names = ('startup.ready', 'restart.ready', 'project.create', 'session.prepare', 'prompt.accept', 'prompt.settle')
    selected = {name: latency[name] for name in names}
    selected['peak_rss_bytes'] = resources['peak_rss_bytes']
    selected['rss_growth_bytes'] = resources['rss_growth_bytes']
    if any(type(value) not in (int, float) or not math.isfinite(value) or value <= 0 for value in selected.values()):
        raise ValueError('existing absolute performance budget is invalid')
    return selected


def enforce(samples: dict[str, list[float]], absolute: dict) -> list[dict]:
    failures = []
    for name, values in samples.items():
        measured = distribution(values)
        if name not in absolute or len(values) < SAMPLES or 'p95' not in measured:
            raise ValueError('performance acceptance requires its existing absolute limit and twenty samples')
        if measured['p95'] > absolute[name]:
            failures.append({'metric': name, 'p95': measured['p95'], 'absolute_limit': absolute[name]})
    return failures


def start_samples(binding: dict) -> tuple[dict, dict]:
    from .desktop_ui import interactive_window
    absolute = limits()
    timings, windows, engine_ids = [], [], []
    streams = {'app-stdout': bytearray(), 'app-stderr': bytearray()}
    for _ in range(SAMPLES):
        app = InstalledApp(binding, timeout=60)
        started = time.monotonic()
        try:
            with app:
                window = interactive_window(app)
                timings.append((time.monotonic() - started) * 1000)
                windows.append(window)
                engine_ids.append(app.observations['engine_identity'])
        finally:
            app.stop()
            if not app.observations.get('capture_complete') or app.observations.get('output_limit'):
                raise ValueError('performance launch lost its bounded process capture')
            for name, raw in app.streams().items():
                if len(streams[name]) + len(raw) > 8 << 20:
                    raise ValueError('performance launch evidence exceeds its aggregate bound')
                streams[name].extend(raw)
    failures = enforce({'restart.ready': timings}, absolute)
    return {'measurements_ms': {'restart.ready': distribution(timings)}, 'samples_ms': timings,
            'absolute_limits': absolute, 'failures': failures, 'windows': windows,
            'engine_identities': engine_ids, 'cache_state': 'uncontrolled',
            'remaining_requirements': ['boot-bound-cold-launch', 'qualified-macos-reference', 'GUI-responsiveness']}, \
           {key: bytes(value) for key, value in streams.items()}


def workflow_samples(binding: dict) -> tuple[dict, dict]:
    app = InstalledApp(binding)
    samples = {'session.prepare': [], 'prompt.settle': []}
    witnesses = []
    try:
        bialy, bialy_streams = bialy_reference(binding)
        if bialy.get("status") != "failed":
            turns, turn_streams = bialy_turn_latency(binding)
            bialy["turn_latency"] = turns
            bialy_streams.update(turn_streams)
    except CaseUnavailable as error:
        bialy, bialy_streams = {"status": "unverified", "reason": str(error)}, {}
    with tempfile.TemporaryDirectory(prefix='paintedwolf-installed-perf-') as directory:
        project = Path(directory)
        (project / 'fixture.txt').write_text('Installed performance fixture.\n')
        try:
            with app:
                for index in range(SAMPLES):
                    turn_project = project / str(index)
                    turn_project.mkdir()
                    (turn_project / 'fixture.txt').write_text('Installed performance fixture.\n')
                    with ProviderFixture([{'name': 'command', 'arguments': {'command': 'printf installed-performance-fixture'}}]) as provider:
                        started = time.monotonic()
                        with ToolTurn(app, provider, turn_project) as turn:
                            samples['session.prepare'].append((time.monotonic() - started) * 1000)
                            started = time.monotonic()
                            turn.submit()
                            results = turn.wait(timeout=60)
                            wait_settled(app, turn.session_id)
                            samples['prompt.settle'].append((time.monotonic() - started) * 1000)
                            if not successful_invocation(results):
                                raise ValueError('performance fixture command did not actually complete')
                            witnesses.append(turn.observations)
        finally:
            app.stop()
    if not app.observations.get('capture_complete') or app.observations.get('output_limit'):
        raise ValueError('performance workflow lost its bounded process capture')
    return {'measurements_ms': {key: distribution(value) for key, value in samples.items()},
            'samples_ms': samples, 'failures': enforce(samples, limits()) +
                ([{'metric': 'Bialy-CPU-probe', 'failure': bialy['failure']}]
                 if bialy.get('status') == 'failed' else []) +
                ([{'metric': 'Bialy-CPU-exchange', 'failure': bialy['turn_latency']['failure']}]
                 if bialy.get('turn_latency', {}).get('status') == 'failed' else []), 'absolute_limits': limits(),
            'workload': 'deterministic-local-provider-command', 'witnesses': witnesses, 'bialy_reference': bialy,
            'remaining_requirements': ['Bialy-load-reference-and-turn-latency', 'Bialy-quality-parity',
                                       'parallel-index-search-scan', 'qualified-macos-reference']}, {**app.streams(), **bialy_streams}


def resource_samples(binding: dict) -> tuple[dict, dict]:
    app = InstalledApp(binding)
    samples, desktop_samples, unavailable = [], [], []
    try:
        with app:
            identity = app.observations['engine_identity']
            desktop = TreeSampler(app.observations['launch_identity'], identity)
            for index in range(31):
                samples.append(resources(identity))
                try:
                    desktop_samples.append(desktop.snapshot())
                except CaseUnavailable as error:
                    unavailable.append({'sample': index, 'reason': str(error)})
                if index < 30:
                    time.sleep(1)
    finally:
        app.stop()
    if not app.observations.get('capture_complete') or app.observations.get('output_limit'):
        raise ValueError('performance resource probe lost its bounded process capture')
    if any(after[key] < before[key] for before, after in zip(samples, samples[1:])
           for key in ('at_monotonic', 'cpu_ticks', 'context_switches')):
        raise ValueError('engine resource counter regressed within its captured lifetime')
    peak = max(row['rss_bytes'] for row in samples)
    growth = statistics.median(row['rss_bytes'] for row in samples[-10:]) - statistics.median(row['rss_bytes'] for row in samples[:10])
    absolute = limits()
    failures = [{'metric': key, 'measured': value, 'absolute_limit': absolute[key]}
                for key, value in [('peak_rss_bytes', peak), ('rss_growth_bytes', max(0, growth))]
                if value > absolute[key]]
    elapsed = samples[-1]['at_monotonic'] - samples[0]['at_monotonic']
    if elapsed <= 0:
        raise ValueError('performance resource sample interval is empty')
    desktop_report = tree_summary(desktop_samples) if len(desktop_samples) >= 2 else None
    return {'samples': samples, 'scope': 'captured-installed-engine', 'peak_rss_bytes': peak,
            'rss_growth_bytes': growth, 'elapsed_seconds': elapsed, 'failures': failures,
            'idle_cpu_percent': 100 * (samples[-1]['cpu_ticks'] - samples[0]['cpu_ticks']) / os.sysconf('SC_CLK_TCK') / elapsed,
            'main_thread_context_switches_per_second': (samples[-1]['context_switches'] - samples[0]['context_switches']) / elapsed,
            'absolute_limits': absolute, 'desktop_tree': desktop_report,
            'desktop_tree_incomplete_snapshots': unavailable,
            'remaining_requirements': ['declared-whole-desktop-resource-budgets',
                'sustained-mixed-load-soak', 'wakeups', 'declared-idle-CPU-budget', 'qualified-macos-reference']}, app.streams()


def execute(binding: dict, capability: str, case: str, root: Path) -> dict:
    if capability != 'performance' or binding.get('case_inputs', {}).get('performance', {}) != {}:
        raise ValueError('installed performance driver refuses arbitrary workload or limit overrides')
    native = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine())
    if platform.system() != 'Linux' or native != binding['artifact']['architecture']:
        raise CaseUnavailable('installed performance requires the selected native Linux architecture')
    drivers = {'cold-and-warm-start': start_samples, 'workflow-and-bialy-latency': workflow_samples,
               'memory-and-sustained-load': resource_samples}
    if case == 'architecture-specific-thresholds':
        observations, streams = {'absolute_limits': limits(), 'architecture': native,
            'baseline_qualified': False, 'regression_budget': None,
            'remaining_requirements': ['qualified-hardware-and-driver-baseline', 'qualified-macos-reference',
                                       'complete-installed-measurement-table']}, {}
    elif case in drivers:
        observations, streams = drivers[case](binding)
    else:
        raise ValueError('installed performance case is not declared')
    # These probes cover their named measurements, not the wider catalog case.
    status = 'failed' if observations.get('failures') else 'unverified'
    return retain(binding, capability, case, root, status, observations, streams)
