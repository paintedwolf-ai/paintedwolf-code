"""Match failed jobs to exact-run infrastructure evidence before retrying."""
import subprocess
from .evidence import retryable
from .github import api, repository


def permits_retry(run, records, jobs):
    failed = [job for job in jobs if job['conclusion'] in {'failure', 'timed_out'}
              and job['name'] not in {'check', 'qualification'}]
    if not failed or run['run_attempt'] != 1:
        return False
    matched, retry_records = set(), []
    for record in records:
        if record.get('classification') == 'passed':
            continue
        identity = record.get('identity', {})
        if identity != {'run': str(run['id']), 'attempt': str(run['run_attempt']), 'sha': run.get('head_sha')}:
            return False
        lane = record.get('lane', '')
        shard = record.get('shard', '')
        suffix = lane + (f' ({shard})' if shard else '')
        candidates = [job for job in jobs if job['name'].rsplit(' / ', 1)[-1] == suffix]
        if len(candidates) != 1 or candidates[0]['id'] in matched:
            return False
        job = candidates[0]
        matched.add(job['id'])
        if job in failed:
            retry_records.append(record)
        elif job['conclusion'] != 'cancelled' or record.get('classification') not in {'unknown', 'runner_oom'}:
            return False
    return all(job['id'] in matched for job in failed) and retryable(retry_records, run['run_attempt'])


def current_group(run):
    branch = run.get('head_branch', '')
    if run.get('event') != 'merge_group' and not branch.startswith('gh-readonly-queue/'):
        return True
    if not branch.startswith('gh-readonly-queue/'):
        return False
    try:
        ref = api(f"{repository()}/git/ref/heads/{branch}")
        return ref['object']['sha'] == run['head_sha']
    except (subprocess.SubprocessError, OSError, KeyError, TypeError):
        return False
