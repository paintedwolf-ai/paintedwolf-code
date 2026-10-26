"""Bring PRs that were based on a landed PR's branch up to date with main.

GitHub's update-branch merges main into each dependent on its own side, bound to
the head read here: a concurrent push is never overwritten and no commit is
rewritten. A conflict is labelled for the PR's owner instead of resolved."""
import json
import os
import subprocess
import time

from ci_policy.github import api

LABEL = 'update-conflict'
POLLS = 30
CONFLICT_POLLS = 12
INTERVAL = 5

OPEN_PULLS = '''query($owner:String!,$name:String!,$cursor:String){repository(owner:$owner,name:$name){
  pullRequests(states:OPEN,first:100,after:$cursor){pageInfo{hasNextPage endCursor}
  nodes{number headRefOid headRefName baseRefName isDraft headRepository{nameWithOwner} mergeQueueEntry{id}
  timelineItems(itemTypes:[BASE_REF_CHANGED_EVENT],last:1){nodes{... on BaseRefChangedEvent{previousRefName currentRefName createdAt}}}}}}}'''


def open_pulls(repository):
    owner, name = repository.split('/')
    cursor = None
    while True:
        result = api('graphql', 'POST', {'query': OPEN_PULLS, 'variables': {'owner': owner, 'name': name, 'cursor': cursor}})
        if result.get('errors'):
            raise ValueError(str(result['errors']))
        page = result['data']['repository']['pullRequests']
        yield from page['nodes']
        if not page['pageInfo']['hasNextPage']:
            return
        cursor = page['pageInfo']['endCursor']


def dependent(pull, landed, repository):
    """A dependent still targets the landed branch, or GitHub moved it from there to main after that PR opened."""
    if (pull['headRepository'] or {}).get('nameWithOwner') != repository or pull['mergeQueueEntry']:
        return False
    if pull['baseRefName'] == landed['head']['ref']:
        return True
    moved = pull['timelineItems']['nodes']
    return (pull['baseRefName'] == 'main' and bool(moved) and moved[0]['previousRefName'] == landed['head']['ref']
            and moved[0]['currentRefName'] == 'main' and moved[0]['createdAt'] >= landed['created_at'])


def behind(repository, head):
    return api(f'repos/{repository}/compare/main...{head}')['behind_by']


def refused(repository, number, head, sleep):
    """Explain a refused update from the PR's own state, never the error text."""
    # GitHub computes mergeability in the background after a base change.
    for _ in range(CONFLICT_POLLS):
        pull = api(f'repos/{repository}/pulls/{number}')
        if pull['head']['sha'] != head:
            return 'superseded'
        if pull['mergeable'] is not None:
            return 'conflict' if pull['mergeable'] is False else 'unexplained'
        sleep(INTERVAL)
    return 'unexplained'


def request(repository, pull, landed, sleep):
    number, head = pull['number'], pull['headRefOid']
    if pull['baseRefName'] != 'main':
        try:
            api(f'repos/{repository}/pulls/{number}', 'PATCH', {'base': 'main'})
        except subprocess.CalledProcessError:
            if api(f'repos/{repository}/pulls/{number}')['base']['ref'] != 'main':
                raise
    if behind(repository, head) == 0:
        return 'current'
    try:
        api(f'repos/{repository}/pulls/{number}/update-branch', 'PUT', {'expected_head_sha': head})
        return 'requested'
    except subprocess.CalledProcessError:
        outcome = refused(repository, number, head, sleep)
        if outcome == 'unexplained':
            raise
        if outcome == 'conflict':
            api(f'repos/{repository}/issues/{number}/labels', 'POST', {'labels': [LABEL]})
            api(f'repos/{repository}/issues/{number}/comments', 'POST', {'body': (
                f'#{landed["number"]} landed on `main`, so this PR now targets `main`. Updating this branch from `main` '
                f'stopped on a merge conflict. Merge `main` into this branch, resolve the conflict and push; '
                f'the `{LABEL}` label clears on that push.')})
        return outcome


def dispatch(repository, pull):
    # Merges made with the Actions token start no workflows; dispatch is the supported exception.
    api(f'repos/{repository}/actions/workflows/ci.yml/dispatches', 'POST',
        {'ref': pull['head']['ref'], 'inputs': {'profile': 'fast'}})
    api(f'repos/{repository}/actions/workflows/dco.yml/dispatches', 'POST',
        {'ref': 'main', 'inputs': {'pull_request': str(pull['number'])}})


def settle(repository, requested, sleep):
    """Wait for each requested merge to land on its branch, then start its checks."""
    waiting = dict(requested)
    outcomes = {}
    for _ in range(POLLS):
        for number, head in list(waiting.items()):
            pull = api(f'repos/{repository}/pulls/{number}')
            if pull['head']['sha'] == head:
                continue
            del waiting[number]
            if pull['draft']:
                outcomes[number] = 'updated'
                continue
            dispatch(repository, pull)
            outcomes[number] = 'updated_and_checked'
        if not waiting:
            break
        sleep(INTERVAL)
    outcomes.update({number: 'unconfirmed' for number in waiting})
    return outcomes


def update_dependents(repository, event, sleep=time.sleep):
    pull = event['pull_request']
    if not pull['merged'] or pull['base']['ref'] != 'main' or (pull['head']['repo'] or {}).get('full_name') != repository:
        return {'state': 'not_applicable'}
    outcomes, requested, failures = {}, {}, []
    for candidate in open_pulls(repository):
        if not dependent(candidate, pull, repository):
            continue
        try:
            outcome = request(repository, candidate, pull, sleep)
        except subprocess.CalledProcessError as error:
            failures.append(candidate['number'])
            print(f'#{candidate["number"]}: {error.stderr}')
            continue
        if outcome == 'requested':
            requested[candidate['number']] = candidate['headRefOid']
        else:
            outcomes[candidate['number']] = outcome
    outcomes.update(settle(repository, requested, sleep))
    record = {'state': 'reconciled', 'landed': pull['number'], 'outcomes': {str(n): o for n, o in sorted(outcomes.items())},
              'failed': failures}
    if failures or 'unconfirmed' in outcomes.values():
        record['state'] = 'incomplete'
    return record


def clear_conflict(repository, event):
    pull = event['pull_request']
    if LABEL not in {label['name'] for label in pull['labels']}:
        return {'state': 'not_applicable'}
    api(f'repos/{repository}/issues/{pull["number"]}/labels/{LABEL}', 'DELETE')
    return {'state': 'cleared', 'pull_request': pull['number']}


def reconcile(environment, event):
    repository = environment['GITHUB_REPOSITORY']
    if environment['GITHUB_EVENT_NAME'] != 'pull_request_target':
        raise ValueError('dependent updates run from pull request events')
    if event['action'] == 'closed':
        return update_dependents(repository, event)
    if event['action'] == 'synchronize':
        return clear_conflict(repository, event)
    return {'state': 'not_applicable'}


def main():
    with open(os.environ['GITHUB_EVENT_PATH']) as handle:
        record = reconcile(os.environ, json.load(handle))
    with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as summary:
        summary.write('### Dependent PR update\n\n```json\n' + json.dumps(record, indent=2) + '\n```\n')
    print(json.dumps(record))
    if record['state'] == 'incomplete':
        raise SystemExit(1)


if __name__ == '__main__':
    main()
