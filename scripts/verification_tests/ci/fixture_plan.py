"""Small verification catalogs for the hosted plan's rules, so a test never names a live lane."""
import contextlib
import copy
import json
from pathlib import Path
import tempfile
from unittest.mock import patch

import verification_plan

LIVE = json.loads(verification_plan.CATALOG.read_text())


def lane(*targets, profiles=("fast", "check", "integration", "nightly"), **fields):
    selected = {"changes": ["always"]} if set(profiles) & {"fast", "integration"} else {}
    return {"targets": list(targets), "minutes": 10, "profiles": list(profiles), "suite": "all",
            "setup": "verification", **selected, **fields}


def lanes(**extra):
    """The smallest lane set every rule accepts: one gate lane and the nightly stress partition."""
    return {"tests": lane("test:short"), "stress": lane("test:stress", profiles=("nightly",)), **extra}


@contextlib.contextmanager
def plan(ci=None, provisions=None, check=("test:short",), fast=("test:short",), stress=("./internal/db",), mutate=None):
    """Use a catalog built from the live declarations with only these lanes, gates and provisions;
    `mutate` edits the catalog before it is written."""
    data = copy.deepcopy(LIVE)
    data["ci"] = lanes() if ci is None else ci
    data["provisions"] = provisions or {}
    data["groups"]["check"], data["groups"]["check-fast"] = list(check), list(fast)
    data["go"]["test:stress"]["packages"] = list(stress)
    if mutate:
        mutate(data)
    with tempfile.TemporaryDirectory() as directory:
        path = Path(directory) / "verification-plan.json"
        path.write_text(json.dumps(data))
        with patch.object(verification_plan, "CATALOG", path):
            yield data


def live(phase=None, runs=None, area=None):
    """The name of the live lane with `phase`, running target `runs`, or selected by change `area` alone,
    so a test of live behaviour never spells a lane name."""
    from ci_policy.hosted_plan import lanes
    found = [name for name, spec in lanes().items()
             if (phase is None or spec.get("phases") == phase)
             and (runs is None or runs in spec["targets"])
             and (area is None or spec.get("changes") == [area])]
    if len(found) != 1:
        raise AssertionError(f"expected one live lane for phase={phase} runs={runs} area={area}: {found}")
    return found[0]


def live_lanes(*areas):
    """Live lanes a change in any of `areas` selects."""
    from ci_policy.hosted_plan import lanes, lanes_of
    return lanes_of(lanes(), areas)
