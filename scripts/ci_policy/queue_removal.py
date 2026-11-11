"""Tell a PR's owner which lanes failed when a merge group removes it from the queue."""
import json
import os

from ci_policy.github import api, pages


def failed_group(repository, number):
    """The newest merge group run for this PR, when it has failed jobs."""
    prefix = f'gh-readonly-queue/main/pr-{number}-'
    runs = api(f'repos/{repository}/actions/workflows/ci.yml/runs?event=merge_group&per_page=100')['workflow_runs']
    for run in runs:
        if run['head_branch'].startswith(prefix):
            jobs = pages(f'repos/{repository}/actions/runs/{run["id"]}/jobs', 'jobs')
            return run, sorted(job['name'] for job in jobs if job['conclusion'] == 'failure')
    return None, []


def notify(repository, number):
    run, failed = failed_group(repository, number)
    # A manual removal or batch withdrawal has no failed job to report.
    if not failed:
        return False
    names = ', '.join(f'`{name}`' for name in failed)
    api(f'repos/{repository}/issues/{number}/comments', 'POST', {'body': (
        f'Removed from the merge queue: {names} failed in [this run]({run["html_url"]}). '
        'Fix it and add the PR to the queue again.')})
    return True


if __name__ == '__main__':
    with open(os.environ['GITHUB_EVENT_PATH'], encoding='utf-8') as event:
        notify(os.environ['GITHUB_REPOSITORY'], json.load(event)['pull_request']['number'])
