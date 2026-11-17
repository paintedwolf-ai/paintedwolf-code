"""Every Go test runs in a scheduled tier, or the declared not-run set names it.

A tier's runs come from the catalog's CI lanes and from `./task` steps of the workflows events start
(and Qualification). A test is reached when one run builds its file (GOOS, GOARCH, tags), selects its
package and name, and no skip site of the test or of a function it calls is certain to be taken under
that run's -short mode and the environment the scheduler passes on."""

from dataclasses import dataclass, field
from pathlib import Path
import re
import shlex
import sys

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from ci_policy.go_source import Token
from ci_policy.skip_conditions import Expression, Index, evaluate
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


def go_runs(names, cli, label, goos, goarch, environment, module):
    plan = catalog()
    names = [n for n in names if n in plan["go"] or n in plan["groups"]]
    if not names:
        return []
    return [stage_runs(label, stage, goos, goarch, environment, module)
            for stage in expand(names, cli) if stage["kind"] == "go"]


def lane_runs(workflows, module):
    """The catalog's CI lanes, run by the verification workflow's matrix job with its environment."""
    job = workflows["verification.yml"]["jobs"]["verify"]
    environment = declared({**workflows["verification.yml"].get("env", {}), **job.get("env", {})})
    runs = []
    for name, lane in catalog()["ci"].items():
        goos, goarch = platform(lane.get("runner", "ubuntu-24.04"))
        for target in lane["targets"]:
            cli = lane.get("arguments", []) if len(lane["targets"]) == 1 else []
            runs += go_runs([target], cli, f"lane {name}", goos, goarch, environment, module)
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


def workflow_runs(workflows, module):
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
                    runs += go_runs(names, cli, label, *target, environment, module)
    return runs


class Reach:
    def __init__(self, index, runs):
        self.index = index
        self.runs = runs
        self.compiled = {}

    def condition(self, condition, file, run):
        if isinstance(condition, tuple):
            return condition[1].builds(run.goos, run.goarch, run.tags)
        key = id(condition)
        if key not in self.compiled:
            tree = Expression(self.index, file, condition.bindings).parse(condition.tokens)
            self.compiled[key] = ("not", tree) if condition.negated else tree
        return evaluate(self.compiled[key], run)

    def skipped(self, clauses, run):
        for clause in clauses:
            if all(self.condition(c, f, run) is True for c, f in clause):
                return True
        return False

    def judge(self, file, function):
        """None when some run reaches the test, else the furthest gate it passed."""
        runs = [r for r in self.runs if r.selects_package(file.directory)]
        if not runs:
            return "no run selects its package"
        runs = [r for r in runs if file.builds(r.goos, r.goarch, r.tags)]
        if not runs:
            return f"no run builds it ({file.path.name}: {file.build or 'file name'})"
        runs = [r for r in runs if r.selects_name(function.name)]
        if not runs:
            return "no run's -run selects it"
        clauses = self.index.clauses(function, test=True)
        if all(self.skipped(clauses, r) for r in runs):
            return "every run that builds it skips it"
        return None


def load_workflows():
    from toolchain import python_tools
    sys.path.insert(0, str(python_tools.provision(ROOT / "scripts" / "workflow_lint" / "requirements.txt")))
    import yaml
    return {path.name: yaml.safe_load(path.read_text()) for path in sorted(WORKFLOWS.glob("*.yml"))}


def not_run(index):
    """The declared not-run set: whole build tags, and individual Go and Rust tests named by path."""
    declaration = catalog().get("not_run", {"tags": {}, "go": {}, "rust": {}})
    return declaration["tags"], declaration["go"], declaration["rust"]


def unreached(index, runs, tags, named):
    reach = Reach(index, runs)
    findings, matched = [], set()
    for file, function in index.tests():
        key = f"{file.directory}/{file.path.name}:{function.name}"
        reason = reach.judge(file, function)
        exempt = key in named or (file.build is not None
                                  and any(re.search(rf"(?<![!\w]){re.escape(t)}\b", file.build) for t in tags))
        if exempt:
            matched.add(key)
        if reason and not exempt:
            findings.append(f"{key}: {reason}")
        elif not reason and key in named:
            findings.append(f"{key}: declared not run, yet {next(r.label for r in runs if r.selects_package(file.directory))} reaches it")
    for key in sorted(set(named) - matched):
        findings.append(f"{key}: declared not run but no such test")
    return findings


def missing_rust(named):
    findings = []
    for key in sorted(named):
        path, _, name = key.partition(":")
        source = ROOT / path
        if not source.is_file() or not re.search(rf"\bfn {re.escape(name)}\b", source.read_text()):
            findings.append(f"{key}: declared not run but no such Rust test")
    return findings


def main():
    module = re.search(r"^module (\S+)", (GO_MODULE / "go.mod").read_text(), re.M).group(1)
    workflows = load_workflows()
    runs = lane_runs(workflows, module) + workflow_runs(workflows, module)
    index = Index(GO_MODULE, module)
    tags, named, rust = not_run(index)
    findings = unreached(index, runs, tags, named) + missing_rust(rust)
    for finding in findings:
        print(f"lycaon/{finding}" if not finding.startswith("lycaon-den/") else finding, file=sys.stderr)
    if findings:
        print(f"{len(findings)} tests run in no tier and are not declared not run "
              "(scripts/verification-plan.json not_run)", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
