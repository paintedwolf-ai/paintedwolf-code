"""Hosted lane catalog validation and matrix selection."""
from collections import Counter
from pathlib import Path
import re

from ci_policy.timings import row_key
from verification_plan import catalog, expand

ROOT = Path(__file__).resolve().parents[2]
PROFILES = {"fast", "check", "nightly", "release", "integration"}
GATE = "check"
FAST_GATE = "check-fast"
SUITES = {"all", "behavior", "race", "coverage", "performance", "fuzz", "e2e"}
SETUPS = {"verification", "shell", "harness"}
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
                        "platform", "paths", "arguments"}:
            raise ValueError(f"unknown CI lane fields: {name}")
        # A path-selected platform lane verifies one subsystem's sources; nightly and release always run it.
        if "paths" in lane and ("platform" not in lane or not lane["paths"]
                                or not all(isinstance(path, str) and path and not path.startswith("/")
                                           for path in lane["paths"])):
            raise ValueError(f"CI lane {name} paths must select a platform lane by repository prefixes")
        if "arguments" in lane and (len(lane["targets"]) != 1 or not lane["arguments"]
                                    or not all(isinstance(value, str) and value for value in lane["arguments"])):
            raise ValueError(f"CI lane {name} arguments belong to exactly one target")
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
        # Only a platform lane runs on its platform's other runners.
        if lane.get("runner", "macos-15") not in {"macos-15", "ubuntu-latest"} | PLATFORMS.get(lane.get("platform"), set()):
            raise ValueError(f"unsupported CI runner: {name}")
        if "workers" in lane and (type(lane["workers"]) is not int or not 1 <= lane["workers"] <= 8):
            raise ValueError(f"CI lane {name} workers must be an integer from 1 through 8")
        if "shards" in lane and (type(lane["shards"]) is not int or not 2 <= lane["shards"] <= 8):
            raise ValueError(f"CI lane {name} shards must be an integer from 2 through 8")
        if "platform" in lane and (lane["platform"] not in PLATFORMS
                                   or lane.get("runner", "ubuntu-latest") not in PLATFORMS[lane["platform"]]
                                   or set(lane["profiles"]) & {"fast", GATE}):
            raise ValueError(f"CI lane {name} is a platform lane on its platform's runners, outside the fast and {GATE} tiers")
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
    expected = Counter(stage["name"] for stage in expand([GATE]) if stage["name"] != "build:cross")
    actual = Counter(stage["name"] for lane in values.values() if "integration" in lane["profiles"] and "platform" not in lane
                     for stage in expand(lane["targets"]))
    if actual != expected:
        raise ValueError(f"integration partition differs from check minus cross compilation: {expected - actual}, {actual - expected}")
    platform = {stage["name"] for lane in values.values() if "platform" in lane for stage in expand(lane["targets"])}
    repeated = platform & {stage["name"] for stage in expand([GATE])}
    if repeated:
        raise ValueError(f"platform lanes repeat {GATE} stages: {sorted(repeated)}")
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


def matrix(profile, suite="all", scope=None, platform=False, timings=None):
    """A profile's lanes; `platform` keeps only its platform lanes, for a run whose other lanes another gate covers."""
    if profile not in PROFILES or suite not in SUITES or profile != "nightly" and suite != "all":
        raise ValueError(f"unsupported CI selection: {profile}/{suite}")
    values = lanes()
    selected = {name for name, lane in values.items() if "platform" in lane} if platform else set(values)
    # A broad change runs the whole check gate and every platform lane of the merge queue.
    broad = profile == "integration" and scope is not None and scope["full"]
    # A path-selected lane runs when its paths change, or whenever the changed paths are unknown.
    by_path = {name for name in selected if "paths" in values[name]}
    if profile == "integration" and scope is not None and scope.get("paths"):
        by_path = {name for name in by_path
                   if any(path.startswith(tuple(values[name]["paths"])) for path in scope["paths"])}
    if profile == "integration" and scope is not None and not broad:
        from ci_policy.impact import selected_lanes
        selected &= selected_lanes(scope, values)
    selected = {name for name in selected if "paths" not in values[name]} | by_path
    result = []
    for name, lane in values.items():
        if name not in selected:
            continue
        tier = GATE if broad and "platform" not in lane else profile
        if tier not in lane["profiles"] or suite != "all" and lane["suite"] not in {"all", suite}:
            continue
        count = lane.get("shards", 1)
        for index in range(1, count + 1):
            result.append({"lane": name, "shard": f"{index}/{count}" if count > 1 else "",
                           "minutes": lane["minutes"], "job_minutes": lane["minutes"] + 30,
                           "setup": lane["setup"],
                           "notices": "licenses:notices" in lane["targets"],
                           "containers": CONTAINER_TARGET in lane["targets"],
                           "browser": BROWSER_TARGET in lane["targets"]
                           and "browser" in lane.get("arguments", ["browser"]),
                           "nix": any(target.startswith("nix:") for target in lane["targets"])
                           or "nix" in lane.get("arguments", []),
                           "analysis": analysis_set(lane["targets"]),
                           "runner": lane.get("runner", "ubuntu-latest")})
    if not result:
        raise ValueError("CI selection contains no verification")
    # A capped matrix starts jobs in order: the cheap lanes that fail most often come first, so a
    # failing group stops early, then the longest lanes, so they don't finish last. A lane main's
    # qualification has not measured is planned at its budget, so it starts early.
    measured = (timings or {}).get("lanes", {})

    def expected(row):
        return measured.get(row_key(row["lane"], row["shard"]), row["minutes"] * 60)

    return {"include": sorted(result, key=lambda row: (not values[row["lane"]].get("first"), -expected(row)))}


def invocation(targets):
    """Expand private groups, which Task refuses to run by name, into their members."""
    plan = catalog()
    private = set(plan.get("private", []))
    return [member for target in targets
            for member in (plan["groups"][target] if target in private else [target])]
