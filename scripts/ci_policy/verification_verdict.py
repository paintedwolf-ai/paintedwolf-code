"""Required-job and exact-commit release qualification verdicts."""
from urllib.parse import urlencode

from ci_policy.github import api


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



def require_qualification(repository, sha):
    """Release eligibility comes from qualification of this exact commit."""
    query = urlencode({"head_sha": sha, "per_page": 100})
    runs = api(f"repos/{repository}/actions/workflows/qualification.yml/runs?{query}")["workflow_runs"]
    if not any(run["head_sha"] == sha and run["head_branch"] == "main"
               and run["event"] in {"push", "workflow_dispatch"}
               and run["conclusion"] == "success" for run in runs):
        raise ValueError(f"{sha} has no passing exact-commit qualification; qualify it before releasing")

