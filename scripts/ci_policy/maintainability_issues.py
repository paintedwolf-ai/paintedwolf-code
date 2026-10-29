"""Reconcile bounded maintainability workstreams from a nightly report on main.

Only artifacts whose measured content is unchanged on current main take their
tracking from the report; the others are held with a record of both commits.
"""
from collections import Counter
import json
import os
import subprocess
import time
import zipfile
from pathlib import Path

from .budget_evidence import read_snapshot
from .budget_issues import expected
from .budget_workstreams import plan
from .budget_history import ROOT, MainChanges, checkpoint, intake, merged_pulls
from .github import api, pages, repository
from .budget_snapshot import SHA


def eligible(run, repo):
    return (run.get('repository', {}).get('full_name') == repo
            and run.get('path') == '.github/workflows/nightly.yml'
            and run.get('event') in {'schedule', 'workflow_dispatch'} and run.get('head_branch') == 'main'
            and run.get('status') == 'completed' and isinstance(run.get('head_sha'), str)
            and run.get('conclusion') in {'success', 'failure'}
            and SHA.fullmatch(run['head_sha']) is not None)


class Holds:
    """Artifacts whose measurement inputs changed between the report source and main."""

    def __init__(self, source):
        self.source, self.records = source, {}
        self.refresh()

    def refresh(self):
        self.changes = MainChanges(ROOT, self.source)

    def __call__(self, row):
        if (row['category'], row['id']) in self.records:
            return True
        if not self.changes.changed(row):
            return False
        self.records[(row['category'], row['id'])] = {
            'category': row['category'], 'id': row['id'], 'report_source': self.source, 'main': self.changes.main}
        return True

    def report(self):
        return [self.records[k] for k in sorted(self.records)]


def reconcile(run, dry_run=False, backfill=False, save_plan=None):
    repo = os.environ['GITHUB_REPOSITORY']
    if not eligible(run, repo):
        return {'skipped': 'not a nightly observation of main'}
    pulls = merged_pulls(run, repo)
    if not pulls:
        return {'skipped': 'no merged PR associated with this main commit'}
    tree = api(f"{repository()}/git/commits/{run['head_sha']}")['tree']['sha']
    snapshot, artifacts = read_snapshot(run, tree)
    observation = {k: snapshot[k] for k in ['source_sha', 'source_tree', 'base_sha', 'digest']}
    observation.update(repository=repo, run_id=run['id'], run_attempt=run['run_attempt'],
                       run_url=f"https://github.com/{repo}/actions/runs/{run['id']}",
                       merged_prs=sorted(p['number'] for p in pulls))
    workstreams = {}
    for identity, row in artifacts.items():
        review = row.get('review')
        if review:
            workstreams.setdefault(review['workstream'], []).append({
                'key': identity, 'category': row['category'], 'artifact': row['id'],
                'measured': row['measured'], 'state': review['state']})
    prior = None if backfill else checkpoint()
    intake_keys = set(artifacts) if backfill else intake(run, artifacts, prior)
    holds = Holds(run['head_sha'])
    operations, suppressed = plan(list(pages(f'{repository()}/issues?state=all')), artifacts, observation,
                                  backfill, intake_keys, holds)
    planned = set(holds.records)
    waiting = [a for op in operations if op['action'] == 'hold' for a in op['artifacts']]
    operations = [op for op in operations if op['action'] != 'hold']
    creations = 0
    selected = []
    for operation in operations:
        if operation['action'] == 'create':
            creations += 1
            if creations > 25:
                continue
        selected.append(operation)
    deferred = max(0, creations - 25)
    report = {'run_id': run['id'], 'source_sha': run['head_sha'], 'dry_run': dry_run,
              'backfill': backfill, 'intake_base_sha': prior['source_sha'] if prior else None, 'above_threshold': dict(Counter(row['category'] for row in artifacts.values())),
              'suppressed': suppressed, 'workstreams': workstreams, 'deferred_creations': deferred, 'applied_operations': 0, 'actions': dict(Counter(op['action'] for op in operations)),
              'operations': selected, 'planned_main_sha': holds.changes.main, 'held': holds.report(),
              'waiting_intake': waiting, 'held_operations': 0}
    if save_plan:
        save_plan(report)
    if not dry_run:
        for operation in selected:
            time.sleep(1)
            holds.refresh()
            # Main may advance while writing; an area with a newly changed member waits for a later report.
            if [a for a in operation['artifacts'] if (a['category'], a['id']) not in planned and holds(a)]:
                report['held'] = holds.report()
                report['held_operations'] += 1
                continue
            path = f'{repository()}/issues'
            if operation['action'] == 'create':
                api(path, 'POST', operation['body'])
            else:
                target = f"{path}/{operation['number']}"
                if expected(api(target)) != operation['expected']:
                    raise ValueError('an issue changed during reconciliation; replay to preserve the concurrent edit')
                api(target, 'PATCH', operation['body'])
            report['applied_operations'] += 1
            if save_plan:
                save_plan(report)
    # Withheld intake keeps the checkpoint so a later report admits it.
    if not dry_run and not deferred and not waiting and not report['held_operations']:
        report['checkpoint'] = {'schema_version': 1, 'source_sha': run['head_sha']}
    return report


def main():
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
    inputs = event.get('inputs', {})
    identity = inputs.get('run_id') if os.environ.get('GITHUB_EVENT_NAME') == 'workflow_dispatch' else event['workflow_run']['id']
    if not str(identity).isdigit() or int(identity) <= 0:
        raise ValueError('run_id must be a positive integer')
    def save_plan(report):
        Path('maintainability-issues.json').write_text(json.dumps(report, indent=2) + '\n')

    failure = None
    try:
        report = reconcile(api(f'{repository()}/actions/runs/{identity}'),
                           str(inputs.get('dry_run', 'false')).lower() == 'true',
                           str(inputs.get('backfill', 'false')).lower() == 'true', save_plan)
    except (ValueError, subprocess.CalledProcessError, zipfile.BadZipFile) as error:
        failure = error
        path = Path('maintainability-issues.json')
        report = json.loads(path.read_text()) if path.exists() else {}
        report['error'] = str(error)
    save_plan(report)
    if 'checkpoint' in report:
        Path('maintainability-checkpoint.json').write_text(json.dumps(report['checkpoint']) + '\n')
    compact = {k: v for k, v in report.items() if k != 'operations'}
    print(json.dumps(compact, indent=2))
    with Path(os.environ['GITHUB_STEP_SUMMARY']).open('a') as summary:
        summary.write('### Maintainability cleanup\n\n```json\n' + json.dumps(compact, indent=2) + '\n```\n')
    if failure:
        raise SystemExit(1)


if __name__ == '__main__':
    main()
