"""Hosted shards: each job of a split lane verifies its share of the package selection."""

import json
import os
from pathlib import Path
import statistics


def measured_durations(environment=os.environ):
    """Package seconds the hosted plan measured; every shard of one run reads the same values."""
    path = environment.get("PW_GO_TIMINGS")
    return json.loads(Path(path).read_text()) if path else {}


def parse(shard):
    index, separator, count = shard.partition("/")
    if not (separator and index.isdigit() and count.isdigit() and 1 <= int(index) <= int(count)):
        raise ValueError(f"PW_GO_SHARD must be k/N with 1 <= k <= N, not {shard!r}")
    return int(index), int(count)


def shard_packages(packages, shard, durations=None):
    """Shard k/N of the selection. Packages go longest first to the least loaded shard, so shards finish
    together; without durations that deals the sorted selection in turn. An unmeasured package counts as
    the median measured one."""
    if not shard:
        return packages
    index, count = parse(shard)
    packages = sorted(packages)
    # An empty shard must not pass as an empty run, so a selection smaller than the split repeats packages.
    if len(packages) < count:
        return [packages[(index - 1) % len(packages)]]
    durations = {package: seconds for package, seconds in (durations or {}).items()
                 if type(seconds) in (int, float) and seconds >= 0}
    default = statistics.median(durations.values()) if durations else 0
    weight = {package: durations.get(package, default) for package in packages}
    loads = [0.0] * count
    shares = [[] for _ in range(count)]
    for package in sorted(packages, key=lambda name: (-weight[name], name)):
        # Count breaks load ties, so packages without measurable time still spread evenly.
        target = min(range(count), key=lambda k: (loads[k], len(shares[k]), k))
        shares[target].append(package)
        loads[target] += weight[package]
    return sorted(shares[index - 1])
