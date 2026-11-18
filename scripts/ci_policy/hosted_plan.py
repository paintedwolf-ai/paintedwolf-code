"""Hosted lane catalog validation and matrix selection."""
from collections import Counter
from pathlib import Path
import re

from ci_policy.hosted_schedule import order, PRIORITIES
from ci_policy.timings import row_key
from ci_policy.native_admission import runs_package_metadata, runs_script_coverage
from verification_plan import catalog, expand

ROOT = Path(__file__).resolve().parents[2]
PROFILES = {"fast", "check", "nightly", "release", "integration"}
GATE = "check"
FAST_GATE = "check-fast"
# The static job runs these before any lane, so the fast tier omits them.
STATIC_STAGES = {"comments:check", "test:fail-messages:check", "docs:links"}
SUITES = {"all", "behavior", "race", "performance", "fuzz", "e2e"}
SETUPS = {"verification", "shell", "harness", "frontend", "analysis"}
# A platform lane runs a check that only its platform can run, so the cross-platform check gate,
# which every development host runs, leaves it out; the merge queue runs it beside that gate.
# Each platform names the runners it may use: architecture-specific code needs each architecture.
PLATFORMS = {"linux": {"ubuntu-24.04", "ubuntu-24.04-arm"}}
# The setup inputs a lane can need, as the catalog's `provisions` declares them; `engine_free` names
# targets that run without the runtime engines every other target needs.
# Change areas a lane declares in `changes`; the change planner selects the lanes of each area it finds.
AREAS = {"always", "go", "den", "scripts", "confinement", "command-language", "shell-native", "decide-native",
         "documentcore-native"}
# Execution a lane declares in `phases`: admission's check phases, admission's test phases, or the Go packages
# a change affects. Each belongs to one lane.
PHASES = {"admission", "admission-tests", "affected-packages"}
INPUTS = ("containers", "browser", "userlands", "repository_tools", "nix", "linux_launch", "notices",
          "analysis_lint", "analysis_vulnerabilities")


def supplemental(lane):
    return "platform" in lane or lane["suite"] == "performance" and "integration" in lane["profiles"]


