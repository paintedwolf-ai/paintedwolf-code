"""Size budgets for prompts and code, reported for the change being made.

New or growing maintained code must fit its category limit or explicit cap.
Unchanged legacy excess is reported for tracking. Prompt limits remain absolute.

`PW_BUDGETS_INSPECT="<path> ..." ./task budgets` reports the standing of
those files and directories as if the change had touched them: look before
editing something large.
"""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

from artifact_paths import artifact_root
from ci_policy.budget_snapshot import seal
import change_report
from change_report import Finding

ROOT = Path(__file__).resolve().parent.parent
SUITES = {
    "maintainability": ("./test/contract/maintainability", "TestMaintainabilityWithinBudget"),
    "prompts": ("./test/contract/agentcontext", "TestRenderedPromptsWithinBudget"),
}


def change_set(scope, inspect):
    added, removed = change_report.added_and_removed(ROOT, scope.base)
    lines = change_report.changed_lines(ROOT, scope.base, [p for p in scope.paths if p not in set(removed)])
    return {"base": scope.base, "lines": {path: sorted(numbers) for path, numbers in lines.items()},
            "added": added, "removed": removed, "inspect": inspect}


def run_suites(work, change):
    if work.exists():
        shutil.rmtree(work)
    reports = work / "reports"
    reports.mkdir(parents=True)
    (work / "change.json").write_text(json.dumps(change))
    env = {**os.environ, "PW_BUDGET_REPORT_DIR": str(reports), "PW_CHANGE_SET": str(work / "change.json")}
    tests = "|".join(test for _, test in SUITES.values())
    # Fresh runs: the suites read git state that the test cache does not key on.
    command = ["bash", str(ROOT / "scripts" / "go-test-digest.sh"), "--tags", "budgets", "--name", "budgets", "--",
               "-count=1", "-run", f"^({tests})$", *(package for package, _ in SUITES.values())]
    code = subprocess.call(command, cwd=ROOT / "lycaon", env=env)
    return code, {name: json.loads((reports / f"{name}.json").read_text())
                  for name in SUITES if (reports / f"{name}.json").exists()}


def suite_findings(name, report):
    """Findings for one suite report, and whether any of them fails."""
    out, failed = [], False

    def size(f):
        return f'{f["measured"]:,} {report["categories"][f["category"]]["unit"]}'

    for f in report["findings"]:
        kind, label = f["kind"], f'{f["category"]} {f["id"]}'
        limit = report["categories"][f["category"]]["limit"]
        source = f["sources"][0] if f["sources"] else None
        if kind == "over_limit":
            failed = True
            out.append(Finding("error", name, f"{label}: {size(f)}, past its limit of {limit:,}. Bring it within the "
                               "limit, or add an exception that says why it must be this large", source))
        elif kind == "legacy_debt":
            out.append(Finding("warning", name, f"{label}: {size(f)}, unchanged or smaller than "
                               f"{f['previous']:,} at the base; legacy debt needs a tracking issue", source))
        elif kind == "over_cap":
            failed = True
            out.append(Finding("error", name, f'{label}: {size(f)}, past its exception cap of {f["bound"]:,}. Make it '
                               f'smaller, or revisit the reason: {f["reason"]}', source))
        elif kind == "over_warn":
            out.append(Finding("warning", name, f"{label}: {size(f)}, past the warning line of {f['bound']:,} on the way "
                               f"to its limit of {limit:,}; put new behavior in a new file or package", source))
        elif kind == "excepted":
            out.append(Finding("notice", name, f'{label}: {size(f)}, admitted up to {f["bound"]:,} because: '
                               f'{f["reason"]}', source))
        elif kind == "unneeded":
            out.append(Finding("notice", name, f"{label}: {size(f)} fits its limit of {limit:,}; remove its exception",
                               report["policy"]))
        elif kind == "vanished":
            out.append(Finding("notice", name, f"{label}: no longer exists; remove its exception", report["policy"]))
        elif kind == "exception_added":
            change = (f"raised from {f['previous']:,} to {f['measured']:,}" if f.get("previous") is not None
                      else f"added at {f['measured']:,}")
            out.append(Finding("notice", name, f"{label}: exception {change}; reason: {f['reason']}", report["policy"]))
    untouched = sum(report["untouched"].values())
    if untouched:
        out.append(Finding("info", name, f"{untouched} artifact(s) past their limit were not touched; growth past the limit fails; unchanged excess is tracked"))
    out += [Finding("warning", name, text) for text in report["warnings"]]
    out += [Finding("info", name, text) for text in report["notes"]]
    return out, failed


INSPECT_ENV = "PW_BUDGETS_INSPECT"


def main():
    inspect = [Path(path).as_posix().strip("/") for path in os.environ.get(INSPECT_ENV, "").split()]
    scope = change_report.resolve(ROOT)
    if scope is None:
        print("Budgets: no change base. Budgets judge what a change touched since it left main; fetch `origin/main`, "
              f"or set {change_report.BASE_ENV} to the commit this change builds on.")
        return 2
    code, reports = run_suites(artifact_root(ROOT) / "budgets", change_set(scope, inspect))
    if 'maintainability' in reports:
        report = reports['maintainability']
        source = os.environ.get('GITHUB_SHA') or change_report.git(ROOT, 'rev-parse', 'HEAD').stdout.strip()
        tree = change_report.git(ROOT, 'rev-parse', 'HEAD^{tree}').stdout.strip()
        seal(report, source, tree, change['base'])
        (artifact_root(ROOT) / 'budgets' / 'reports' / 'maintainability.json').write_text(json.dumps(report) + '\n')
    findings, failed = [], code != 0
    for name in SUITES:
        if name not in reports:
            findings.append(Finding("error", name, "the suite wrote no report; read the test output above"))
            continue
        suite, suite_failed = suite_findings(name, reports[name])
        findings += suite
        failed = failed or suite_failed
    header = scope.describe() + (f"; inspecting {', '.join(inspect)}" if inspect else "")
    change_report.emit(ROOT, "Budgets", header, findings, failed)
    if failed:
        return 1
    return 2 if len(reports) < len(SUITES) else 0


if __name__ == "__main__":
    sys.exit(main())
