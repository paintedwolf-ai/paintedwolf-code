"""Hosted lane catalog validation and matrix selection."""
from collections import Counter
from pathlib import Path
import re

from verification_plan import catalog, expand

ROOT = Path(__file__).resolve().parents[2]
PROFILES = {"fast", "check", "nightly", "release", "integration"}
GATE = "check"
FAST_GATE = "check-fast"
SUITES = {"all", "behavior", "race", "coverage", "performance", "fuzz", "e2e"}
SETUPS = {"verification", "shell", "harness"}


def lanes():
    from ci_policy.quarantine import entries
    entries()
    values = catalog()["ci"]
    taskfile = (ROOT / "Taskfile.yml").read_text()
    targets = set(re.findall(r"^  ([\w:-]+):$", taskfile, re.M))
    for name, lane in values.items():
        if not re.fullmatch(r"[a-z][a-z0-9-]*", name):
            raise ValueError(f"invalid CI lane: {name}")
        if set(lane) - {"targets", "minutes", "profiles", "suite", "setup", "runner", "workers", "shards", "first"}:
            raise ValueError(f"unknown CI lane fields: {name}")
        if lane.get("first", True) is not True:
            raise ValueError(f"CI lane {name} marks itself first or leaves the field out")
        if not lane["targets"] or not set(lane["targets"]).issubset(targets):
            raise ValueError(f"CI lane {name} must name existing task targets")
        if type(lane["minutes"]) is not int or not 1 <= lane["minutes"] <= 300:
            raise ValueError(f"CI lane {name} must leave time for setup and evidence upload")
        if not lane["profiles"] or not set(lane["profiles"]).issubset(PROFILES):
            raise ValueError(f"invalid CI profiles: {name}")
        if lane["suite"] not in SUITES or lane.get("setup") not in SETUPS:
            raise ValueError(f"invalid CI setup or suite: {name}")
        if lane.get("runner", "macos-15") not in {"macos-15", "ubuntu-latest"}:
            raise ValueError(f"unsupported CI runner: {name}")
        if "workers" in lane and (type(lane["workers"]) is not int or not 1 <= lane["workers"] <= 8):
            raise ValueError(f"CI lane {name} workers must be an integer from 1 through 8")
        if "shards" in lane and (type(lane["shards"]) is not int or not 2 <= lane["shards"] <= 8):
            raise ValueError(f"CI lane {name} shards must be an integer from 2 through 8")
    expected = Counter(stage["name"] for stage in expand([GATE]))
    actual = Counter(stage["name"] for lane in values.values() if GATE in lane["profiles"]
                     for stage in expand(lane["targets"]))
    if actual != expected:
        raise ValueError(f"CI {GATE} profile differs from the {GATE} gate: "
                         f"missing={expected - actual}, extra={actual - expected}")
    fast = Counter(stage["name"] for lane in values.values() if "fast" in lane["profiles"]
                   for stage in expand(lane["targets"]))
    extra = set(fast) - {stage["name"] for stage in expand([FAST_GATE])}
    if not fast or extra or max(fast.values()) > 1:
        raise ValueError(f"CI fast tier must run stages of {FAST_GATE} once each: extra={extra}, counts={dict(fast)}")
    expected = Counter(stage["name"] for stage in expand(["check"]) if stage["name"] != "build:cross")
    actual = Counter(stage["name"] for lane in values.values() if "integration" in lane["profiles"]
                     for stage in expand(lane["targets"]))
    if actual != expected:
        raise ValueError(f"integration partition differs from check minus cross compilation: {expected - actual}, {actual - expected}")
    return values


def max_parallel(profile):
    """Jobs a profile's matrix runs at once; the catalog's capacity table bounds every hosted class."""
    caps = catalog()["capacity"]["max_parallel"]
    if set(caps) != PROFILES or not all(type(cap) is int and cap >= 1 for cap in caps.values()):
        raise ValueError("every CI profile declares a positive maximum of parallel jobs")
    if profile not in PROFILES:
        raise ValueError(f"unsupported CI profile: {profile}")
    return caps[profile]


def analysis_set(targets):
    lint = bool(set(targets) & {"lint:fast", "lint:full"})
    vulnerabilities = bool(set(targets) & {"lint:vuln", "lint:vuln:fresh"})
    return "all" if lint and vulnerabilities else "lint" if lint else "vulnerabilities" if vulnerabilities else "none"


def matrix(profile, suite="all", scope=None):
    if profile not in PROFILES or suite not in SUITES or profile != "nightly" and suite != "all":
        raise ValueError(f"unsupported CI selection: {profile}/{suite}")
    if profile == "integration" and scope is not None and scope["full"]:
        profile = "check"
    result = []
    values = lanes()
    selected = set(values)
    if profile == "integration" and scope is not None:
        from ci_policy.impact import selected_lanes
        selected = selected_lanes(scope, values)
    for name, lane in values.items():
        if name not in selected:
            continue
        if profile not in lane["profiles"] or suite != "all" and lane["suite"] not in {"all", suite}:
            continue
        count = lane.get("shards", 1)
        for index in range(1, count + 1):
            result.append({"lane": name, "shard": f"{index}/{count}" if count > 1 else "",
                           "minutes": lane["minutes"], "job_minutes": lane["minutes"] + 30,
                           "setup": lane["setup"],
                           "notices": "licenses:notices" in lane["targets"],
                           "analysis": analysis_set(lane["targets"]),
                           "runner": lane.get("runner", "ubuntu-latest")})
    if not result:
        raise ValueError("CI selection contains no verification")
    # A capped matrix starts jobs in order: the cheap lanes that fail most often come first, so a
    # failing group stops early, then the longest lanes, so they don't finish last.
    return {"include": sorted(result, key=lambda row: (not values[row["lane"]].get("first"), -row["minutes"]))}


def invocation(targets):
    """Expand private groups, which Task refuses to run by name, into their members."""
    plan = catalog()
    private = set(plan.get("private", []))
    return [member for target in targets
            for member in (plan["groups"][target] if target in private else [target])]
