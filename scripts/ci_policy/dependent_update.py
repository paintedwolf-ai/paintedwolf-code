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
ATTEMPTS = 3
CONFLICT_POLLS = 12
HELD_POLLS = 6
INTERVAL = 5
BOT = 'github-actions[bot]'

OPEN_PULLS = '''query($owner:String!,$name:String!,$cursor:String){repository(owner:$owner,name:$name){
  pullRequests(states:OPEN,first:100,after:$cursor){pageInfo{hasNextPage endCursor}
  nodes{number headRefOid headRefName baseRefName createdAt updatedAt isDraft headRepository{nameWithOwner} mergeQueueEntry{id}
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
    if pull['baseRefName'] != 'main' or not moved:
        return False
    change = moved[0]
    if change['previousRefName'] == landed['head']['ref'] and change['currentRefName'] == 'main':
        return change['createdAt'] >= landed['created_at']
    # Silent retargeting leaves the latest explicit move onto the dependency.
    # That event identifies older PRs even before they inherit its commits.
    return (change['currentRefName'] == landed['head']['ref']
            and landed['created_at'] <= change['createdAt'] <= landed['merged_at'])


def auto_retarget_candidate(pull, landed, repository):
    """GitHub may silently retarget a child when its base branch lands."""
    return ((pull['headRepository'] or {}).get('nameWithOwner') == repository and not pull['mergeQueueEntry']
            and pull['baseRefName'] == 'main' and landed['created_at'] <= pull['createdAt'] <= landed['merged_at']
            and pull['updatedAt'] >= landed['merged_at'])


def parent_commit(repository, landed):
    """Use a commit unique to the landed branch to identify inherited history."""
    comparison = api(f'repos/{repository}/compare/{landed["base"]["sha"]}...{landed["head"]["sha"]}?per_page=1')
    return comparison['commits'][0]['sha'] if comparison['ahead_by'] and comparison['commits'] else None


def inherits_commit(repository, commit, head):
    return api(f'repos/{repository}/compare/{commit}...{head}')['behind_by'] == 0


def behind(repository, head):
    return api(f'repos/{repository}/compare/main...{head}')['behind_by']


def refused(repository, number, head, sleep):
    """Explain a refused update from the PR's own state, never the error text."""
    # GitHub computes mergeability in the background after a base change.
    for _ in range(CONFLICT_POLLS):
        pull = api(f'repos/{repository}/pulls/{number}')
        if pull['head']['sha'] != head:
            return 'superseded', pull
        if pull['mergeable'] is not None:
            return ('conflict' if pull['mergeable'] is False else 'unexplained'), pull
        sleep(INTERVAL)
    return 'unexplained', pull


def request(repository, pull, landed, sleep):
    """Update one dependent from main, following its head when its owner pushes meanwhile."""
    number, head = pull['number'], pull['headRefOid']
    if pull['baseRefName'] != 'main':
        try:
            api(f'repos/{repository}/pulls/{number}', 'PATCH', {'base': 'main'})
        except subprocess.CalledProcessError:
            if api(f'repos/{repository}/pulls/{number}')['base']['ref'] != 'main':
                raise
    for _ in range(ATTEMPTS):
        if behind(repository, head) == 0:
            # A retarget starts no CI, so a current ready branch still needs its checks.
            current = api(f'repos/{repository}/pulls/{number}')
            if current['head']['sha'] != head:
                head = current['head']['sha']
                continue
            if current['draft']:
                return 'current', head
            dispatch(repository, current)
            return 'current_and_checked', head
        try:
            api(f'repos/{repository}/pulls/{number}/update-branch', 'PUT', {'expected_head_sha': head})
            return 'requested', head
        except subprocess.CalledProcessError as error:
            refusal = error
        outcome, current = refused(repository, number, head, sleep)
        if outcome == 'superseded':
            head = current['head']['sha']
            continue
        if outcome == 'unexplained':
            raise refusal
        api(f'repos/{repository}/issues/{number}/labels', 'POST', {'labels': [LABEL]})
        api(f'repos/{repository}/issues/{number}/comments', 'POST', {'body': (
            f'#{landed["number"]} landed on `main`, so this PR now targets `main`. Updating this branch from `main` '
            f'stopped on a merge conflict. Merge `main` into this branch, resolve the conflict and push; '
            f'the `{LABEL}` label clears on that push.')})
        return 'conflict', head
    return 'contended', head


def dispatch(repository, pull):
    # A merge by the Actions token starts no certification and only a CI run held
    # for approval; dispatch is the supported way to run both.
    api(f'repos/{repository}/actions/workflows/ci.yml/dispatches', 'POST',
        {'ref': pull['head']['ref'], 'inputs': {'profile': 'fast'}})
    api(f'repos/{repository}/actions/workflows/dco.yml/dispatches', 'POST',
        {'ref': 'main', 'inputs': {'pull_request': str(pull['number'])}})


def settle(repository, requested, landed, sleep):
    """Wait for each requested merge; follow owner pushes that still need main."""
    waiting = dict(requested)
    outcomes, merged = {}, {}
    retries = {number: 0 for number in requested}
    for _ in range(POLLS):
        for number, head in list(waiting.items()):
            pull = api(f'repos/{repository}/pulls/{number}')
            if pull['head']['sha'] == head:
                continue
            new_head = pull['head']['sha']
            if behind(repository, new_head):
                retries[number] += 1
                if retries[number] >= ATTEMPTS:
                    del waiting[number]
                    outcomes[number] = 'contended'
                    continue
                try:
                    outcome, current_head = request(repository, {'number': number, 'headRefOid': new_head,
                                                                 'baseRefName': 'main'}, landed, sleep)
                except subprocess.CalledProcessError:
                    del waiting[number]
                    outcomes[number] = 'unconfirmed'
                    continue
                if outcome == 'requested':
                    waiting[number] = current_head
                else:
                    del waiting[number]
                    outcomes[number] = outcome
                    if outcome == 'current_and_checked':
                        merged[number] = current_head
                continue
            del waiting[number]
            merged[number] = new_head
            if pull['draft']:
                outcomes[number] = 'updated'
                continue
            dispatch(repository, pull)
            outcomes[number] = 'updated_and_checked'
        if not waiting:
            break
        sleep(INTERVAL)
    outcomes.update({number: 'unconfirmed' for number in waiting})
    return outcomes, merged


def held(repository, head):
    runs = api(f'repos/{repository}/actions/runs?event=pull_request&status=action_required&head_sha={head}')['workflow_runs']
    return [run for run in runs if run['head_sha'] == head and run['path'] == '.github/workflows/ci.yml'
            and (run.get('triggering_actor') or {}).get('login') == BOT]


def release(repository, merged, sleep):
    """Delete the CI run GitHub holds for approval after each bot merge; dispatched checks replace it."""
    waiting = dict(merged)
    for _ in range(HELD_POLLS):
        for number, head in list(waiting.items()):
            runs = held(repository, head)
            for run in runs:
                api(f'repos/{repository}/actions/runs/{run["id"]}', 'DELETE')
            if runs:
                del waiting[number]
        if not waiting:
            break
        sleep(INTERVAL)
    return sorted(waiting)


def update_dependents(repository, event, sleep=time.sleep):
    pull = event['pull_request']
    if not pull['merged'] or pull['base']['ref'] != 'main' or (pull['head']['repo'] or {}).get('full_name') != repository:
        return {'state': 'not_applicable'}
    outcomes, requested, failures = {}, {}, []
    anchor, anchor_checked = None, False
    for candidate in open_pulls(repository):
        if not dependent(candidate, pull, repository):
            if not auto_retarget_candidate(candidate, pull, repository):
                continue
            if not anchor_checked:
                anchor = parent_commit(repository, pull)
                anchor_checked = True
            if not anchor or not inherits_commit(repository, anchor, candidate['headRefOid']):
                continue
        try:
            outcome, head = request(repository, candidate, pull, sleep)
        except subprocess.CalledProcessError as error:
            failures.append(candidate['number'])
            print(f'#{candidate["number"]}: {error.stderr}')
            continue
        if outcome == 'requested':
            requested[candidate['number']] = head
        else:
            outcomes[candidate['number']] = outcome
    settled, merged = settle(repository, requested, pull, sleep)
    outcomes.update(settled)
    record = {'state': 'reconciled', 'landed': pull['number'], 'outcomes': {str(n): o for n, o in sorted(outcomes.items())},
              'failed': failures, 'held_runs_remaining': release(repository, merged, sleep)}
    if failures or {'unconfirmed', 'contended'} & set(outcomes.values()):
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
