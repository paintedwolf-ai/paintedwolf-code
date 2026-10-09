"""Reconcile one issue per maintainability artifact after a PR lands on main."""
from collections import Counter
import json
import os
from pathlib import Path

from .budget_evidence import read_snapshot
from .budget_issues import expected, plan
from .budget_history import checkpoint, intake, merged_pulls
from .github import api, pages, repository
from .budget_snapshot import SHA


def eligible(run, repo):
    return (run.get('repository', {}).get('full_name') == repo
            and run.get('path') == '.github/workflows/qualification.yml'
            and run.get('event') == 'push' and run.get('head_branch') == 'main'
            and run.get('status') == 'completed' and isinstance(run.get('head_sha'), str)
            and SHA.fullmatch(run['head_sha']) is not None)


def current_main(sha):
    return api(f'{repository()}/git/ref/heads/main')['object']['sha'] == sha


def reconcile(run, dry_run=False, backfill=False):
    repo = os.environ['GITHUB_REPOSITORY']
    if not eligible(run, repo) or not current_main(run['head_sha']):
        return {'skipped': 'not a current post-merge qualification push'}
    pulls = merged_pulls(run, repo)
    if not pulls:
        return {'skipped': 'no merged PR associated with this main commit'}
    tree = api(f"{repository()}/git/commits/{run['head_sha']}")['tree']['sha']
    snapshot, artifacts = read_snapshot(run, tree)
    observation = {k: snapshot[k] for k in ['source_sha', 'source_tree', 'base_sha', 'digest']}
    observation.update(repository=repo, run_id=run['id'], run_attempt=run['run_attempt'],
                       run_url=f"https://github.com/{repo}/actions/runs/{run['id']}",
                       merged_prs=sorted(p['number'] for p in pulls))
    prior = None if backfill else checkpoint()
    intake_keys = intake(run, artifacts, prior, repo)
    operations, suppressed = plan(list(pages(f'{repository()}/issues?state=all')), artifacts, observation, backfill, intake_keys)
    report = {'run_id': run['id'], 'source_sha': run['head_sha'], 'dry_run': dry_run,
              'backfill': backfill, 'intake_base_sha': prior['source_sha'] if prior else None, 'above_threshold': dict(Counter(row['category'] for row in artifacts.values())),
              'suppressed': suppressed, 'actions': dict(Counter(op['action'] for op in operations)),
              'operations': operations}
    if not current_main(run['head_sha']):
        return {'skipped': 'main advanced during planning'}
    if not dry_run:
        for operation in operations:
            # A full later snapshot converges state; do not write older evidence once main advances.
            if not current_main(run['head_sha']):
                report['stopped'] = 'main advanced during reconciliation'
                break
            path = f'{repository()}/issues'
            if operation['action'] == 'create':
                api(path, 'POST', operation['body'])
            else:
                target = f"{path}/{operation['number']}"
                if expected(api(target)) != operation['expected']:
                    raise ValueError('an issue changed during reconciliation; replay to preserve the concurrent edit')
                api(target, 'PATCH', operation['body'])
    if not dry_run and 'stopped' not in report and current_main(run['head_sha']):
        report['checkpoint'] = {'schema_version': 1, 'source_sha': run['head_sha'],
                                'qualification_created_at': run['created_at']}
    return report


def main():
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
    inputs = event.get('inputs', {})
    identity = inputs.get('run_id') if os.environ.get('GITHUB_EVENT_NAME') == 'workflow_dispatch' else event['workflow_run']['id']
    if not str(identity).isdigit() or int(identity) <= 0:
        raise ValueError('run_id must be a positive integer')
    report = reconcile(api(f'{repository()}/actions/runs/{identity}'), str(inputs.get('dry_run', 'false')).lower() == 'true',
                       str(inputs.get('backfill', 'false')).lower() == 'true')
    Path('maintainability-issues.json').write_text(json.dumps(report, indent=2) + '\n')
    if 'checkpoint' in report:
        Path('maintainability-checkpoint.json').write_text(json.dumps(report['checkpoint']) + '\n')
    compact = {k: v for k, v in report.items() if k != 'operations'}
    print(json.dumps(compact, indent=2))
    with Path(os.environ['GITHUB_STEP_SUMMARY']).open('a') as summary:
        summary.write('### Maintainability cleanup\n\n```json\n' + json.dumps(compact, indent=2) + '\n```\n')


if __name__ == '__main__':
    main()
