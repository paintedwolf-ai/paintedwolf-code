"""Observe captured Linux process lifetimes without inventing missing resources."""
from __future__ import annotations

import os
from pathlib import Path
import time

from .app import process_identity
from .records import CaseUnavailable


def resources(identity: dict) -> dict:
    pid = identity['pid']
    if process_identity(pid) != identity:
        raise ValueError('resource observation lost its captured engine identity')
    raw_stat = Path(f'/proc/{pid}/stat').read_text().rpartition(') ')[2].split()
    status = Path(f'/proc/{pid}/status').read_text()
    if process_identity(pid) != identity or len(raw_stat) < 22:
        raise ValueError('resource observation changed engine identity')
    fields = {}
    for line in status.splitlines():
        key, separator, value = line.partition(':')
        if separator and key in {'VmRSS', 'voluntary_ctxt_switches', 'nonvoluntary_ctxt_switches'}:
            if key in fields:
                raise ValueError('resource observation has duplicate fields')
            fields[key] = value.strip()
    rss = fields.get('VmRSS', '').split()
    if len(rss) != 2 or rss[1] != 'kB' or not rss[0].isdecimal() or int(rss[0]) <= 0:
        raise ValueError('engine RSS observation is missing or invalid')
    switches = [fields.get(key, '') for key in ('voluntary_ctxt_switches', 'nonvoluntary_ctxt_switches')]
    if any(not value.isdecimal() for value in switches):
        raise ValueError('engine context-switch observation is missing or invalid')
    if any(not raw_stat[index].isdecimal() for index in (11, 12)):
        raise ValueError('engine CPU observation is invalid')
    return {'at_monotonic': time.monotonic(), 'rss_bytes': int(rss[0]) * 1024,
            'cpu_ticks': int(raw_stat[11]) + int(raw_stat[12]),
            'context_switches': sum(map(int, switches)), 'identity': identity}


def members(root: dict) -> list[dict]:
    """Select only descendants of the captured desktop lifetime."""
    if process_identity(root['pid']) != root:
        raise ValueError('desktop resource root changed its captured identity')
    rows = {}
    for path in Path('/proc').iterdir():
        if not path.name.isdecimal():
            continue
        try:
            row = process_identity(int(path.name))
        except (FileNotFoundError, ProcessLookupError):
            continue
        if row['pid'] != int(path.name) or row['start_time_ticks'] <= 0:
            raise ValueError('process discovery returned an invalid lifetime')
        rows[row['pid']] = row
        if len(rows) > 65536:
            raise CaseUnavailable('desktop process discovery exceeds its bound')
    selected = {root['pid']: root}
    while True:
        additional = {pid: row for pid, row in rows.items() if pid not in selected
                      and row['parent_pid'] in selected
                      and row['start_time_ticks'] >= selected[row['parent_pid']]['start_time_ticks']}
        if not additional:
            break
        selected.update(additional)
        if len(selected) > 256:
            raise CaseUnavailable('owned desktop process tree exceeds its capture bound')
    if process_identity(root['pid']) != root:
        raise ValueError('desktop resource root changed during discovery')
    return [selected[pid] for pid in sorted(selected)]


class TreeSampler:
    """Keep CPU lifetime counters when owned descendants enter or leave."""

    def __init__(self, desktop: dict, engine: dict):
        self.desktop, self.engine = desktop, engine
        self.initial = True
        self.counters = {}

    def snapshot(self) -> dict:
        for _ in range(3):
            selected = members(self.desktop)
            if self.engine not in selected:
                raise ValueError('captured engine is outside the owned desktop tree')
            try:
                observations = [resources(row) for row in selected]
            except (FileNotFoundError, ProcessLookupError):
                continue
            if selected == members(self.desktop):
                break
        else:
            raise CaseUnavailable('desktop process churn prevented a complete bounded snapshot')
        for row in observations:
            identity = row['identity']
            key = identity['pid'], identity['start_time_ticks']
            previous = self.counters.get(key)
            if previous is not None and row['cpu_ticks'] < previous[1]:
                raise ValueError('owned process CPU counter regressed')
            baseline = previous[0] if previous is not None else (row['cpu_ticks'] if self.initial else 0)
            self.counters[key] = baseline, row['cpu_ticks']
        if len(self.counters) > 4096:
            raise CaseUnavailable('owned process lifetime history exceeds its capture bound')
        self.initial = False
        return {'at_monotonic': time.monotonic(), 'process_count': len(observations),
                'rss_sum_bytes': sum(row['rss_bytes'] for row in observations),
                'observed_cpu_ticks': sum(current - baseline for baseline, current in self.counters.values()),
                'processes': observations}


def tree_summary(samples: list[dict]) -> dict:
    if len(samples) < 2:
        raise CaseUnavailable('desktop resource observation needs multiple complete snapshots')
    elapsed = samples[-1]['at_monotonic'] - samples[0]['at_monotonic']
    if elapsed <= 0 or any(after['observed_cpu_ticks'] < before['observed_cpu_ticks']
                           for before, after in zip(samples, samples[1:])):
        raise ValueError('desktop sample clock or cumulative CPU counter regressed')
    return {'scope': 'captured-installed-desktop-and-observed-descendants',
            'samples': samples, 'elapsed_seconds': elapsed,
            'sampled_peak_rss_sum_bytes': max(row['rss_sum_bytes'] for row in samples),
            'observed_cpu_percent_lower_bound': 100 * (samples[-1]['observed_cpu_ticks'] - samples[0]['observed_cpu_ticks'])
                                                / os.sysconf('SC_CLK_TCK') / elapsed,
            'rss_semantics': 'sum-of-resident-pages-including-shared-pages-per-process',
            'cpu_semantics': 'sampled-lifetime-deltas-exclude-unobserved-short-lived-process-work',
            'wakeups_measured': False, 'absolute_limits': None, 'baseline_qualified': False}

