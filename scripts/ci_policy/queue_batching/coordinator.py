"""Reconcile one automatic queue batch at a time using exact source identities."""
import json
import os
from pathlib import Path
import subprocess
import tempfile

from ci_policy.batch_prs import command, source
from ci_policy.github import api, pages
from . import github
from .selection import MARKER, identity, manifest, select, unchanged


def describe(record):
    sources = ', '.join(f'#{p["number"]}' for p in record['sources'])
    return (f'Automatic merge-queue batch of {sources}. Combined admission and merge-group checks are required. '
            'Original PRs remain open until this batch lands.' + MARKER + json.dumps(record, sort_keys=True) + '\n-->')


def dispatch(repository, pull, record):
    for workflow, ref, inputs in (
            ('ci.yml', pull['head']['ref'], {'profile': 'fast'}),
            ('dco.yml', 'main', {'pull_request': str(pull['number'])})):
        key = workflow + '_dispatched'
        if not record.get(key):
            api(f'repos/{repository}/actions/workflows/{workflow}/dispatches', 'POST',
                {'ref': ref, 'inputs': inputs})
            record[key] = True
            api(f'repos/{repository}/pulls/{pull["number"]}', 'PATCH', {'body': describe(record)})


def settle(repository, pull, record, outcome):
    record.update(settled=True, outcome=outcome)
    api(f'repos/{repository}/pulls/{pull["number"]}', 'PATCH', {'body': describe(record)})


def dissolve(repository, pull, record, outcome='individual_fallback'):
    github.withdraw(repository, {'number': pull['number']})
    api(f'repos/{repository}/pulls/{pull["number"]}', 'PATCH', {'state': 'closed'})
    for row in record['sources']:
        github.restore(repository, row)
    settle(repository, pull, record, outcome)


def reconcile(repository, pull, record):
    if record.get('settled'):
        return False
    if pull['merged_at']:
        for row in record['sources']:
            current = api(f'repos/{repository}/pulls/{row["number"]}')
            if current['state'] == 'open' and not current['draft'] and current['head']['sha'] == row['headRefOid']:
                api(f'repos/{repository}/pulls/{row["number"]}', 'PATCH', {'state': 'closed'})
        settle(repository, pull, record, 'merged')
        return False
    if pull['state'] != 'open':
        for row in record['sources']:
            github.restore(repository, row)
        settle(repository, pull, record, 'individual_fallback')
        return False
    # A human draft decision is a handoff; never promote the batch again.
    if pull['draft']:
        return True
    try:
        current = [source(repository, row['number']) for row in record['sources']]
    except ValueError:
        dissolve(repository, pull, record)
        return False
    if any(row.get('mergeQueueEntry') is not None for row in current):
        dissolve(repository, pull, record)
        return False
    held = any(github.blocked(api(f'repos/{repository}/pulls/{row["number"]}')) for row in record['sources'])
    if pull['head']['sha'] != record['batch_head'] or held or github.blocked(pull) or any(not unchanged(now, row) for now, row in zip(current, record['sources'])):
        dissolve(repository, pull, record, 'no_combine' if held or github.blocked(pull) else 'source_changed')
        return False
    if github.ejected(repository, pull['number']):
        dissolve(repository, pull, record)
        return False
    dispatch(repository, pull, record)
    verdict = github.checks(repository, pull['head']['sha'])
    if verdict == 'failed':
        # Individual fallbacks isolate the failure without retrying the same
        # source identities in another batch or suppressing required checks.
        dissolve(repository, pull, record)
        return False
    if verdict == 'passed' and not pull.get('auto_merge') and source(repository, pull['number']).get('mergeQueueEntry') is None:
        github.mutation('enablePullRequestAutoMerge', {'pullRequestId': pull['node_id'], 'mergeMethod': 'SQUASH'})
    return True


