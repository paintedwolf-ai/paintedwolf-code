"""Declared verification recipes and deterministic request compatibility."""

import hashlib
import json
from pathlib import Path
import re
import shlex
import sys


CATALOG = Path(__file__).with_name("verification-plan.json")
GO_MODULE = CATALOG.parent.parent / "lycaon"
DEN = CATALOG.parent.parent / "lycaon-den"
# The scheduler's own descriptors and per-operation paths; a nested request never forwards them.
SCHEDULER_ENV = {"PW_TEST_EXECUTION_TICKET", "PW_TEST_EXECUTION_FD", "PW_TEST_RESOURCE_FD",
                 "PW_TEST_STAGE_RESULT", "PW_TEST_STAGE_RAW", "PW_TEST_STAGE_EVENTS", "PW_TEST_STAGE_ID",
                 "PW_TEST_BATCH_DIRECTORY", "PW_TEST_REUSE_STORE", "PW_TEST_ENVIRONMENT_IDENTITY",
                 "PW_TEST_PACKAGE_IDENTITIES", "PW_TEST_SCRATCH_ROOT", "PW_TEST_SOURCE_ROOT", "PW_TEST_MODULE_ROOT",
                 "PW_REPO_SNAPSHOT_TOKEN"}
# Verification is offline and fixtures are repository-local, so proxy routes stay
# out of a shared, replayable stage.
NETWORK_ENV = {"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
               "http_proxy", "https_proxy", "all_proxy", "no_proxy",
               "NODE_USE_ENV_PROXY", "GIT_SSH_COMMAND"}
SELECTION_GRAMMARS = {"go", "vitest", "playwright"}
PASSTHROUGH = "passthrough"
INFO_ARGUMENTS = {"--help", "-h", "-help", "--version", "--usage"}
SELECTOR_FLAGS = {"-run", "-skip"}
VALUE_FLAGS = SELECTOR_FLAGS | {"-timeout", "-tags", "-parallel", "-p", "-cpu"}
BOOL_FLAGS = {"-short", "-race", "-v"}
EXCLUSIVE_GO_FLAGS = {"-count", "-shuffle", "-failfast", "-bench", "-fuzz", "-coverprofile", "-cpuprofile",
                      "-memprofile", "-trace", "-outputdir", "-blockprofile", "-mutexprofile", "-o", "-exec"}
# The value grammars go test and the worker bound enforce only once a run starts.
GO_DURATION = re.compile(r"[-+]?(0|((\d+\.?\d*|\.\d+)(ns|us|µs|μs|ms|s|m|h))+)")
GO_VALUES = {"-p": (re.compile(r"[1-9]\d*"), "a positive integer"),
             "-parallel": (re.compile(r"[1-9]\d*"), "a positive integer"),
             "-count": (re.compile(r"\d+"), "a non-negative integer"),
             "-cpu": (re.compile(r"[1-9]\d*(,[1-9]\d*)*"), "a comma-separated list of positive integers"),
             "-timeout": (GO_DURATION, "a Go duration such as 90s or 20m"),
             "-shuffle": (re.compile(r"off|on|-?\d+"), "off, on, or an integer seed")}


# The runner's own outcome vocabulary. A project tool's exit code is its own
# dialect, so it stays in the result JSON as task_exit_code rather than standing
# in for the runner's verdict.
EXIT_PASSED = 0
EXIT_FAILED = 1
EXIT_UNVERIFIED = 2


def outcome_exit(code, verified=True):
    """Map a tool's exit code to the runner's verdict."""
    if not verified:
        return EXIT_UNVERIFIED
    if code == 0:
        return EXIT_PASSED
    # 130 and 143 are interruption, not a verdict about the source.
    if code in (130, 143) or code < 0:
        return EXIT_UNVERIFIED
    return EXIT_FAILED


