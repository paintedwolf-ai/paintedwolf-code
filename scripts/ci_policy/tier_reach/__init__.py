"""Every Go test runs in a scheduled tier, or the declared not-run set names it.

A tier's runs come from the catalog's CI lanes and from `./task` steps of the workflows events start
(and Qualification). A test is reached when one run builds its file (GOOS, GOARCH, tags), selects its
package and name, and no skip site of the test or of a function it calls is certain to be taken under
that run's -short mode and the environment the scheduler passes on."""

import re
import sys

from ci_policy.tier_reach.conditions import Expression, Index, evaluate
from ci_policy.tier_reach.runs import GO_MODULE, ROOT, WORKFLOWS, lane_runs, module_path, workflow_runs
from verification_plan import catalog


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
        """The first clause certain to skip under run, or None."""
        for clause in clauses:
            if all(self.condition(c, f, run) is True for c, f in clause):
                return clause
        return None

    def judge(self, file, function):
        """None when some run reaches the test, else the furthest gate it passed."""
        runs = [r for r in self.runs if r.selects_package(file.directory)]
        if not runs:
            return "no run selects its package"
        runs = [r for r in runs if file.builds(r.goos, r.goarch, r.tags)]
        if not runs:
            return f"no run builds it ({file.path.name}: {file.build or 'file name'})"
        if function.name in self.index.reexecuted(file.directory):
            return None
        runs = [r for r in runs if r.selects_name(function.name)]
        if not runs:
            return "no run's -run selects it"
        clauses = self.index.clauses(function, test=True)
        taken = [self.skipped(clauses, r) for r in runs]
        if all(taken):
            return f"every run that builds it skips it ({describe(taken[-1])})"
        return None


def describe(clause):
    """The written conditions of a skip clause, innermost last."""
    written = []
    for condition, _ in clause:
        if isinstance(condition, tuple):
            continue
        text = " ".join(t.text for t in condition.tokens)
        written.append(f"!({text})" if condition.negated else text)
    return " && ".join(written) or "unconditional"


def load_workflows():
    from toolchain import python_tools
    sys.path.insert(0, str(python_tools.provision(ROOT / "scripts" / "workflow_lint" / "requirements.txt")))
    import yaml
    return {path.name: yaml.safe_load(path.read_text()) for path in sorted(WORKFLOWS.glob("*.yml"))}


def not_run():
    """The declared not-run set: whole build tags, and Go and Rust tests named by path and function."""
    declaration = catalog()["not_run"]
    groups = declaration["tests"]
    return (declaration["tags"], {name for group in groups for name in group.get("go", [])},
            {name for group in groups for name in group.get("rust", [])})


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
    module = module_path()
    workflows = load_workflows()
    import yaml
    taskfile = yaml.safe_load((ROOT / "Taskfile.yml").read_text())["tasks"]
    runs = lane_runs(workflows, module, taskfile) + workflow_runs(workflows, module, taskfile)
    index = Index(GO_MODULE, module)
    tags, named, rust = not_run()
    findings = unreached(index, runs, tags, named) + missing_rust(rust)
    for finding in findings:
        print(f"lycaon/{finding}" if not finding.startswith("lycaon-den/") else finding, file=sys.stderr)
    if findings:
        print(f"{len(findings)} tests run in no tier and are not declared not run "
              "(scripts/verification-plan.json not_run)", file=sys.stderr)
        return 1
    return 0
