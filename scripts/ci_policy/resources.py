"""Per-test-process resident-memory ceiling, independent of runner-wide OOM."""
import json
from pathlib import Path
import sys
import time

POLICY = Path(__file__).with_name('resources.json')
# Leaves room for stacks, cgo, and runtime metadata that GOMEMLIMIT does not govern.
SOFT_LIMIT_PERCENT = 80


def budget(package, race=False):
    """The package's ceiling in bytes; a race build gets the declared multiple of it, since the
    detector's shadow memory and instrumented heap are overhead, not retained test state."""
    policy = json.loads(POLICY.read_text())
    value = policy['packages'].get(package, policy['default'])
    if type(value.get('rss_mib')) is not int or value['rss_mib'] < 64:
        raise ValueError('package RSS budget must be an integer of at least 64 MiB')
    scale = policy['race_scale'] if race else 1
    if type(scale) not in (int, float) or not 1 <= scale <= 4:
        raise ValueError('race_scale must be a number between 1 and 4')
    return int(value['rss_mib'] * 1024 * 1024 * scale)


def runtime_environment(environment, limit):
    """Give the Go runtime a soft limit below the ceiling, so collector slack is not mistaken for growth."""
    if 'GOMEMLIMIT' in environment:
        return environment
    return {**environment, 'GOMEMLIMIT': str(limit * SOFT_LIMIT_PERCENT // 100)}


def proc_status_memory(pid):
    """Read (VmRSS, VmHWM) in bytes from /proc/<pid>/status; missing fields are None."""
    try:
        fields = dict(line.split(':', 1) for line in Path(f'/proc/{pid}/status').read_text().splitlines() if ':' in line)
    except OSError:
        return None, None
    values = []
    for key in ('VmRSS', 'VmHWM'):
        parts = fields.get(key, '').split()
        values.append(int(parts[0]) * 1024 if parts and parts[0].isdigit() else None)
    return tuple(values)


def rusage_maxrss_bytes(rusage):
    """Lifetime peak RSS in bytes from wait4 rusage, or None when unavailable."""
    raw = getattr(rusage, 'ru_maxrss', 0) if rusage is not None else 0
    if type(raw) is not int or raw <= 0:
        return None
    # Linux reports ru_maxrss in KiB; macOS reports bytes.
    return raw if sys.platform == 'darwin' else raw * 1024


class Monitor:
    def __init__(self, pid, limit):
        self.pid, self.limit = pid, limit
        self.peak = None
        self.hwm = None
        self.next_sample = 0
        self.violation = None

    def sample(self):
        now = time.monotonic()
        if now < self.next_sample or self.violation:
            return self.violation
        self.next_sample = now + 0.5
        rss, hwm = proc_status_memory(self.pid)
        if rss is not None:
            self.peak = rss if self.peak is None else max(self.peak, rss)
        if hwm is not None:
            self.hwm = hwm if self.hwm is None else max(self.hwm, hwm)
        if self.peak is not None and self.peak > self.limit:
            self.violation = {'kind': 'package_rss', 'measured_bytes': self.peak, 'limit_bytes': self.limit}
        return self.violation

    def finalize(self, rusage=None):
        """Return the package's peak RSS observation after the process is reaped."""
        peak = rusage_maxrss_bytes(rusage)
        if peak is not None:
            source, lower_bound = 'wait4_ru_maxrss', False
        elif self.hwm is not None:
            peak, source, lower_bound = self.hwm, 'proc_vmhwm', True
        elif self.peak is not None:
            peak, source, lower_bound = self.peak, 'proc_vmrss', True
        else:
            return {'peak_bytes': None, 'limit_bytes': self.limit, 'ratio': None,
                    'source': 'unavailable', 'lower_bound': False}
        return {'peak_bytes': peak, 'limit_bytes': self.limit, 'ratio': round(peak / self.limit, 4),
                'source': source, 'lower_bound': lower_bound}
