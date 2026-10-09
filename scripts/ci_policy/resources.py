"""Per-test-process resident-memory ceiling, independent of runner-wide OOM."""
import json
from pathlib import Path
import time

POLICY = Path(__file__).with_name('resources.json')
# Leaves room for stacks, cgo, and runtime metadata that GOMEMLIMIT does not govern.
SOFT_LIMIT_PERCENT = 80


def budget(package):
    policy = json.loads(POLICY.read_text())
    value = policy['packages'].get(package, policy['default'])
    if type(value.get('rss_mib')) is not int or value['rss_mib'] < 64:
        raise ValueError('package RSS budget must be an integer of at least 64 MiB')
    return value['rss_mib'] * 1024 * 1024


def runtime_environment(environment, limit):
    """Give the Go runtime a soft limit below the ceiling, so collector slack is not mistaken for growth."""
    if 'GOMEMLIMIT' in environment:
        return environment
    return {**environment, 'GOMEMLIMIT': str(limit * SOFT_LIMIT_PERCENT // 100)}


def resident_bytes(pid):
    try:
        fields = dict(line.split(':', 1) for line in Path(f'/proc/{pid}/status').read_text().splitlines() if ':' in line)
        return int(fields.get('VmRSS', '0 kB').split()[0]) * 1024
    except FileNotFoundError:
        return 0


class Monitor:
    def __init__(self, pid, limit):
        self.pid, self.limit, self.peak = pid, limit, 0
        self.next_sample = 0
        self.violation = None

    def sample(self):
        now = time.monotonic()
        if now < self.next_sample or self.violation:
            return self.violation
        self.next_sample = now + 0.5
        self.peak = max(self.peak, resident_bytes(self.pid))
        if self.peak > self.limit:
            self.violation = {'kind': 'package_rss', 'measured_bytes': self.peak, 'limit_bytes': self.limit}
        return self.violation
