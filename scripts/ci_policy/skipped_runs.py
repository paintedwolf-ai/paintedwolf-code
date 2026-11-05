"""Delete the skipped CI runs a draft push leaves on a head that turned ready.

A push and a ready-for-review event on one commit start two CI runs. GitHub reads
a required check from the newest check suite, so a skipped run that registers last
hides a passed check from the merge queue until that run is deleted."""
import subprocess

from ci_policy.github import api, pages

WORKFLOW = '.github/workflows/ci.yml'
FAILURES = (subprocess.SubprocessError, OSError, ValueError, KeyError)


def selected(runs, head, current):
    return [run for run in runs if run['id'] != current and run['head_sha'] == head and run['path'] == WORKFLOW
            and run['event'] == 'pull_request' and run['status'] == 'completed' and run['conclusion'] == 'skipped']


def diagnostic(error):
    return ' '.join(str(getattr(error, 'stderr', None) or error).split())


def delete_skipped_runs(repository, head, current):
    """Return one diagnostic per failure; the check this protects must still report its own verdict."""
    query = f'repos/{repository}/actions/runs?event=pull_request&status=completed&head_sha={head}'
    try:
        runs = selected(pages(query, 'workflow_runs'), head, current)
    except FAILURES as error:
        return [f'could not list CI runs for {head}: {diagnostic(error)}']
    failures = []
    for run in runs:
        try:
            api(f'repos/{repository}/actions/runs/{run["id"]}', 'DELETE')
        except FAILURES as error:
            failures.append(f'could not delete skipped CI run {run["id"]}: {diagnostic(error)}')
    return failures