def identity(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def declared_variable(name, declaration):
    return name in declaration["names"] or name.startswith(tuple(declaration["prefixes"]))


def execution_environment(environment, declaration=None):
    """Shared verification receives only declared inputs, so agent sessions and their secrets never
    reach tests or split otherwise identical requests.

    Every declared value must be a function of the checkout rather than of the call that
    submitted it. A per-invocation value would give each request its own compatibility
    digest, which silently ends batch sharing and result reuse."""
    declaration = declaration or catalog()["environment"]
    return {key: value for key, value in environment.items()
            if key not in SCHEDULER_ENV and key not in NETWORK_ENV
            and declared_variable(key, declaration)}


def catalog():
    value = json.loads(CATALOG.read_text())
    if set(value) != {"groups", "go", "tasks", "private", "selection", "resources", "environment", "ci",
                      "runner_priority", "capacity"}:
        raise ValueError("invalid verification catalog")
    names = set()
    for section in ("groups", "go", "tasks"):
        if not isinstance(value[section], dict) or names.intersection(value[section]):
            raise ValueError("verification names must have one declaration")
        names.update(value[section])
    if not isinstance(value["selection"], dict) or not set(value["selection"]).issubset(names):
        raise ValueError("selection grammars must be declared for declared verification names")
    if not set(value["selection"].values()).issubset(SELECTION_GRAMMARS | {PASSTHROUGH}):
        raise ValueError("unknown verification selection grammar")
    if any(grammar == "go" and name not in value["go"] for name, grammar in value["selection"].items()):
        raise ValueError("a Go selection grammar requires a Go recipe")
    for section in ("groups", "tasks"):
        for name, items in value[section].items():
            if not isinstance(items, list) or not items or not all(isinstance(item, str) and item for item in items):
                raise ValueError(f"verification {name} requires a nonempty command or stage list")
            if section == "groups" and not set(items).issubset(names):
                raise ValueError(f"verification {name} references an undeclared stage")
    if not isinstance(value["private"], list) or not set(value["private"]).issubset(names):
        raise ValueError("private verification names must be declared")
    for name, recipe in value["go"].items():
        if not isinstance(recipe, dict) or not {"options", "packages"}.issubset(recipe):
            raise ValueError(f"verification {name} requires a Go recipe")
        if not isinstance(recipe["options"], list) or not isinstance(recipe["packages"], list) or not recipe["packages"]:
            raise ValueError(f"verification {name} requires options and package lists")
        if not all(isinstance(item, str) for item in recipe["options"] + recipe["packages"]):
            raise ValueError(f"verification {name} requires string arguments")
        flags = recipe.get("flags", [])
        if not isinstance(flags, list) or not all(isinstance(item, str) for item in flags):
            raise ValueError(f"verification {name} requires string flags")
        selection = go_selection(flags, name)
        if not selection["shareable"] or selection["packages"]:
            raise ValueError(f"verification {name} requires shareable Go flags")
    environment = value["environment"]
    if (not isinstance(environment, dict) or set(environment) != {"names", "prefixes"}
            or not all(isinstance(items, list) and all(isinstance(item, str) and item for item in items)
                       for items in environment.values())):
        raise ValueError("the verification environment declares names and prefixes")
    for name, spec in value["resources"].items():
        # Catalog stages and invocations outside the catalog declare resources by Task name.
        if name in value["groups"] or name in value["go"]:
            raise ValueError(f"resources describe executed stages, not {name}")
        if (set(spec) - {"locks", "workers", "shared_locks"} or not {"locks", "workers"}.issubset(spec)
                or not isinstance(spec["locks"], list)
                or not all(isinstance(lock, str) and lock for lock in spec["locks"])
                or not isinstance(spec.get("shared_locks", []), list)
                or not all(isinstance(lock, str) for lock in spec.get("shared_locks", []))
                or not set(spec.get("shared_locks", [])).issubset(set(spec["locks"]) - {"*"})
                or not (spec["workers"] in ("shared", "all")
                        or type(spec["workers"]) is int and spec["workers"] > 0)):
            raise ValueError(f"invalid resource recipe: {name}")
    return value


def flag_value(arguments, index, target):
    name, separator, value = arguments[index].partition("=")
    if not separator:
        index += 1
        if index == len(arguments) or arguments[index].startswith("-"):
            raise ValueError(f"{target}: {name} requires a value")
        value = arguments[index]
    if not value:
        raise ValueError(f"{target}: {name} requires a value")
    return index, value


def unknown_flag(target, flag, allowed):
    raise ValueError(f"{target}: unsupported flag {flag!r}; supported flags: {', '.join(sorted(allowed))}. "
                     "No verification was queued")


def go_selection(arguments, target):
    """Validate flags; `shareable` records whether the resulting package outcomes can be reused."""
    packages, flags = [], []
    shareable = True
    index = 0
    while index < len(arguments):
        arg = arguments[index]
        if not arg.startswith("-"):
            if not arg.startswith("./") or arg.endswith(".go") or "=" in arg or ".." in Path(arg).parts:
                shareable = False
            packages.append(arg)
        else:
            name = arg.partition("=")[0]
            if name in VALUE_FLAGS or name in EXCLUSIVE_GO_FLAGS - {"-failfast"}:
                index, value = flag_value(arguments, index, target)
                if name in GO_VALUES and not GO_VALUES[name][0].fullmatch(value):
                    raise ValueError(f"{target}: {name} requires {GO_VALUES[name][1]}, not {value!r}. "
                                     "No verification was queued")
                flags.append(f"{name}={value}")
            elif name in BOOL_FLAGS | {"-failfast"}:
                flags.append(arg)
            else:
                unknown_flag(target, arg, VALUE_FLAGS | BOOL_FLAGS | EXCLUSIVE_GO_FLAGS)
            if name in EXCLUSIVE_GO_FLAGS:
                shareable = False
        index += 1
    return {"packages": sorted(set(packages)), "flags": flags, "shareable": shareable}


def package_prefix(pattern):
    pattern = pattern.rstrip("/")
    return (pattern[:-4].rstrip("/") or ".") if pattern.endswith("/...") else pattern


def within(requested, declared):
    target = package_prefix(requested)
    for pattern in declared:
        prefix = package_prefix(pattern)
        if target == prefix:
            return True
        if pattern.rstrip("/").endswith("/...") and (prefix == "." or target.startswith(prefix + "/")):
            return True
    return False


def scoped_packages(name, requested, declared):
    """A selection narrows what a declared target means; it never reaches outside that scope."""
    if not requested:
        return list(declared)
    outside = [pattern for pattern in requested if not within(pattern, declared)]
    if outside:
        raise ValueError(f"{name} covers {' '.join(declared)}; {' '.join(outside)} lies outside it. "
                         "Use test:digest for packages no declared target covers. No verification was queued")
    return requested


def missing_go_paths(requested):
    """go test resolves relative packages and files inside the module; import paths are its own to resolve."""
    missing = []
    for arg in requested:
        path = GO_MODULE / package_prefix(arg)
        if arg.endswith(".go"):
            if not path.is_file():
                missing.append(arg)
        elif arg.startswith("./") and not path.is_dir():
            missing.append(arg)
    return missing


VITEST_FLAGS = {"-t", "--testNamePattern"}
PLAYWRIGHT_FLAGS = {"-g", "--grep", "--grep-invert", "--retries", "--timeout", "--global-timeout", "--repeat-each"}
LINE_SUFFIX = re.compile(r"(:\d+){1,2}$")


def selection_filters(arguments, allowed, target):
    """Validate flags against a tool's declared grammar and return its positional file filters."""
    filters = []
    index = 0
    while index < len(arguments):
        arg = arguments[index]
        if arg.partition("=")[0] in allowed:
            index, _ = flag_value(arguments, index, target)
        elif arg.startswith("-"):
            unknown_flag(target, arg, allowed)
        else:
            filters.append(arg)
        index += 1
    return filters


def vitest_selection(arguments):
    return all(arg.startswith("src/") for arg in selection_filters(arguments, VITEST_FLAGS, "den:test:digest"))


def unmatched_vitest_filters(filters):
    """Vitest keeps test files whose Den-relative path contains a filter, ignoring case."""
    files = [path.relative_to(DEN).as_posix().lower() for pattern in ("*.test.ts", "*.test.tsx")
             for path in (DEN / "src").rglob(pattern)]
    unmatched = []
    for arg in filters:
        text = LINE_SUFFIX.sub("", arg)
        if Path(text).is_absolute() and Path(text).is_relative_to(DEN):
            text = Path(text).relative_to(DEN).as_posix()
        text = text.removeprefix("./").lower()
        if not any(text in path for path in files):
            unmatched.append(arg)
    return unmatched


def unmatched_playwright_filters(filters):
    """Playwright keeps web-project specs whose absolute path a filter matches as a case-insensitive regex."""
    files = [str(path) for path in (DEN / "e2e").rglob("*.spec.ts") if not path.name.endswith(".desktop.spec.ts")]
    unmatched = []
    for arg in filters:
        text = LINE_SUFFIX.sub("", arg)
        literal = re.fullmatch(r"/(.*)/([gi]*)", text)
        try:
            pattern = re.compile(literal[1], re.I if "i" in literal[2] else 0) if literal else re.compile(text, re.I)
        except re.error:
            # A JavaScript expression Python cannot read is Playwright's to judge.
            continue
        if not any(pattern.search(path) for path in files):
            unmatched.append(arg)
    return unmatched


def declared_names(data):
    return set(data["groups"]) | set(data["go"]) | set(data["tasks"])


def validate_selection(names, arguments):
    """Arguments a target cannot consume are silently dropped by Task, so they are refused here."""
    if not arguments:
        return
    for argument in arguments:
        if argument.partition("=")[0] in INFO_ARGUMENTS:
            raise ValueError(f"{argument} after -- would run {', '.join(names) or 'the gate'} rather than describe "
                             "it; read ./task --list and scripts/verification-plan.json. No verification was queued")
    data = catalog()
    silent = [name for name in names if name in declared_names(data) and name not in data["selection"]]
    if silent:
        raise ValueError(f"{silent[0]} takes no selection arguments; everything after -- would be dropped and "
                         f"the whole target would run. Scope Go work with a target that declares a selection, "
                         f"or with test:digest. No verification was queued")
    if not any(name in data["selection"] for name in names):
        return
    if len(names) != 1:
        raise ValueError("pass verification selection arguments to one target at a time")
    grammar = data["selection"][names[0]]
    if grammar == "go":
        requested = go_selection(arguments, names[0])["packages"]
        scoped_packages(names[0], requested, data["go"][names[0]]["packages"])
        missing = missing_go_paths(requested)
        if missing:
            raise ValueError(f"{names[0]}: {' '.join(missing)} names no package directory or file under "
                             f"{GO_MODULE.name}/. No verification was queued")
    elif grammar == "vitest":
        missing = unmatched_vitest_filters(selection_filters(arguments, VITEST_FLAGS, names[0]))
        if missing:
            raise ValueError(f"{names[0]}: {' '.join(missing)} matches no test file under {DEN.name}/src. "
                             "No verification was queued")
    elif grammar == "playwright":
        missing = unmatched_playwright_filters(selection_filters(arguments, PLAYWRIGHT_FLAGS, names[0]))
        if missing:
            raise ValueError(f"{names[0]}: {' '.join(missing)} matches no web-project spec under {DEN.name}/e2e. "
                             "No verification was queued")


def expand(names, cli=()):
    data = catalog()
    stages = []

    def visit(name, ancestors):
        if name in ancestors:
            raise ValueError(f"cyclic verification group: {name}")
        if name in data["groups"]:
            for child in data["groups"][name]:
                visit(child, {*ancestors, name})
        elif name in data["go"]:
            recipe = data["go"][name]
            flags = go_selection(recipe.get("flags", []), name)["flags"]
            packages = recipe["packages"]
            if cli and data["selection"].get(name) == "go":
                selection = go_selection(cli, name)
                flags = [*flags, *selection["flags"]]
                packages = scoped_packages(name, selection["packages"], recipe["packages"])
            stages.append({"kind": "go", "name": name, "options": recipe["options"],
                           "exclude": recipe.get("exclude", ""), "bundled": recipe.get("bundled", False),
                           "packages": packages, "flags": flags})
        elif name in data["tasks"]:
            arguments = list(data["tasks"][name])
            if name in {"den:test:digest", "den:harness:test"} and cli:
                arguments.extend(["--", *cli])
            stages.append({"kind": "task", "name": name, "arguments": arguments})
        else:
            raise ValueError(f"undeclared verification target: {name}")

    for name in names:
        visit(name, set())
    return stages


def invocation_profile(names, cli=()):
    """What an invocation outside a shared batch holds for its whole run: the union of its stages'
    declarations. A name neither the catalog nor a resource declaration describes holds every resource."""
    from verification_resources import EXCLUSIVE, combine, profile
    data = catalog()
    specs = []
    for name in names or ["default"]:
        if name in data["resources"] and name not in declared_names(data):
            specs.append(data["resources"][name])
            continue
        try:
            stages = expand([name], cli if len(names) == 1 else ())
        except ValueError:
            return EXCLUSIVE
        specs.extend(profile(stage, data) for stage in stages)
    return combine(specs)


def request(arguments, environment):
    """Reject unknown selection flags; explicit nonshareable work keeps exclusive admission."""
    args = list(arguments or ["default"])
    separator = args.index("--") if "--" in args else len(args)
    names, cli = args[:separator], args[separator + 1:]
    if not names or any(name.startswith("-") or "=" in name for name in names):
        return None
    validate_selection(names, cli)
    data = catalog()
    if any(name in data["private"] for name in names):
        return None
    # A passthrough target forwards arguments the shared plan cannot model, so it stays exclusive.
    if cli and (len(names) != 1 or data["selection"].get(names[0]) not in SELECTION_GRAMMARS):
        return None
    if names == ["den:test:digest"] and not vitest_selection(cli):
        return None
    if cli and names[0] in data["go"] and not go_selection(cli, names[0])["shareable"]:
        return None
    # Refreshes write fixtures; replays select a retained source.
    if any(key.startswith("UPDATE_") and value not in {"", "0"} for key, value in environment.items()):
        return None
    if any(environment.get(key) for key in ("PW_TEST_REPLAY_MANIFEST", "PW_SOURCE_SNAPSHOT_COMMIT")):
        return None
    if any(flag.partition("=")[0] in EXCLUSIVE_GO_FLAGS for flag in shlex.split(environment.get("GOFLAGS", ""))):
        return None
    if environment.get("LYCAON_VITEST_PERF", "0") != "0":
        return None
    try:
        stages = expand(names, cli)
    except ValueError:
        return None
    return {"names": names, "stages": stages}


def compatibility(root, binary, environment):
    """`environment` is the declared execution environment, exactly what shared stages receive."""
    files = [CATALOG, *Path(__file__).parent.glob("verification_*.py"),
             Path(__file__).with_name("test-execution.py")]
    return identity({"source": str(Path(root).resolve()), "binary": str(Path(binary).resolve()),
                     "python": [sys.executable, sys.version], "environment": environment,
                     "planner": {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in files}})


def stage_key(stage):
    if stage["kind"] == "task":
        return identity({"kind": "task", "arguments": stage["arguments"]})
    # Exclusions select packages; they do not change how an included package runs.
    return identity({k: stage[k] for k in ("kind", "options", "flags", "bundled")})


def resolve_request(member):
    names = member["plan"]["names"]
    if any(name in catalog()["private"] for name in names):
        raise ValueError("requested verification target is private in the captured source")
    args = member["arguments"]
    cli = args[args.index("--") + 1:] if "--" in args else []
    return expand(names, cli)


def digest_command(stage, packages):
    command = ["bash", "scripts/go-test-digest.sh", *stage["options"], "--name", stage["name"],
               "--", *packages, *stage["flags"]]
    if stage["bundled"]:
        command = ["bash", "scripts/with-bundled-scanners.sh", "--", *command]
    return command
