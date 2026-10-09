"""Structured outcomes and stable failure signatures; diagnostic prose is never authority."""

import hashlib
import json
from pathlib import Path


def oom_events(path=Path('/sys/fs/cgroup/memory.events')):
    try:
        return int(dict(line.split() for line in path.read_text().splitlines()).get('oom_kill', 0))
    except (OSError, ValueError):
        return None


def failure_signature(failure):
    identity = [failure.get('stage', ''), failure.get('subject', ''), sorted(failure.get('tests', []))]
    return hashlib.sha256(json.dumps(identity, separators=(',', ':')).encode()).hexdigest()[:20]


def classify(record, failures):
    # An assertion or package resource budget is a source failure even if the runner also ran out of memory.
    if any(f.get('tests') or f.get('resource_limit') or f.get('exit_code') == 1 for f in failures):
        return 'test_failure'
    if record.get('status') == 'passed':
        return 'passed'
    before, after = record.get('oom_before'), record.get('oom_after')
    if type(before) is int and type(after) is int and after > before:
        return 'runner_oom'
    return 'check_failure' if failures else 'unknown'


def retryable(records, attempt):
    failed = [r for r in records if r.get('classification') != 'passed']
    return attempt == 1 and bool(failed) and all(r.get('classification') == 'runner_oom' for r in failed)
