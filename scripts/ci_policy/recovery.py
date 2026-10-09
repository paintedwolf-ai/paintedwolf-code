"""Recover proven runner faults and propose attributable main reversions.

This runs only from the default branch. Artifacts are bounded JSON data, never
executed or extracted; fork code never runs with this workflow's write token.
"""
import io
import json
import os
from pathlib import Path
import re
import subprocess
import zipfile

from .github import api, pages, repository, ensure_issue
from .evidence import retryable

MAX_ARTIFACT_BYTES = 32 * 1024 * 1024


def evidence(run):
    records, failures, debt = [], [], []
    for artifact in pages(f"{repository()}/actions/runs/{run['id']}/artifacts", 'artifacts'):
        if not artifact['name'].startswith('receipt-'):
            continue
        if artifact['expired'] or artifact['size_in_bytes'] > MAX_ARTIFACT_BYTES:
            continue
        if not artifact['name'].endswith('-' + str(run['run_attempt'])):
            continue
        raw = subprocess.check_output(['gh', 'api', f"{repository()}/actions/artifacts/{artifact['id']}/zip"])
        if len(raw) > MAX_ARTIFACT_BYTES:
            continue
        with zipfile.ZipFile(io.BytesIO(raw)) as archive:
            for member in archive.infolist():
                if member.file_size > 1024 * 1024:
                    continue
                path = member.filename
                if not path.endswith(('ci/run.json', 'ci/failures.json', 'reports/maintainability.json')):
                    continue
                value = json.loads(archive.read(member))
                if path.endswith('ci/run.json') and isinstance(value, dict):
                    records.append(value)
                elif path.endswith('ci/failures.json') and isinstance(value, list):
                    failures.extend(value)
                elif isinstance(value, dict):
                    debt.extend(f for f in value.get('findings', []) if f.get('kind') == 'legacy_debt')
    return records, failures, debt


def qualified(sha):
    return any(r['head_sha'] == sha and r['head_branch'] == 'main' and r['conclusion'] == 'success'
               and r['event'] in {'push', 'workflow_dispatch'}
               for r in pages(f'{repository()}/actions/workflows/qualification.yml/runs?head_sha={sha}', 'workflow_runs'))


def revert_candidate(run, commit, pulls, parent_qualified, main):
    if (run.get('path') != '.github/workflows/qualification.yml' or run.get('event') != 'push'
            or run.get('head_branch') != 'main' or run.get('conclusion') != 'failure'
            or main != run.get('head_sha') or len(commit.get('parents', [])) != 1 or not parent_qualified):
        return None
    matches = [p for p in pulls if p.get('merged_at') and p.get('base', {}).get('ref') == 'main'
               and p.get('merge_commit_sha') == run['head_sha']]
    return matches[0] if len(matches) == 1 else None


def propose_revert(run, failures):
    sha = run['head_sha']
    if not re.fullmatch('[0-9a-f]{40}', sha):
        raise ValueError('invalid failed commit identity')
    commit = api(f'{repository()}/commits/{sha}')
    pulls = list(pages(f'{repository()}/commits/{sha}/pulls'))
    main = api(f'{repository()}/git/ref/heads/main')['object']['sha']
    parent = commit.get('parents', [])
    candidate = revert_candidate(run, commit, pulls, len(parent) == 1 and qualified(parent[0]['sha']), main)
    description = '\n'.join(f"- `{f.get('stage', '')}` / `{f.get('subject', '')}`: {', '.join(f.get('tests', []))}"
                            for f in failures[:30])
    issue = ensure_issue(f'Qualification failed at {sha[:12]}',
                         f"[Qualification evidence]({run['html_url']})\n\n{description}\n\n"
                         + (f"Candidate introducing PR: #{candidate['number']}. Its parent passed qualification. "
                            'The revert is a proposal for review, not proof that every failure is caused by this change.'
                            if candidate else 'Attribution is ambiguous or main has advanced; investigate before reverting.'))
    if not candidate or not any(f.get("status") == "failed" for f in failures):
        return
    branch = 'automation/revert-' + sha[:12]
    existing = list(pages(f"{repository()}/pulls?state=all&head={os.environ['GITHUB_REPOSITORY'].split('/')[0]}:{branch}"))
    if existing:
        return
    # A new hosted checkout is disposable. Never rewrite an existing remote branch.
    subprocess.run(['git', 'switch', '-c', branch, sha], check=True)
    result = subprocess.run(['git', 'revert', '--no-commit', sha], capture_output=True, text=True)
    if result.returncode:
        print('Revert conflicts; the incident issue requires manual resolution.')
        return
    subprocess.run(['git', '-c', 'user.name=github-actions[bot]', '-c',
                    'user.email=41898282+github-actions[bot]@users.noreply.github.com',
                    'commit', '-s', '-m', f"Revert #{candidate['number']} after qualification failure"], check=True)
    subprocess.run(['git', 'push', 'origin', 'HEAD:refs/heads/' + branch], check=True)
    api(f'{repository()}/pulls', 'POST', {'title': f"Revert #{candidate['number']} after qualification failure",
        'head': branch, 'base': 'main', 'draft': True,
        'body': f"Proposed recovery for {issue['html_url']}. Review failure attribution and mark ready to run admission."})


def recover(run):
    if run.get('path') not in {'.github/workflows/ci.yml', '.github/workflows/qualification.yml'}:
        return
    if run.get('repository', {}).get('full_name', os.environ['GITHUB_REPOSITORY']) != os.environ['GITHUB_REPOSITORY']:
        return
    # Fork artifacts cannot authorize privileged recovery and need not be downloaded.
    if run.get('event') == 'pull_request':
        return
    records, failures, debt = evidence(run)
    for item in debt:
        ensure_issue(f"Maintainability debt: {item['category']} {item['id']}",
                     f"Measured {item['measured']}; limit {item['bound']}. "
                     f"[Evidence]({run['html_url']}). Reduce this artifact along cohesive domain boundaries.")
    if run.get('conclusion') != 'failure':
        return
    jobs = list(pages(f"{repository()}/actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs", 'jobs'))
    # Every failed executable lane must have evidence. Missing evidence cannot prove a runner fault.
    failed_jobs = [j for j in jobs if j['conclusion'] in {'failure', 'timed_out'} and j['name'] not in {'check', 'qualification'}]
    if len(failed_jobs) == len([r for r in records if r.get('classification') != 'passed']) and retryable(records, run['run_attempt']):
        api(f"{repository()}/actions/runs/{run['id']}/rerun-failed-jobs", 'POST')
        return
    if run.get('path') == '.github/workflows/qualification.yml':
        propose_revert(run, failures)
    else:
        ensure_issue(f"Integration failed: run {run['id']}", f"[Evidence]({run['html_url']}). "
                     'The queue rejected this combined commit. Test failures are not retried.')


def main():
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
    recover(api(f"{repository()}/actions/runs/{event['workflow_run']['id']}"))


if __name__ == '__main__':
    main()
