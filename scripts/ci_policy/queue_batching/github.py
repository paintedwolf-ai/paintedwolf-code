"""GitHub operations never execute the combined branch with coordinator credentials."""
from ci_policy.github import api, pages


def graphql(repository, query):
    owner, name = repository.split('/')
    result = api('graphql', 'POST', {'query': query, 'variables': {'owner': owner, 'name': name}})
    if result.get('errors'):
        raise ValueError(str(result['errors']))
    return result['data']['repository']


def queue(repository):
    query = '''query($owner:String!,$name:String!){repository(owner:$owner,name:$name){
      mergeQueue{entries(first:100){nodes{state position pullRequest{
      id number headRefOid headRefName isDraft labels(first:100){nodes{name}}}}}}}}'''
    entries = graphql(repository, query)['mergeQueue']['entries']['nodes']
    for entry in entries:
        pull = entry['pullRequest']
        pull['labels'] = [label['name'] for label in pull['labels']['nodes']]
    return entries


def batches(repository):
    opened = list(pages(f'repos/{repository}/pulls?state=open'))
    closed = api(f'repos/{repository}/pulls?state=closed&sort=updated&direction=desc&per_page=100')
    return [p for p in [*opened, *closed]
            if p['head']['ref'].startswith('automation/queue-batch-')
            and p['user']['login'] == 'github-actions[bot]'
            and p['head']['repo'] and p['head']['repo']['full_name'] == repository
            and p['base']['ref'] == 'main']


def mutation(operation, input):
    query = 'mutation($input:' + operation[0].upper() + operation[1:] + 'Input!){' + operation + '(input:$input){clientMutationId}}'
    result = api('graphql', 'POST', {'query': query, 'variables': {'input': input}})
    if result.get('errors'):
        raise ValueError(str(result['errors']))


def restore(repository, row):
    current = api(f'repos/{repository}/pulls/{row["number"]}')
    # Never promote a later head, a human draft decision, or a closed PR.
    if current.get('auto_merge'):
        return
    if current['state'] == 'open' and not current['draft'] and current['head']['sha'] == row['headRefOid']:
        from ci_policy.batch_prs import source
        if source(repository, row['number'])['mergeQueueEntry'] is not None:
            return
        mutation('enablePullRequestAutoMerge', {'pullRequestId': current['node_id'], 'mergeMethod': 'SQUASH'})


def checks(repository, sha):
    runs = list(pages(f'repos/{repository}/commits/{sha}/check-runs', 'check_runs'))
    relevant = [r for r in runs if r['name'] in {'check', 'DCO-owned'}]
    latest = {}
    for row in relevant:
        if row['name'] not in latest or row['id'] > latest[row['name']]['id']:
            latest[row['name']] = row
    if any(r['status'] == 'completed' and r['conclusion'] not in {'success', 'neutral', 'skipped'} for r in latest.values()):
        return 'failed'
    statuses = api(f'repos/{repository}/commits/{sha}/status')['statuses']
    dco = next((r['state'] for r in statuses if r['context'] == 'DCO-owned'), None)
    if dco == 'failure':
        return 'failed'
    return 'passed' if latest.get('check', {}).get('conclusion') == 'success' and dco == 'success' else 'pending'


def blocked(pull):
    return any(label['name'] == 'no-combine' for label in pull.get('labels', []))


def withdraw(repository, row):
    from ci_policy.batch_prs import source, withdraw as remove
    current = source(repository, row['number'])
    if 'headRefOid' in row and current['headRefOid'] != row['headRefOid']:
        raise ValueError('source changed before withdrawal')
    remove(repository, current)


def enforce_holds(repository):
    # A no-combine label revokes batch participation, not individual admission.
    held = set()
    for pull in pages(f'repos/{repository}/pulls?state=open'):
        if blocked(pull):
            held.add(pull['number'])
    if held:
        from .selection import manifest
        for pull in batches(repository):
            record = manifest(pull['body'])
            if pull['state'] == 'open' and record and (pull['number'] in held or any(r['number'] in held for r in record['sources'])):
                from .coordinator import dissolve
                dissolve(repository, pull, record, 'no_combine')


def withdraw_drafts(repository):
    """Retire existing draft admission before any merge-group recovery."""
    from ci_policy.batch_prs import source, withdraw as remove
    withdrawn = []
    for entry in queue(repository):
        recorded = entry['pullRequest']
        if not recorded['isDraft']:
            continue
        current = source(repository, recorded['number'])
        if not current['isDraft'] or current['headRefOid'] != recorded['headRefOid']:
            continue
        remove(repository, current)
        remaining = source(repository, recorded['number'])
        if remaining['mergeQueueEntry'] is not None or remaining['autoMergeRequest'] is not None:
            raise ValueError(f"draft PR #{recorded['number']} retains merge admission")
        withdrawn.append(recorded['number'])
    return withdrawn


def build_window(repository):
    result = graphql(repository, """query($owner:String!,$name:String!){repository(owner:$owner,name:$name){
      mergeQueue{configuration{maximumEntriesToBuild}}}}""")
    return result['mergeQueue']['configuration']['maximumEntriesToBuild']


def ejected(repository, number):
    events = [event for event in pages(f'repos/{repository}/issues/{number}/timeline')
              if event.get('event') in {'removed_from_merge_queue', 'added_to_merge_queue'}]
    return bool(events and events[-1]['event'] == 'removed_from_merge_queue')
