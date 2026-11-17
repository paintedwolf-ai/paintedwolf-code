"""The Go runs a tier performs: catalog lanes and the `./task` steps of tier workflows, each with the
platform, tags, mode, package and name selection, and environment its tests see."""

from dataclasses import dataclass, field
import re
import shlex

from verification_plan import CATALOG, GO_MODULE, catalog, execution_environment, expand

ROOT = CATALOG.parent.parent
WORKFLOWS = ROOT / ".github" / "workflows"
EVENTS = {"pull_request", "merge_group", "schedule", "push"}
# Qualification is dispatched, not scheduled, yet every release requires it.
QUALIFICATION = "qualification.yml"
RUNNERS = {"macos": ("darwin", "arm64"), "ubuntu-24.04-arm": ("linux", "arm64"), "ubuntu": ("linux", "amd64"),
           "windows": ("windows", "amd64")}
# The bundled-scanner wrapper sets these inside the stage, after the scheduler's environment filter.
BUNDLED_ENVIRONMENT = {"LYCAON_SCANNER_SUITE_REQUIRED": "1", "LYCAON_ENGINE_ROOT": None}


@dataclass
class Run:
    """One `go test` invocation as a tier performs it."""
    label: str
    goos: str
    goarch: str
    tags: frozenset
    short: bool
    environment: dict
    packages: list
    exclude: str = ""
    run: str = None
    skip: str = None
    module: str = field(default="")

    def selects_package(self, directory):
        path = "." if directory == "." else "./" + directory
        for pattern in self.packages:
            if pattern.endswith("/..."):
                root = pattern[:-4]
                if root in {".", "./"} or path == root or path.startswith(root + "/"):
                    break
            elif pattern.rstrip("/") == path:
                break
        else:
            return False
        return not (self.exclude and re.search(self.exclude, f"{self.module}/{directory}"))

    def selects_name(self, name):
        if self.run is not None and not re.search(top_level(self.run), name):
            return False
        return self.skip is None or not re.search(top_level(self.skip), name)


def top_level(pattern):
    """The element of a -run pattern that applies to top-level tests: up to the first unbracketed slash."""
    depth = 0
    for index, character in enumerate(pattern):
        if character == "[":
            depth += 1
        elif character == "]":
            depth -= 1
        elif character == "/" and depth == 0:
            return pattern[:index]
    return pattern


def platform(runner):
    for prefix, value in RUNNERS.items():
        if runner.startswith(prefix):
            return value
    return None


def stage_runs(label, stage, goos, goarch, environment, module):
    options = stage["options"]
    tags = set()
    for index, option in enumerate(options):
        if option == "--tags":
            tags.update(options[index + 1].split(","))
        elif option == "--race":
            tags.add("race")
    flags = {}
    for flag in stage["flags"]:
        name, _, value = flag.partition("=")
        flags[name] = value
    if "-tags" in flags:
        tags.update(flags["-tags"].split(","))
    if stage.get("bundled"):
        environment = {**environment, **BUNDLED_ENVIRONMENT}
    return Run(f"{label}: {stage['name']}", goos, goarch, frozenset(tags),
               "--full" not in options and "-short=false" not in stage["flags"], environment,
               list(stage["packages"]), stage.get("exclude", ""), flags.get("-run"), flags.get("-skip"), module)


def declared(environment):
    """What the scheduler hands a test; an unresolved workflow expression is set to an unknown value."""
    return execution_environment({k: (None if isinstance(v, str) and "${{" in v else str(v))
                                  for k, v in environment.items()})


def go_runs(names, cli, label, goos, goarch, environment, module, taskfile):
    """Go runs of the named targets: catalog recipes, and the digest calls of scripted tasks."""
    plan = catalog()
    recipes = [n for n in names if n in plan["go"] or n in plan["groups"]]
    runs = [stage_runs(label, stage, goos, goarch, environment, module)
            for stage in (expand(recipes, cli) if recipes else []) if stage["kind"] == "go"]
    for name in names:
        if name not in recipes:
            runs += [Run(f"{label}: {name}", goos, goarch, tags, short, {**environment, **assigned}, packages,
                         "", run, None, module)
                     for tags, short, assigned, packages, run in scripted_runs(name, taskfile)]
    return runs


