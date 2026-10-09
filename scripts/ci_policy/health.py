"""Daily queue latency, lane duration, and recurring structured failure signatures."""
from collections import defaultdict
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path

from .github import api, pages, repository, ensure_issue
from .recovery import evidence


def seconds(start, end):
    return (datetime.fromisoformat(end.replace('Z', '+00:00')) - datetime.fromisoformat(start.replace('Z', '+00:00'))).total_seconds()


def quantile(values, fraction):
    values = sorted(values)
    return values[min(len(values) - 1, int((len(values) - 1) * fraction))] if values else None


def queue_times(pulls):
    values = []
    for pull in pulls:
        entries = [e['createdAt'] for e in pull['timelineItems']['nodes'] if e['__typename'] == 'AddedToMergeQueueEvent']
        if entries and pull['mergedAt']:
            values.append({'pr': pull['number'], 'seconds': seconds(entries[-1], pull['mergedAt'])})
    return values


def recent_merges(since):
    owner, name = os.environ['GITHUB_REPOSITORY'].split('/')
    cursor, out = None, []
    while True:
        query = '''query($owner:String!,$name:String!,$cursor:String) {
          repository(owner:$owner,name:$name) { pullRequests(first:100,after:$cursor,states:MERGED,
          orderBy:{field:UPDATED_AT,direction:DESC}) { pageInfo {hasNextPage endCursor}
          nodes {number mergedAt updatedAt timelineItems(last:100,itemTypes:[ADDED_TO_MERGE_QUEUE_EVENT]) {
          nodes {__typename ... on AddedToMergeQueueEvent {createdAt}}}}}}}'''
        result = api('graphql', 'POST', {'query': query, 'variables': {'owner': owner, 'name': name, 'cursor': cursor}})
        data = result['data']['repository']['pullRequests']
        out += [p for p in data['nodes'] if p['mergedAt'] >= since]
        if not data['pageInfo']['hasNextPage'] or any(p['updatedAt'] < since for p in data['nodes']):
            return out
        cursor = data['pageInfo']['endCursor']


def summarize(runs, get_evidence):
    signatures, lanes = defaultdict(dict), defaultdict(list)
    for run in runs:
        records, failures, _ = get_evidence(run)
        for record in records:
            if 'finished_at' in record and 'started_at' in record:
                lanes[record.get('lane', 'unknown')].append(record['finished_at'] - record['started_at'])
        for failure in failures:
            signature = failure.get('signature')
            if isinstance(signature, str) and len(signature) == 20:
                signatures[signature][run['id']] = run['html_url']
    return {'lanes': {name: {'count': len(values), 'p50_seconds': quantile(values, .5), 'p95_seconds': quantile(values, .95)}
                      for name, values in lanes.items()},
            'recurring': {key: list(links.values()) for key, links in signatures.items() if len(links) >= 3}}


def main():
    since = (datetime.now(timezone.utc) - timedelta(days=1)).strftime('%Y-%m-%dT%H:%M:%SZ')
    runs = [r for r in pages(f'{repository()}/actions/workflows/ci.yml/runs?created=>={since}', 'workflow_runs')
            if r['event'] == 'merge_group' and r['status'] == 'completed']
    report = summarize(runs, evidence)
    report['window_start'] = since
    report['queue_to_merge'] = queue_times(recent_merges(since))
    report['queue_p95_seconds'] = quantile([r['seconds'] for r in report['queue_to_merge']], .95)
    report['ejected_runs'] = [{'run': r['id'], 'conclusion': r['conclusion'], 'url': r['html_url']}
                              for r in runs if r['conclusion'] != 'success']
    for signature, urls in report['recurring'].items():
        ensure_issue('Recurring queue failure: ' + signature,
                     'This structured failure occurred in at least three distinct queue runs in 24 hours.\n\n'
                     + '\n'.join(f'- [Evidence]({url})' for url in urls))
    Path('queue-health.json').write_text(json.dumps(report, indent=2) + '\n')
    with Path(os.environ['GITHUB_STEP_SUMMARY']).open('a') as summary:
        summary.write('### Queue health\n\n```json\n' + json.dumps(report, indent=2) + '\n```\n')


if __name__ == '__main__':
    main()
