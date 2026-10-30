"""Hosted lane catalog validation and matrix selection."""
from collections import Counter
from pathlib import Path
import re

from ci_policy.hosted_schedule import order, validate_sources, PRIORITIES
from ci_policy.timings import row_key
from ci_policy.native_admission import targets as native_targets
from verification_plan import catalog, expand

ROOT = Path(__file__).resolve().parents[2]
PROFILES = {"fast", "check", "nightly", "release", "integration"}
GATE = "check"
FAST_GATE = "check-fast"
SUITES = {"all", "behavior", "race", "coverage", "performance", "fuzz", "e2e"}
SETUPS = {"verification", "shell", "harness", "frontend", "analysis"}
# A platform lane runs a check that only its platform can run, so the cross-platform check gate,
# which every development host runs, leaves it out; the merge queue runs it beside that gate.
# Each platform names the runners it may use: architecture-specific code needs each architecture.
PLATFORMS = {"linux": {"ubuntu-24.04", "ubuntu-24.04-arm"}}
# Runs its probe in container images that an offline lane must find already provisioned.
CONTAINER_TARGET = "den:webkit:atspi"
# Runs the pinned browser, which an offline lane must find already provisioned.
BROWSER_TARGET = "test:confine:linux"
# Drives the native repository tools against committed distribution fixtures.
REPOSITORY_TOOLS_TARGET = "test:runner"
# Runs the reference userland images, which an offline lane must find already built and verified.
USERLANDS_TARGET = "test:userlands"
# The protected launch leg builds and runs a GTK desktop under a display.
LAUNCH_PACKAGE = "./internal/launchguard"
# Go runs that select their packages in their own scripts, outside the catalog's `go` recipes,
# and reach the protected launch leg.
LAUNCH_SCRIPT_TARGETS = {"test:confine:linux", "coverage:changes", "check:coverage"}