def scripted_runs(task, taskfile, seen=None):
    """`go-test-digest.sh` calls a Taskfile task makes, directly, through the scripts it runs, or through
    the tasks it calls. A value only known when the script runs selects anything."""
    seen = seen or set()
    if task in seen or task not in taskfile:
        return []
    seen.add(task)
    runs = []
    for command in taskfile[task].get("cmds", []):
        if isinstance(command, dict):
            runs += scripted_runs(command.get("task", ""), taskfile, seen)
            continue
        runs += digest_calls(command)
        for script in re.findall(r"\{\{\.SCRIPTS_DIR\}\}/([\w./-]+\.(?:sh|py))", command):
            path = ROOT / "scripts" / script
            if script != "go-test-digest.sh" and path.is_file():
                runs += digest_calls(path.read_text(), literal=script.endswith(".py"))
        for program in re.findall(r"\bgo run (\./[\w./-]+)", command):
            runs += program_calls(GO_MODULE / program)
    return runs


def program_calls(directory):
    """Digest calls a Go program builds, in its own package or in a module package it imports."""
    sources = [p for p in directory.glob("*.go") if not p.name.endswith("_test.go")]
    prefix = f'"{module_path()}/'
    imported = {line.strip()[len(prefix):-1] for p in sources for line in p.read_text().splitlines()
                if line.strip().startswith(prefix) and line.strip().endswith('"')}
    for path in imported:
        sources += [p for p in (GO_MODULE / path).glob("*.go") if not p.name.endswith("_test.go")]
    return [call for p in sources for call in digest_calls(p.read_text(), literal=True)]


def list_elements(text, start):
    """The remaining elements of a list literal from start: string values, and `${dynamic}` for the rest."""
    elements, depth, position, element = [], 0, start, ""
    while position < len(text):
        character = text[position]
        if character in "\"'" and depth == 0:
            end = text.index(character, position + 1)
            element += text[position:end + 1]
            position = end
        elif character in "([{":
            depth += 1
            element += character
        elif character in ")]}" and depth > 0:
            depth -= 1
            element += character
        elif character in ")]}" or character == "," and depth == 0:
            element = element.strip()
            if element:
                quoted = re.fullmatch(r"[\"']([^\"']*)[\"']", element)
                elements.append(quoted.group(1) if quoted else "${dynamic}")
            element = ""
            if character != ",":
                break
        else:
            element += character
        position += 1
    return elements


def digest_calls(text, literal=False):
    """Digest calls written as shell lines, or (literal) as argument list literals in Python or Go source."""
    calls = []
    if literal:
        for match in re.finditer(r"go-test-digest\.sh[\"']\)?\s*,", text):
            words = list_elements(text, match.end())
            calls.append(interpret({}, words, False))
        return calls
    for line in re.sub(r"\\\n", " ", text).splitlines():
        if "go-test-digest.sh" not in line or line.lstrip().startswith("#"):
            continue
        try:
            before, after = re.split(r"\S*go-test-digest\.sh\S*", line, maxsplit=1)
            words, before = shlex.split(after), shlex.split(before)
        except ValueError:
            continue
        cut = next((i for i, w in enumerate(words) if w in {"|", "||", "&&", ";", ")"}), len(words))
        assigned = dict(w.split("=", 1) for w in before if re.fullmatch(r"[A-Z_][A-Z0-9_]*=.*", w))
        calls.append(interpret(assigned, words[:cut], "with-bundled-scanners.sh" in line))
    return calls


