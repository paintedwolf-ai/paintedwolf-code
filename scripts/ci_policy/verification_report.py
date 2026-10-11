"""Receipt interpretation and hosted failure summaries."""
import json
import os
from pathlib import Path
import re
import time

from artifact_paths import artifact_root
from ci_policy.evidence import failure_signature

ROOT = Path(__file__).resolve().parents[2]
EXCERPT_LINES = 60
GO_PROGRESS = re.compile(r"^=== (RUN|PAUSE|CONT|NAME)\b|^\s*--- (PASS|SKIP):")


def failures(root):
    """Stages and packages this job's receipts did not pass, with the tests a digest named."""
    found = []
    for receipt in sorted((root / "verification").glob("*/[0-9]*.json")):
        if receipt.name.endswith(".progress.json"):
            continue
        record = json.loads(receipt.read_text())
        if record.get("status") == "passed":
            continue
        stages = [{"stage": evidence["stage"], "subject": subject, "status": result.get("status"), "exit_code": result.get("exit_code"),
                   "tests": result.get("tests", []), "output": result.get("output"), "log": result.get("log"), "resource_limit": result.get("resource_limit")}
                  for evidence in record.get("evidence", [])
                  for subject, result in evidence.get("results", {}).items() if result.get("status") != "passed"]
        # A run stopped before any stage reported, such as a cancellation, has only the receipt's reason.
        found += stages or [{"stage": ", ".join(record.get("names", [])), "subject": "", "status": record.get("status"),
                             "tests": [], "output": None, "log": None, "reason": record.get("reason")}]
    return found



def excerpt(failure):
    """The digest's failure output, or the end of the stage log when no digest isolated one."""
    for key, tail in (("output", False), ("log", True)):
        path = failure.get(key)
        if path and Path(path).is_file():
            lines = [line for line in Path(path).read_text(errors="replace").splitlines() if not GO_PROGRESS.match(line)]
            return lines[-EXCERPT_LINES:] if tail else lines[:EXCERPT_LINES]
    return []



def named_subject(failure):
    """The failing package, or nothing for a stage that reports as a single task."""
    return failure["subject"] if failure["subject"] not in {"", "task"} else ""



def annotation(failure):
    """A workflow error the pull request's checks show without opening the log."""
    message = ": ".join(part for part in (named_subject(failure), ", ".join(failure["tests"])) if part)
    message = message or failure.get("reason") or "see the job summary"
    return f"::error title={failure['stage']} {failure['status']}::{message}".replace("\n", "%0A")



def summary_lines(failure):
    subject = f" · `{named_subject(failure)}`" if named_subject(failure) else ""
    tests = ", ".join(f"`{test}`" for test in failure["tests"])
    reason = f" ({failure['reason']})" if failure.get("reason") else ""
    heading = f"- **{failure['status']}** `{failure['stage']}`{subject}" + (f": {tests}" if tests else "") + reason
    body = excerpt(failure)
    if not body:
        return [heading]
    return [heading, "  <details><summary>Output</summary>", "", "  ```text",
            *(f"  {line}" for line in body), "  ```", "  </details>"]



def report(status):
    root = artifact_root(ROOT)
    path = root / "ci" / "run.json"
    record = json.loads(path.read_text()) if path.exists() else {}
    found = failures(root)
    for failure in found:
        failure["signature"] = failure_signature(failure)
    (root / "ci").mkdir(parents=True, exist_ok=True)
    (root / "ci" / "failures.json").write_text(json.dumps(found, indent=2) + "\n")
    lines = [f"### Verification: {status}", ""]
    if record:
        elapsed = (record.get("finished_at", time.time()) - record["started_at"]) / 60
        lines += [f"Targets: `{', '.join(record['targets'])}`", "", f"Elapsed: {elapsed:.1f} minutes.", ""]
        if record["status"] == "running":
            lines += ["The task invocation did not return a verdict; inspect the retained evidence.", ""]
    else:
        lines += ["No catalog task invocation was recorded; inspect the setup or custom job steps.", ""]
    found = failures(root)
    if found:
        lines += ["#### Not passed", ""]
        for failure in found:
            lines += summary_lines(failure)
            print("\n".join([annotation(failure), *excerpt(failure), ""]), flush=True)
        lines.append("")
    lines += ["Receipts, stage logs, and available reports are retained with this job's artifacts.", ""]
    with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a") as output:
        output.write("\n".join(lines))

