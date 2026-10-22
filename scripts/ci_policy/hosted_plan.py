"""Hosted lane catalog validation and matrix selection."""
from collections import Counter
from pathlib import Path
import re

from ci_policy.hosted_schedule import order, validate_sources
from ci_policy.timings import row_key
from verification_plan import catalog, expand

ROOT = Path(__file__).resolve().parents[2]
PROFILES = {"fast", "check", "nightly", "release", "integration"}
GATE = "check"
FAST_GATE = "check-fast"
SUITES = {"all", "behavior", "race", "coverage", "performance", "fuzz", "e2e"}
SETUPS = {"verification", "shell", "harness", "frontend"}
# A platform lane runs a check that only its platform can run, so the cross-platform check gate,
# which every development host runs, leaves it out; the merge queue runs it beside that gate.
# Each platform names the runners it may use: architecture-specific code needs each architecture.
PLATFORMS = {"linux": {"ubuntu-latest", "ubuntu-24.04-arm"}}
# Runs its probe in container images that an offline lane must find already provisioned.
CONTAINER_TARGET = "den:webkit:atspi"
# Runs the pinned browser, which an offline lane must find already provisioned.
BROWSER_TARGET = "test:confine:linux"


def lanes():
    from ci_policy.quarantine import entries
    entries()
    values = catalog()["ci"]
    taskfile = (ROOT / "Taskfile.yml").read_text()
    targets = set(re.findall(r"^  ([\w:-]+):$", taskfile, re.M))
    for name, lane in values.items():
        if not re.fullmatch(r"[a-z][a-z0-9-]*", name):
            raise ValueError(f"invalid CI lane: {name}")
        if set(lane) - {"targets", "minutes", "profiles", "suite", "setup", "runner", "workers", "shards", "first",