def interpret(assigned, words, bundled):
    options, rest = (words[:words.index("--")], words[words.index("--") + 1:]) if "--" in words else (words, [])
    tags = {t for i, w in enumerate(options) if w == "--tags" for t in options[i + 1].split(",")}
    packages, run, index = [], None, 0
    dynamic = any("$" in w or "{" in w for w in rest)
    while index < len(rest):
        word = rest[index]
        name, _, value = word.partition("=")
        if name in {"-run", "-tags"} and not value and index + 1 < len(rest):
            index += 1
            value = rest[index]
        if name == "-run":
            run = None if "$" in value or "{" in value else value
        elif name == "-tags":
            tags.update(value.split(","))
        elif word.startswith("./"):
            packages.append(word)
        index += 1
    environment = {k: (None if "$" in v else v) for k, v in assigned.items()}
    if bundled:
        environment.update(BUNDLED_ENVIRONMENT)
    return frozenset(tags), "--full" not in options, environment, packages or (["./..."] if dynamic or not rest else []), run


def lane_runs(workflows, module, taskfile):
    """The catalog's CI lanes, run by the verification workflow's matrix job with its environment."""
    job = workflows["verification.yml"]["jobs"]["verify"]
    environment = declared({**workflows["verification.yml"].get("env", {}), **job.get("env", {})})
    runs = []
    for name, lane in catalog()["ci"].items():
        goos, goarch = platform(lane.get("runner", "ubuntu-24.04"))
        for target in lane["targets"]:
            cli = lane.get("arguments", []) if len(lane["targets"]) == 1 else []
            runs += go_runs([target], cli, f"lane {name}", goos, goarch, environment, module, taskfile)
    return runs


def tier_workflows(workflows):
    """Workflows an event or Qualification starts, and the reusable workflows they call."""
    def triggers(document):
        value = document.get("on", document.get(True, {}))
        return set(value) if isinstance(value, (dict, list)) else {value}

    selected = {name for name, document in workflows.items() if triggers(document) & EVENTS or name == QUALIFICATION}
    pending = list(selected)
    while pending:
        for job in workflows[pending.pop()].get("jobs", {}).values():
            called = str(job.get("uses", ""))
            if called.startswith("./.github/workflows/"):
                name = called.rsplit("/", 1)[-1]
                if name in workflows and name not in selected:
                    selected.add(name)
                    pending.append(name)
    return sorted(selected)


def task_invocations(script):
    """Each `./task` command line of a step: its leading assignments, target names and arguments."""
    joined = re.sub(r"\\\n", " ", script)
    for line in joined.splitlines():
        if "./task " not in line:
            continue
        try:
            words = shlex.split(line, comments=True)
        except ValueError:
            continue
        if "./task" not in words:
            continue
        start = words.index("./task")
        assignments = dict(w.split("=", 1) for w in words[:start] if re.fullmatch(r"[A-Z_][A-Z0-9_]*=.*", w))
        rest = words[start + 1:]
        cut = next((i for i, w in enumerate(rest) if w in {"|", "||", "&&", ";", "2>&1", ">"}), len(rest))
        rest = rest[:cut]
        names, cli = (rest[:rest.index("--")], rest[rest.index("--") + 1:]) if "--" in rest else (rest, [])
        yield assignments, [n for n in names if not n.startswith("-")], cli


def workflow_runs(workflows, module, taskfile):
    runs = []
    for name in tier_workflows(workflows):
        document = workflows[name]
        for job_name, job in document.get("jobs", {}).items():
            if name == "verification.yml" and job_name == "verify":
                continue
            target = platform(str(job.get("runs-on", "")))
            if target is None:
                continue
            base = {**document.get("env", {}), **job.get("env", {})}
            for step in job.get("steps", []):
                for assignments, names, cli in task_invocations(str(step.get("run", ""))):
                    environment = declared({**base, **step.get("env", {}), **assignments})
                    label = f"{name} {job_name}: {step.get('name', names)}"
                    runs += go_runs(names, cli, label, *target, environment, module, taskfile)
    return runs


def module_path():
    return re.search(r"^module (\S+)", (GO_MODULE / "go.mod").read_text(), re.M).group(1)
