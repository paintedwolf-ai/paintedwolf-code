"""Required-job and exact-commit release qualification verdicts."""
from datetime import datetime, timedelta, timezone
from urllib.parse import urlencode
import re

from ci_policy.github import pages


def require_success(results, skipped=(), draft=False):
    """Every job passed, except those the caller's tier skips on purpose, which must not have run."""
    if draft:
        raise ValueError("draft pull requests are not verified; mark the pull request ready for review to run its checks")
    if not isinstance(results, dict) or not results:
        raise ValueError("required-job results are missing")
    unknown = set(skipped) - set(results)
    if unknown:
        raise ValueError("jobs expected to be skipped are not required: " + ", ".join(sorted(unknown)))
    failed = [f"{name}: {value.get('result', 'missing')}" for name, value in results.items()
              if value.get("result") != ("skipped" if name in skipped else "success")]
    if failed:
        raise ValueError("required verification did not pass: " + ", ".join(failed))


def qualified_run(repository, sha):
    from ci_policy import qualification_receipt
    if not isinstance(sha, str) or not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("qualification requires a full source commit SHA")
    retained_since = (datetime.now(timezone.utc) - timedelta(days=90)).strftime("%Y-%m-%dT%H:%M:%SZ")

    def accepts(run):
        if (run.get("head_branch") != "main" or run.get("conclusion") != "success"
                or run.get("event") != "workflow_dispatch"):
            return False
        if not qualification_receipt.aggregate_passed(repository, run):
            return False
        receipt = qualification_receipt.from_run(repository, run)
        return receipt is not None and qualification_receipt.matches(receipt, repository, sha, run)

    query = urlencode({"event": "workflow_dispatch", "branch": "main", "status": "success",
                       "created": ">=" + retained_since})
    path = f"repos/{repository}/actions/workflows/qualification.yml/runs?{query}"
    for run in pages(path, "workflow_runs"):
        if run.get("display_title") == f"Qualification {sha}" and accepts(run):
            return run
    return None


def require_qualification(repository, sha):
    """Release eligibility requires completed qualification of the selected source."""
    run = qualified_run(repository, sha)
    if run is None:
        raise ValueError(f"{sha} has no passing exact-commit qualification; qualify it before releasing")
    return run
