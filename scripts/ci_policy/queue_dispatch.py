"""Recover suppressed merge-group events without replacing existing verification."""
import os
from urllib.parse import urlencode

from .github import api

PREFIX = 'refs/heads/gh-readonly-queue/main/'


def entries(repository):
    owner, name = repository.split('/')
    result = api('graphql', 'POST', {'query': '''query($owner:String!,$name:String!){
      repository(owner:$owner,name:$name){mergeQueue{entries(first:100){nodes{
      state baseCommit{oid} headCommit{oid}}}}}}''', 'variables': {'owner': owner, 'name': name}})
    if result.get('errors'):
        raise ValueError(str(result['errors']))
    return result['data']['repository']['mergeQueue']['entries']['nodes']


def context(repository, ref, sha):
    if not ref.startswith(PREFIX):
        raise ValueError('integration dispatch requires a live merge-queue ref')
    current = api(f'repos/{repository}/git/ref/{ref.removeprefix("refs/")}')
    if current['object']['sha'] != sha:
        raise ValueError('merge-queue ref no longer identifies this exact commit')
    for entry in entries(repository):
        if entry['state'] == 'AWAITING_CHECKS' and (entry.get('headCommit') or {}).get('oid') == sha:
            return entry['baseCommit']['oid']
    raise ValueError('commit is no longer an active merge group')


def certify(repository, ref, sha):
    statuses = api(f'repos/{repository}/commits/{sha}/status')['statuses']
    if any(row['context'] == 'DCO-owned' for row in statuses):
        return
    runs = api(f'repos/{repository}/actions/workflows/dco.yml/runs?event=workflow_dispatch&per_page=100')['workflow_runs']
    if any(row.get('display_title') == f'DCO queue {sha}' for row in runs):
        return
    context(repository, ref, sha)
    api(f'repos/{repository}/actions/workflows/dco.yml/dispatches', 'POST',
        {'ref': 'main', 'inputs': {'queue_ref': ref, 'queue_sha': sha}})


def recover(repository):
    active = {row['headCommit']['oid'] for row in entries(repository)
              if row['state'] == 'AWAITING_CHECKS' and row.get('headCommit')}
    if not active:
        return []
    refs = api(f'repos/{repository}/git/matching-refs/heads/gh-readonly-queue/main/')
    dispatched = []
    for ref in refs:
        sha = ref['object']['sha']
        if sha not in active:
            continue
        certify(repository, ref['ref'], sha)
        branch = ref['ref'].removeprefix('refs/heads/')
        query = urlencode({'head_sha': sha, 'branch': branch, 'per_page': 100})
        runs = api(f'repos/{repository}/actions/workflows/ci.yml/runs?{query}')['workflow_runs']
        # Failed or cancelled verification needs diagnosis, never a fresh green replacement.
        if runs:
            continue
        context(repository, ref['ref'], sha)
        api(f'repos/{repository}/actions/workflows/ci.yml/dispatches', 'POST',
            {'ref': branch, 'inputs': {'profile': 'integration'}})
        dispatched.append({'ref': branch, 'sha': sha})
    return dispatched


if __name__ == '__main__':
    print(context(os.environ['GITHUB_REPOSITORY'], os.environ['GITHUB_REF'], os.environ['GITHUB_SHA']))
