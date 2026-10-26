"""Account for hosted job time without treating dependency waits as runner waits."""
from collections import defaultdict
from datetime import datetime


def elapsed(start, end):
    if not start or not end:
        return None
    return max(0, (datetime.fromisoformat(end.replace('Z', '+00:00'))
                   - datetime.fromisoformat(start.replace('Z', '+00:00'))).total_seconds())


def assigned(job):
    return bool(job.get('runner_id')) or any(step.get('started_at') for step in job.get('steps', []))


def summarize(runs, jobs):
    totals = defaultdict(float)
    lanes = defaultdict(lambda: defaultdict(list))
    missing, unassigned = [], []
    seen = set()
    for run in runs:
        for job in jobs(run):
            if job['id'] in seen or job.get('conclusion') == 'skipped':
                continue
            seen.add(job['id'])
            if not assigned(job):
                unassigned.append(job['id'])
                continue
            duration = elapsed(job.get('started_at'), job.get('completed_at'))
            if duration is None:
                missing.append(job['id'])
                continue
            totals['runner_seconds'] += duration
            if run['conclusion'] == 'cancelled' or job.get('conclusion') == 'cancelled':
                totals['cancelled_runner_seconds'] += duration
            if run['conclusion'] in {'failure', 'timed_out'}:
                totals['failed_run_runner_seconds'] += duration
            lane = job.get('name', 'unknown').rsplit(' / ', 1)[-1]
            lanes[lane]['job_seconds'].append(duration)
            # GitHub's job creation time includes dependency and matrix admission waits.
            wait = elapsed(job.get('created_at'), job.get('started_at'))
            if wait is not None:
                lanes[lane]['created_to_start_seconds'].append(wait)
            execution = [step for step in job.get('steps', []) if step['name'] == 'Run verification']
            if len(execution) != 1:
                continue
            step = execution[0]
            for name, start, end in (
                    ('setup_seconds', job.get('started_at'), step.get('started_at')),
                    ('execution_seconds', step.get('started_at'), step.get('completed_at')),
                    ('completion_seconds', step.get('completed_at'), job.get('completed_at'))):
                value = elapsed(start, end)
                if value is not None:
                    totals[name] += value
                    lanes[lane][name].append(value)
    return {'totals': dict(totals), 'lanes': dict(lanes), 'jobs_without_complete_timestamps': missing, 'jobs_without_runner_evidence': unassigned,
            'wait_definition': 'Job creation to start includes dependencies and matrix admission; runner-only wait is unavailable.'}