def build(repository, rows, entries):
    base = api(f'repos/{repository}/git/ref/heads/main')['object']['sha']
    branch = identity(base, rows)
    record = {'version': 1, 'base': base, 'sources': rows}
    with tempfile.TemporaryDirectory(prefix='paintedwolf-queue-batch-') as directory:
        checkout = Path(directory) / 'checkout'
        command(['git', 'fetch', '--no-tags', '--no-write-fetch-head', 'origin', base,
                 *[row['headRefOid'] for row in rows]])
        command(['git', 'worktree', 'add', '--detach', str(checkout), base])
        try:
            # No code from the combined tree executes in this privileged job.
            # Git only merges tracked source; hooks come from the trusted checkout.
            for row in rows:
                command(['git', 'merge', '--no-ff', '--no-edit', '--signoff', row['headRefOid']], checkout)
            snapshot = {e['pullRequest']['number']: e for e in github.queue(repository)}
            for row in rows:
                entry = snapshot.get(row['number'])
                if not entry or entry['state'] != 'QUEUED' or entry['position'] <= entries:
                    return None
                if not unchanged(source(repository, row['number']), row):
                    return None
            # The Actions token cannot publish workflow changes. Recheck the
            # actual combined tree before any push or source withdrawal.
            workflows = command(['git', 'diff', '--name-only', base, 'HEAD',
                                 '--', '.github/workflows/'], checkout)
            if workflows:
                return None
            record['batch_head'] = command(['git', 'rev-parse', 'HEAD'], checkout)
            command(['git', 'push', 'origin', f'HEAD:refs/heads/{branch}'], checkout)
            pull = api(f'repos/{repository}/pulls', 'POST', {
                'title': 'Automatic queue batch: ' + ', '.join(f'#{p["number"]}' for p in rows),
                'head': branch, 'base': 'main', 'body': describe(record), 'draft': True})
            try:
                for row in rows:
                    if github.blocked(api(f'repos/{repository}/pulls/{row["number"]}')):
                        raise ValueError('source acquired a merge hold during preparation')
                    github.withdraw(repository, row)
                github.mutation('markPullRequestReadyForReview', {'pullRequestId': pull['node_id']})
                # GITHUB_TOKEN-created PRs do not trigger pull_request workflows.
                # Explicit dispatch is the supported exception and runs with the
                # CI workflow's read-only verification permissions.
                dispatch(repository, {**pull, 'head': {'ref': branch}}, record)
            except Exception:
                dissolve(repository, pull, record)
                raise
            return pull['html_url']
        finally:
            # This is an owned disposable checkout, including a merge conflict.
            command(['git', 'worktree', 'remove', '--force', str(checkout)])


def run(repository, policy):
    from ci_policy.queue_dispatch import recover
    github.withdraw_drafts(repository)
    recover(repository)
    policy = {**policy, 'preserve_front': max(policy['preserve_front'], github.build_window(repository))}
    github.enforce_holds(repository)
    history = []
    for pull in github.batches(repository):
        record = manifest(pull['body'])
        if record:
            history.append((pull, record))
    active = False
    excluded = set()
    for pull, record in history:
        active = reconcile(repository, pull, record) or active
        if record.get('outcome') == 'individual_fallback' or (pull['state'] == 'closed' and not pull['merged_at'] and not record.get('settled')):
            excluded.update((r['number'], r['headRefOid']) for r in record['sources'])
    if active:
        return {'state': 'watching'}
    rows = select(github.queue(repository), policy,
                  lambda number: [f['filename'] for f in pages(f'repos/{repository}/pulls/{number}/files')], excluded)
    if not rows:
        return {'state': 'no_waiting_batch'}
    try:
        url = build(repository, rows, policy['preserve_front'])
        return {'state': 'created', 'url': url} if url else {'state': 'individual_fallback'}
    except subprocess.CalledProcessError:
        # A source conflict leaves original admissions intact and requires no
        # interactive resolution. Their normal queue checks remain authoritative.
        return {'state': 'individual_fallback'}


def main():
    policy = json.loads(Path('.github/queue-batching.json').read_text())
    print(json.dumps(run(os.environ['GITHUB_REPOSITORY'], policy), sort_keys=True))


if __name__ == '__main__':
    main()