def lanes():
    from ci_policy.quarantine import entries
    entries()
    values = catalog()["ci"]
    taskfile = (ROOT / "Taskfile.yml").read_text()
    targets = set(re.findall(r"^  ([\w:-]+):$", taskfile, re.M))
    for name, lane in values.items():
        if not re.fullmatch(r"[a-z][a-z0-9-]*", name):
            raise ValueError(f"invalid CI lane: {name}")
        if set(lane) - {"targets", "minutes", "profiles", "suite", "setup", "runner", "workers", "shards", "priority",
                        "platform", "arguments", "preflight", "timing_source", "early_targets", "contract_first", "compile_targets", "runtime_engines"}:
            raise ValueError(f"unknown CI lane fields: {name}")
        if "arguments" in lane and (len(lane["targets"]) != 1 or not lane["arguments"]
                                    or not all(isinstance(value, str) and value for value in lane["arguments"])):
            raise ValueError(f"CI lane {name} arguments belong to exactly one target")
        if 'runtime_engines' in lane and (lane['runtime_engines'] is not False
                or lane['targets'] != ['den:test:rust', 'decide:test']):
            raise ValueError(f"CI lane {name} requires runtime engines for host checks")
        compilation = lane.get('compile_targets', {})
        if (name == 'ready' or 'compile_targets' in lane) and (name != 'ready' or not isinstance(compilation, dict)
                or set(compilation) != {'den:test:rust', 'decide:test'}
                or any(not isinstance(roots, list) or not roots
                       or any(not isinstance(root, str) or not root.endswith('/') or '..' in root for root in roots)
                       for roots in compilation.values())):
            raise ValueError(f"CI lane {name} has invalid native compilation targets")
        early = lane.get("early_targets", [])
        if (not isinstance(early, list) or len(set(early)) != len(early)
                or not set(early).issubset(lane["targets"])):
            raise ValueError(f"CI lane {name} early targets must partition its declared targets")
        if "contract_first" in lane and (lane["contract_first"] is not True or lane["targets"] != ["test:full"]):
            raise ValueError(f"CI lane {name} contract ordering requires the complete Go recipe")

        if lane.get("priority", "normal") not in PRIORITIES:
            raise ValueError(f"CI lane {name} has an invalid scheduling priority")
        if not lane["targets"] or not set(lane["targets"]).issubset(targets):
            raise ValueError(f"CI lane {name} must name existing task targets")
        if type(lane["minutes"]) is not int or not 1 <= lane["minutes"] <= 300:
            raise ValueError(f"CI lane {name} must leave time for setup and evidence upload")
        if not lane["profiles"] or not set(lane["profiles"]).issubset(PROFILES):
            raise ValueError(f"invalid CI profiles: {name}")
        if lane["suite"] not in SUITES or lane.get("setup") not in SETUPS:
            raise ValueError(f"invalid CI setup or suite: {name}")
        if lane["setup"] == "frontend" and not set(lane["targets"]).issubset({"den:typecheck", "den:lint", "den:test"}):
            raise ValueError(f"CI lane {name} requires toolchains outside frontend setup")
        if lane["setup"] == "analysis" and not set(lane["targets"]).issubset({
                "lint:fast", "lint:full", "lint:vuln", "lint:vuln:fresh", "comments:check", "test:fail-messages:check"}):
            raise ValueError(f"CI lane {name} requires inputs outside Go analysis setup")
        # Only a platform lane runs on its platform's other runners.
        if lane.get("runner", "macos-15") not in {"macos-15", "ubuntu-24.04"} | PLATFORMS.get(lane.get("platform"), set()):
            raise ValueError(f"unsupported CI runner: {name}")
        if "workers" in lane and (type(lane["workers"]) is not int or not 1 <= lane["workers"] <= 8):
            raise ValueError(f"CI lane {name} workers must be an integer from 1 through 8")
        if "shards" in lane and (type(lane["shards"]) is not int or not 2 <= lane["shards"] <= 8):
            raise ValueError(f"CI lane {name} shards must be an integer from 2 through 8")
        if "platform" in lane and (lane["platform"] not in PLATFORMS
                                   or lane.get("runner", "ubuntu-24.04") not in PLATFORMS[lane["platform"]]
                                   or set(lane["profiles"]) & {"fast", GATE}):
            raise ValueError(f"CI lane {name} is a platform lane on its platform's runners, outside the fast and {GATE} tiers")
    validate_sources(values)
    expected = Counter(stage["name"] for stage in expand([GATE]))
    actual = Counter(stage["name"] for lane in values.values() if GATE in lane["profiles"]
                     for stage in expand(lane["targets"]))
    if actual != expected:
        raise ValueError(f"CI {GATE} profile differs from the {GATE} gate: "
                         f"missing={expected - actual}, extra={actual - expected}")
    for name, lane in values.items():
        if "fast" in lane["profiles"] or "preflight" in lane:
            command = lane.get("preflight", [])
            if (lane["profiles"] != ["fast"] or lane.get("shards", 1) != 1
                    or not isinstance(command, list) or len(command) < 3
                    or command[:2] != ["test:contract", "--"]
                    or (command != ["test:contract", "--", "./test/contract/..."]
                        and any(not isinstance(package, str)
                                or not re.fullmatch(r"\./test/contract/[a-z][a-z0-9_]*", package)
                                or not (ROOT / "lycaon" / package).is_dir() for package in command[2:]))
                    or len(set(command[2:])) != len(command[2:])):
                raise ValueError(f"CI lane {name} requires scoped admission contracts")
    fast = Counter(stage["name"] for lane in values.values() if "fast" in lane["profiles"]
                   for stage in expand(lane["targets"]))
    extra = set(fast) - {stage["name"] for stage in expand([FAST_GATE])}
    if not fast or extra or max(fast.values()) > 1:
        raise ValueError(f"CI fast tier must run stages of {FAST_GATE} once each: {dict(fast)}")
    expected = Counter(stage["name"] for stage in expand([GATE]) if stage["name"] != "build:cross")
    actual = Counter(stage["name"] for lane in values.values() if "integration" in lane["profiles"] and "platform" not in lane
                     for stage in expand(lane["targets"]))
    if actual != expected:
        raise ValueError(f"integration partition differs from check minus cross compilation: {expected - actual}, {actual - expected}")
    platform = {stage["name"] for lane in values.values() if "platform" in lane for stage in expand(lane["targets"])}
    repeated = platform & {stage["name"] for stage in expand([GATE])}
    if repeated:
        raise ValueError(f"platform lanes repeat {GATE} stages: {sorted(repeated)}")
    for name, lane in values.items():
        if ("check" in lane["profiles"] or "release" in lane["profiles"] and "platform" in lane) and "nightly" not in lane["profiles"]:
            raise ValueError(f"nightly must include qualification lane {name}")
    return values