def lanes():
    values = catalog()["ci"]
    taskfile = (ROOT / "Taskfile.yml").read_text()
    targets = set(re.findall(r"^  ([\w:-]+):$", taskfile, re.M))
    for name, lane in values.items():
        if not re.fullmatch(r"[a-z][a-z0-9-]*", name):
            raise ValueError(f"invalid CI lane: {name}")
        if set(lane) - {"targets", "minutes", "profiles", "suite", "setup", "runner", "workers", "shards", "priority",
                        "platform", "arguments", "early_targets", "contract_first", "admission_checks", "changes",
                        "phases"}:
            raise ValueError(f"unknown CI lane fields: {name}")
        if "arguments" in lane and (len(lane["targets"]) != 1 or not lane["arguments"]
                                    or not all(isinstance(value, str) and value for value in lane["arguments"])):
            raise ValueError(f"CI lane {name} arguments belong to exactly one target")
        if supplemental(lane) and "platform" not in lane:
            if (lane["targets"] != ["test:stress"] or not lane.get("arguments")
                    or not {"integration", "nightly", "release"}.issubset(lane["profiles"])):
                raise ValueError(f"CI lane {name} requires explicit scale scope and complete qualification profiles")
        changes = lane.get("changes", [])
        if (not isinstance(changes, list) or not set(changes) <= AREAS or len(set(changes)) != len(changes)
                or not changes and set(lane["profiles"]) & {"fast", "integration"}):
            raise ValueError(f"CI lane {name} needs the change areas that select it")
        if lane.get("phases", "admission") not in PHASES:
            raise ValueError(f"CI lane {name} has unknown phases")
        admission = lane.get('admission_checks', [])
        if ('admission_checks' in lane and lane.get('phases') != 'admission'
                or not isinstance(admission, list)
                or admission not in ([], [['test:confine:linux', '--', 'supervisor']])):
            raise ValueError(f'CI lane {name} has invalid early native checks')
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
        if lane["setup"] == "frontend" and not set(lane["targets"]).issubset({
                "den:typecheck", "den:lint", "den:test", "den:coverage:changes", "den:knip:changes"}):
            raise ValueError(f"CI lane {name} requires toolchains outside frontend setup")
        if lane["setup"] == "analysis" and not set(lane["targets"]).issubset({
                "lint:fast", "lint:full", "lint:deadcode", "lint:vuln", "lint:vuln:fresh", "comments:check",
                "test:fail-messages:check", "arch-lint:changes"}):
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
    for phase in PHASES:
        if sum(lane.get("phases") == phase for lane in values.values()) > 1:
            raise ValueError(f"CI phases {phase} belong to one lane")
    validate_provisions(values, targets)
    expected = Counter(stage["name"] for stage in expand([GATE]))
    actual = Counter(stage["name"] for lane in values.values() if GATE in lane["profiles"]
                     for stage in expand(lane["targets"]))
    if actual != expected:
        raise ValueError(f"CI {GATE} profile differs from the {GATE} gate: "
                         f"missing={expected - actual}, extra={actual - expected}")
    fast = Counter(stage["name"] for lane in values.values() if "fast" in lane["profiles"]
                   for stage in expand(lane["targets"]))
    expected_fast = Counter(stage['name'] for stage in expand([FAST_GATE])
                            if stage['name'] != 'licenses:notices' and stage['name'] not in STATIC_STAGES)
    if fast != expected_fast:
        raise ValueError(f"CI fast tier must run stages of {FAST_GATE} once each: {dict(fast)}")
    expected = Counter(stage["name"] for stage in expand([GATE]))
    actual = Counter(stage["name"] for lane in values.values() if "integration" in lane["profiles"] and not supplemental(lane)
                     for stage in expand(lane["targets"]))
    if actual != expected:
        raise ValueError(f"integration partition differs from check: {expected - actual}, {actual - expected}")
    platform = {stage["name"] for lane in values.values() if "platform" in lane for stage in expand(lane["targets"])}
    repeated = platform & {stage["name"] for stage in expand([GATE])}
    if repeated:
        raise ValueError(f"platform lanes repeat {GATE} stages: {sorted(repeated)}")
    for name, lane in values.items():
        if ("check" in lane["profiles"] or "release" in lane["profiles"] and supplemental(lane)) and "nightly" not in lane["profiles"]:
            raise ValueError(f"nightly must include qualification lane {name}")
    stress = Counter(package for lane in values.values()
                     if "nightly" in lane["profiles"] and lane["targets"] == ["test:stress"]
                     for package in lane.get("arguments", catalog()["go"]["test:stress"]["packages"]))
    expected_stress = Counter(catalog()["go"]["test:stress"]["packages"])
    if stress != expected_stress:
        raise ValueError(f"nightly stress partition differs: {expected_stress - stress}, {stress - expected_stress}")
    return values


def lanes_of(values, areas):
    """The lanes a change in any of `areas` selects."""
    return {name for name, lane in values.items() if set(lane.get("changes", [])) & set(areas)}


def max_parallel(profile):
    """Jobs a profile's matrix runs at once; the catalog's capacity table bounds every hosted class."""
    caps = catalog()["capacity"]["max_parallel"]
    if set(caps) != PROFILES or not all(type(cap) is int and cap >= 1 for cap in caps.values()):
        raise ValueError("every CI profile declares a positive maximum of parallel jobs")
    if profile not in PROFILES:
        raise ValueError(f"unsupported CI profile: {profile}")
    return caps[profile]


def selects_package(pattern, package):
    """Whether a Go package pattern names package, directly or through a `/...` tree."""
    if pattern.endswith("/..."):
        root = pattern[:-len("/...")]
        return package == root or package.startswith(root + "/")
    return pattern == package


def stages_of(targets):
    """A lane's stages; a Task target the catalog does not plan runs as itself."""
    declared = set(catalog()["groups"]) | set(catalog()["go"]) | set(catalog()["tasks"])
    return [stage for target in targets
            for stage in (expand([target]) if target in declared else [{"kind": "task", "name": target}])]


def provisioned(lane):
    """The setup inputs a lane needs, derived from the targets and Go packages it runs.
    A target's provision may apply only to some of its arguments, such as one leg of a script."""
    stages = stages_of(lane["targets"])
    names = set(lane["targets"]) | {stage["name"] for stage in stages}
    arguments = lane.get("arguments", [])
    packages = [package for stage in stages if stage["kind"] == "go"
                for package in (arguments or stage["packages"])]

    def needs(provision):
        if any(target in names and (not legs or not arguments or set(legs) & set(arguments))
               for target, legs in provision["targets"].items()):
            return True
        return any(selects_package(pattern, needed) for pattern in packages for needed in provision.get("packages", []))

    provisions = catalog()["provisions"]
    result = {name: name in provisions and needs(provisions[name]) for name in INPUTS}
    result["runtime_engines"] = not names <= set(provisions.get("engine_free", {"targets": {}})["targets"])
    return result


