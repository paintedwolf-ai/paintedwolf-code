"""Advisory timing summaries from completed test-runner events."""

import math


def seconds(value, scale=1):
    if type(value) not in (int, float):
        return None
    try:
        duration = value / scale
    except OverflowError:
        return None
    return duration if math.isfinite(duration) and duration >= 0 else None


def write_slowest(out, label, timings):
    slow = sorted(((duration, name) for name, duration in timings if duration is not None and duration >= 1),
                  key=lambda item: (-item[0], item[1]))[:5]
    if not slow:
        return
    out.write(f"    slowest {label} (elapsed, not additive):\n")
    for duration, name in slow:
        out.write(f"      {duration:.2f}s  {name}\n")
