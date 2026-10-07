"""Size budgets for prompts and code, reported against the change being made.

Runs the budget suites (Go tests tagged `budgets`), then reports: every
artifact that grew past a chosen line, warnings and grandfathered standing for
the artifacts the change touched, and cleanup the policy could absorb.
"""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

from artifact_paths import artifact_root
import change_report
from change_report import Finding

ROOT = Path(__file__).resolve().parent.parent
SUITES = {
    "maintainability": ("./test/contract/maintainability", "TestMaintainabilityWithinBudget"),
    "prompts": ("./test/contract/agentcontext", "TestRenderedPromptsWithinBudget"),
}
FAILING = {"over_limit", "over_cap", "grandfather_raised"}
CLEANUP = {"slack", "unneeded", "vanished"}


def run_suites(report_dir, scope):
    if report_dir.exists():
        shutil.rmtree(report_dir)
    report_dir.mkdir(parents=True)
    env = {**os.environ, "PW_BUDGET_REPORT_DIR": str(report_dir)}
    if scope is not None:
        env[change_report.BASE_ENV] = scope.base
    tests = "|".join(test for _, test in SUITES.values())
    # Fresh runs: the suites read git state that the test cache does not key on.
    command = ["bash", str(ROOT / "scripts" / "go-test-digest.sh"), "--tags", "budgets", "--name", "budgets", "--",
               "-count=1", "-run", f"^({tests})$", *(package for package, _ in SUITES.values())]
    return subprocess.call(command, cwd=ROOT / "lycaon", env=env)


def load_reports(report_dir):
    return {name: json.loads((report_dir / f"{name}.json").read_text())
            for name in SUITES if (report_dir / f"{name}.json").exists()}


def size(report, category, value):
    return f'{value:,} {report["categories"][category]["unit"]}'


def suite_findings(name, report, scope):
    """Findings for one suite report, and whether any of them fails."""
    sources = {(a["category"], a["id"]): a["sources"] for a in report["artifacts"]}

    def source(category, artifact):
        paths = sources.get((category, artifact)) or []
        return paths[0] if paths else None

    out, failed, cleanup = [], False, 0
    for f in report["findings"]:
        kind, category, artifact = f["kind"], f["category"], f["id"]
        label = f"{category} {artifact}"
        if kind == "grandfather_raised":
            failed = True
            out.append(Finding("error", name, f'{label}: grandfathered cap raised to {f["measured"]:,} from '
                               f'{f["bound"]:,}; grandfathered caps only shrink, so move it to exceptions with a reason',
                               report["policy"]))
        elif kind in FAILING:
            failed = True
            bound = "limit" if not f.get("entry") else f'{f["entry"]} cap'
            out.append(Finding("error", name, f'{label}: {size(report, category, f["measured"])}, over its {bound} of '
                               f'{f["bound"]:,} by {f["measured"] - f["bound"]:,}', source(category, artifact)))
        elif kind in CLEANUP:
            cleanup += 1
        elif kind == "over_warn" and scope is not None and scope.touches(sources.get((category, artifact), [])):
            limit = report["categories"][category]["limit"]
            out.append(Finding("warning", name, f'{label}: {size(report, category, f["measured"])}, past the warning '
                               f'line of {f["bound"]:,} (limit {limit:,}); this change touched it, so weigh its shape now',
                               source(category, artifact)))
    if scope is not None:
        for a in report["artifacts"]:
            if a.get("entry") and a["measured"] <= a["cap"] and scope.touches(a["sources"]):
                limit = report["categories"][a["category"]]["limit"]
                out.append(Finding("notice", name, f'{a["category"]} {a["id"]}: {size(report, a["category"], a["measured"])}, '
                                   f'{a["entry"]} at {a["cap"]:,} over a limit of {limit:,}; this change touched it',
                                   a["sources"][0] if a["sources"] else None))
    if cleanup:
        entries = "1 entry" if cleanup == 1 else f"{cleanup} entries"
        out.append(Finding("notice", name, f"{entries} can be tightened, because the artifact shrank, fits its limit, or "
                           f"is gone; `{report['refresh']}` tightens them", report["policy"]))
    out += [Finding("info", name, note) for note in report["notes"]]
    return out, failed


def main():
    report_dir = artifact_root(ROOT) / "budgets"
    scope = change_report.resolve(ROOT)
    code = run_suites(report_dir, scope)
    reports = load_reports(report_dir)
    findings, failed = [], code != 0
    for name in SUITES:
        if name not in reports:
            findings.append(Finding("error", name, "the suite wrote no report; read the test output above"))
            continue
        suite, suite_failed = suite_findings(name, reports[name], scope)
        findings += suite
        failed = failed or suite_failed
    header = scope.describe() if scope else "no main branch is reachable, so only failures are reported"
    change_report.emit(ROOT, "Budgets", header, findings, failed)
    if failed:
        return 1
    return 2 if len(reports) < len(SUITES) else 0


if __name__ == "__main__":
    sys.exit(main())