def validate_provisions(values, targets):
    """Each provision names real targets and packages, and some lane needs it."""
    declared = targets | set(catalog()["groups"]) | set(catalog()["go"]) | set(catalog()["tasks"])
    unknown = sorted(set(catalog()["provisions"]) - {*INPUTS, "engine_free"})
    if unknown:
        raise ValueError(f"no setup input is named {', '.join(unknown)}")
    needed = {name: False for name in catalog()["provisions"]}
    for name, provision in catalog()["provisions"].items():
        missing = sorted(set(provision["targets"]) - declared)
        missing += [package for package in provision.get("packages", [])
                    if not (ROOT / "lycaon" / package.removeprefix("./")).is_dir()]
        if missing:
            raise ValueError(f"provision {name} names what nothing declares: {', '.join(missing)}")
    for lane in values.values():
        for name, used in provisioned({**lane, "arguments": []}).items():
            if name in needed:
                needed[name] = needed[name] or used
        if "engine_free" in needed:
            # The engine-free declaration serves a lane that runs without the engines.
            needed["engine_free"] = needed["engine_free"] or not provisioned(lane)["runtime_engines"]
    unused = sorted(name for name, used in needed.items() if not used)
    if unused:
        raise ValueError(f"no lane runs what these provisions serve: {', '.join(unused)}")


def matrix(profile, suite="all", scope=None, platform=False, timings=None):
    """A profile's lanes; `platform` keeps only its platform lanes, for a run whose other lanes another gate covers."""
    if profile not in PROFILES or suite not in SUITES or profile != "nightly" and suite != "all":
        raise ValueError(f"unsupported CI selection: {profile}/{suite}")
    values = lanes()
    selected = {name for name, lane in values.items() if supplemental(lane)} if platform else set(values)
    # A broad change runs the whole check gate and every platform lane of the merge queue.
    broad = profile == "integration" and scope is not None and scope["full"]
    prose = False
    if profile in {"fast", "integration"} and scope is not None:
        from ci_policy.impact import docs_only, fast_lanes, selected_lanes
        selected &= (fast_lanes if profile == "fast" else selected_lanes)(scope, values)
        prose = profile == "fast" and docs_only(scope.get("paths", []))
    result = []
    for name, lane in values.items():
        if name not in selected:
            continue
        tier = GATE if broad and not supplemental(lane) else profile
        if tier not in lane["profiles"] or suite != "all" and lane["suite"] not in {"all", suite}:
            continue
        if profile == "nightly" and suite != "all" and lane["suite"] == "all" and GATE in lane["profiles"]:
            continue
        count = lane.get("shards", 1)
        needs = provisioned(lane)
        for index in range(1, count + 1):
            result.append({"lane": name,
                           "timing_row": row_key(name, f"{index}/{count}" if count > 1 else ""),
                           "shard": f"{index}/{count}" if count > 1 else "",
                           "minutes": lane["minutes"], "step_minutes": lane["minutes"] + 1, "job_minutes": lane["minutes"] + 30,
                           "setup": lane["setup"],
                           "runtime_engines": needs["runtime_engines"],
                           "notices": needs["notices"],
                           "containers": needs["containers"],
                           "browser": needs["browser"],
                           "userlands": needs["userlands"],
                           "linux_launch": needs["linux_launch"],
                           # Admission runs package metadata and script coverage in the ready lane when the change needs them.
                           "repository_tools": needs["repository_tools"]
                           or (lane.get("phases") == "admission"
                               and (runs_package_metadata(scope) or runs_script_coverage(scope))),
                           "nix": needs["nix"],
                           "analysis": {(True, True): "all", (True, False): "lint", (False, True): "vulnerabilities",
                                        (False, False): "none"}[needs["analysis_lint"], needs["analysis_vulnerabilities"]],
                           "runner": lane.get("runner", "ubuntu-24.04")})
    # Only a pull request of prose alone has no lane: the static job verifies it.
    if not result and not prose:
        raise ValueError("CI selection contains no verification")
    return {"include": order(result, values, timings)}


def invocation(targets):
    """Expand private groups, which Task refuses to run by name, into their members."""
    plan = catalog()
    private = set(plan.get("private", []))
    return [member for target in targets
            for member in (plan["groups"][target] if target in private else [target])]