def max_parallel(profile):
    """Jobs a profile's matrix runs at once; the catalog's capacity table bounds every hosted class."""
    caps = catalog()["capacity"]["max_parallel"]
    if set(caps) != PROFILES or not all(type(cap) is int and cap >= 1 for cap in caps.values()):
        raise ValueError("every CI profile declares a positive maximum of parallel jobs")
    if profile not in PROFILES:
        raise ValueError(f"unsupported CI profile: {profile}")
    return caps[profile]


def runs_protected_launch(target):
    """Whether a target's Go run reaches the protected launch leg: its catalog recipe selects
    the launch package, or its script selects packages that include it."""
    recipe = catalog()["go"].get(target)
    if recipe is None:
        return target in LAUNCH_SCRIPT_TARGETS
    return any(selects_package(pattern, LAUNCH_PACKAGE) for pattern in recipe["packages"])


def selects_package(pattern, package):
    """Whether a Go package pattern names package, directly or through a `/...` tree."""
    if pattern.endswith("/..."):
        root = pattern[:-len("/...")]
        return package == root or package.startswith(root + "/")
    return pattern == package


def analysis_set(targets):
    lint = bool(set(targets) & {"lint:fast", "lint:full"})
    vulnerabilities = bool(set(targets) & {"lint:vuln", "lint:vuln:fresh"})
    return "all" if lint and vulnerabilities else "lint" if lint else "vulnerabilities" if vulnerabilities else "none"


def scripts_python_in_scope(scope):
    """Whether changed-scripts coverage may run a Python suite: only when scripts Python changed under a bounded scope.

    Without a scope the target resolves its own change base and may run every suite, so the plan provisions for all of them."""
    if scope is None or scope["full"]:
        return True
    return any(path.startswith("scripts/") and path.endswith(".py") for path in scope["paths"])


def matrix(profile, suite="all", scope=None, platform=False, timings=None):
    """A profile's lanes; `platform` keeps only its platform lanes, for a run whose other lanes another gate covers."""
    if profile not in PROFILES or suite not in SUITES or profile != "nightly" and suite != "all":
        raise ValueError(f"unsupported CI selection: {profile}/{suite}")
    values = lanes()
    selected = {name for name, lane in values.items() if "platform" in lane} if platform else set(values)
    # A broad change runs the whole check gate and every platform lane of the merge queue.
    broad = profile == "integration" and scope is not None and scope["full"]
    if profile == "integration" and scope is not None and not broad:
        from ci_policy.impact import selected_lanes
        selected &= selected_lanes(scope, values)
    result = []
    for name, lane in values.items():
        if name not in selected:
            continue
        tier = GATE if broad and "platform" not in lane else profile
        if tier not in lane["profiles"] or suite != "all" and lane["suite"] not in {"all", suite}:
            continue
        if profile == "nightly" and suite != "all" and lane["suite"] == "all" and GATE in lane["profiles"]:
            continue
        count = lane.get("shards", 1)
        for index in range(1, count + 1):
            result.append({"lane": name,
                           "timing_row": row_key(lane.get("timing_source", name), f"{index}/{count}" if count > 1 else ""),
                           "shard": f"{index}/{count}" if count > 1 else "",
                           "minutes": lane["minutes"], "job_minutes": lane["minutes"] + 30,
                           "setup": "shell" if profile == "fast" and "den:test:rust" in native_targets(scope) else lane["setup"],
                           "runtime_engines": lane.get("runtime_engines", True),
                           "notices": "licenses:notices" in lane["targets"],
                           "containers": CONTAINER_TARGET in lane["targets"],
                           "browser": BROWSER_TARGET in lane["targets"]
                           and "browser" in lane.get("arguments", ["browser"]),
                           "userlands": USERLANDS_TARGET in lane["targets"],
                           "linux_launch": any(runs_protected_launch(target) for target in lane["targets"]),
                           "repository_tools": REPOSITORY_TOOLS_TARGET in lane["targets"]
                           or "scripts:coverage:changes" in lane["targets"] and scripts_python_in_scope(scope),
                           "nix": any(target.startswith("nix:") for target in lane["targets"])
                           or "nix" in lane.get("arguments", []),
                           "analysis": analysis_set(lane["targets"]),
                           "runner": lane.get("runner", "ubuntu-24.04")})
    if not result:
        raise ValueError("CI selection contains no verification")
    return {"include": order(result, values, timings, max_parallel(profile))}


def invocation(targets):
    """Expand private groups, which Task refuses to run by name, into their members."""
    plan = catalog()
    private = set(plan.get("private", []))
    return [member for target in targets
            for member in (plan["groups"][target] if target in private else [target])]
