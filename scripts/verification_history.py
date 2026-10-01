"""Completed runtimes per operation name: the baseline for advisories, backfill, and stage priority."""

import math
import time

from verification_state import read_state, write_json

HISTORY_LIMIT = 32
HISTORY_NAMES = 128
MIN_SAMPLES = 3
# Backfill and priority plan against a slow run, not a typical one.
ESTIMATE_QUANTILE = 0.9


def history_path(queue):
    return queue.root / "durations.json"


def read_history(queue):
    return read_state(history_path(queue))


def samples(history, name):
    record = history.get(name)
    values = record.get("samples") if isinstance(record, dict) else None
    if not isinstance(values, list):
        return []
    return [value for value in values if type(value) in (int, float) and math.isfinite(value) and value >= 0]


def record_duration(queue, names, seconds):
    if not names or type(seconds) not in (int, float) or not 0 <= seconds < 86400:
        return
    key = ", ".join(names)
    with queue.locked():
        path = history_path(queue)
        history = read_state(path)
        history[key] = {"samples": [*samples(history, key), round(seconds, 3)][-HISTORY_LIMIT:],
                        "updated_at": time.time()}
        if len(history) > HISTORY_NAMES:
            ranked = sorted(history.items(), key=lambda item: item[1].get("updated_at", 0), reverse=True)
            history = dict(ranked[:HISTORY_NAMES])
        write_json(path, history)


def median(values):
    ordered = sorted(values)
    if not ordered:
        return None
    middle = len(ordered) // 2
    return ordered[middle] if len(ordered) % 2 else (ordered[middle - 1] + ordered[middle]) / 2


def typical(history, name):
    values = samples(history, name)
    return median(values) if len(values) >= MIN_SAMPLES else None


def estimate(history, name):
    """Nearest-rank upper quantile; a name without enough completed runs has no estimate."""
    values = sorted(samples(history, name))
    if len(values) < MIN_SAMPLES:
        return None
    return values[min(len(values) - 1, math.ceil(ESTIMATE_QUANTILE * len(values)) - 1)]
