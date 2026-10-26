"""Daily queue latency, lane duration, and recurring structured failure signatures."""
from collections import defaultdict
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path

from .github import api, pages, repository
from .recovery import evidence
from . import job_metrics, delivery_metrics


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
          nodes {number baseRefName headRefName body author {login} mergedAt updatedAt timelineItems(last:100,itemTypes:[ADDED_TO_MERGE_QUEUE_EVENT]) {
          nodes {__typename ... on AddedToMergeQueueEvent {createdAt}}}}}}}'''
        result = api('graphql', 'POST', {'query': query, 'variables': {'owner': owner, 'name': name, 'cursor': cursor}})
        data = result['data']['repository']['pullRequests']
        out += [p for p in data['nodes'] if p['mergedAt'] >= since and p['baseRefName'] == 'main']
        if not data['pageInfo']['hasNextPage'] or any(p['updatedAt'] < since for p in data['nodes']):
            return out
        cursor = data['pageInfo']['endCursor']


def summarize(runs, get_evidence):
    signatures, lanes = defaultdict(dict), defaultdict(list)
    missing = []
    classifications = defaultdict(int)
    for run in runs:
        records, failures = get_evidence(run)
        if not records:
            missing.append(run['id'])
        for record in records:
            classifications[record.get('classification', 'unknown')] += 1
            if 'finished_at' in record and 'started_at' in record:
                lanes[record.get('lane', 'unknown')].append(record['finished_at'] - record['started_at'])
        for failure in failures:
            signature = failure.get('signature')
            if isinstance(signature, str) and len(signature) == 20:
                signatures[signature][run['id']] = run['html_url']
    return {'lane_classifications': dict(classifications), 'runs_observed': len(runs), 'runs_without_receipts': missing, 'lanes': {name: {'count': len(values), 'p50_seconds': quantile(values, .5), 'p95_seconds': quantile(values, .95)}
                      for name, values in lanes.items()},
            'recurring': {key: list(links.values()) for key, links in signatures.items() if len(links) >= 3}}


def throughput(runs, merges, start, end, jobs):
    hours = seconds(start, end) / 3600
    if hours <= 0:
        raise ValueError("queue measurement needs a positive observation window")
    outcomes = defaultdict(int)
    failure_seconds, runner_seconds, unfinished_jobs = [], 0, 0
    seen_jobs = set()
    for run in runs:
        outcomes[run["conclusion"]] += 1
        if run["conclusion"] in {"failure", "timed_out", "startup_failure", "action_required"}:
            failure_seconds.append(seconds(run["created_at"], run["updated_at"]))
        for job in jobs(run):
            if job["id"] in seen_jobs:
                continue
            seen_jobs.add(job["id"])
            if not job_metrics.assigned(job) or job.get("conclusion") == "skipped":
                continue
            if job.get("started_at") and job.get("completed_at"):
                runner_seconds += max(0, seconds(job["started_at"], job["completed_at"]))
            elif job.get("started_at"):
                unfinished_jobs += 1
    delivery = delivery_metrics.delivered(merges)
    delivered = len(delivery['original_prs'])
    failures = sum(outcomes[name] for name in ("failure", "timed_out", "startup_failure", "action_required"))
    decided = outcomes["success"] + failures
    return {"delivery": delivery, "original_prs_delivered": delivered,
            "original_prs_per_hour": delivered / hours,
            "runner_minutes_per_original_pr": runner_seconds / 60 / delivered if delivered else None,
            "window_hours": hours, "main_merges": len({p["number"] for p in merges}),
            "main_merges_per_hour": len({p["number"] for p in merges}) / hours,
            "merge_group_outcomes": dict(outcomes), "decided_groups": decided,
            "failure_fraction_of_decided": failures / decided if decided else None,
            "created_to_failure_p50_seconds": quantile(failure_seconds, .5),
            "created_to_failure_p95_seconds": quantile(failure_seconds, .95),
            "observed_runner_minutes": runner_seconds / 60, "jobs_missing_completion": unfinished_jobs}


def main():
    now = datetime.now(timezone.utc)
    end = now.strftime('%Y-%m-%dT%H:%M:%SZ')
    since = (now - timedelta(days=1)).strftime('%Y-%m-%dT%H:%M:%SZ')
    runs = [r for r in pages(f'{repository()}/actions/workflows/ci.yml/runs?created=>={since}', 'workflow_runs')
            if (r['event'] == 'merge_group' or r['event'] == 'workflow_dispatch'
                and r.get('head_branch', '').startswith('gh-readonly-queue/'))
            and r['status'] == 'completed' and r['updated_at'] <= end]
    report = summarize(runs, evidence)
    report['window_start'] = since
    merges = [pull for pull in recent_merges(since) if pull["mergedAt"] <= end]
    report['window_end'] = end
    jobs = {run['id']: list(pages(f"{repository()}/actions/runs/{run['id']}/jobs?filter=all", 'jobs')) for run in runs}
    report['throughput'] = throughput(runs, merges, since, end, lambda run: jobs[run['id']])
    report['job_costs'] = job_metrics.summarize(runs, lambda run: jobs[run['id']])
    report['queue_to_merge'] = queue_times(merges)
    report['queue_p95_seconds'] = quantile([r['seconds'] for r in report['queue_to_merge']], .95)
    report['unsuccessful_runs'] = [{'run': r['id'], 'conclusion': r['conclusion'], 'url': r['html_url']}
                              for r in runs if r['conclusion'] != 'success']
    Path('queue-health.json').write_text(json.dumps(report, indent=2) + '\n')
    with Path(os.environ['GITHUB_STEP_SUMMARY']).open('a') as summary:
        summary.write('### Queue health\n\n```json\n' + json.dumps(report, indent=2) + '\n```\n')


if __name__ == '__main__':
    main()
