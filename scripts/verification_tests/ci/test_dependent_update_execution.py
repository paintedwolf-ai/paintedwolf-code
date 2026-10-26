"""Dependent PR updates merge main only into exact heads and never resolve conflicts."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import dependent_update as d

ROOT = Path(__file__).resolve().parents[3]
REPO = 'o/r'
LANDED_OPENED = '2026-10-20T00:00:00Z'


def failure():
    return subprocess.CalledProcessError(1, ['gh', 'api'], stderr='HTTP 422')


def landed(**change):
    pull = {'number': 1, 'merged': True, 'created_at': LANDED_OPENED, 'base': {'ref': 'main'},
            'head': {'ref': 'feat/parent', 'repo': {'full_name': REPO}}, 'labels': []}
    return {'action': 'closed', 'pull_request': {**pull, **change}}


def node(number, base='feat/parent', moved=None, **change):
    return {'number': number, 'headRefOid': f'{number:040x}', 'headRefName': f'feat/{number}', 'baseRefName': base,
            'isDraft': False, 'headRepository': {'nameWithOwner': REPO}, 'mergeQueueEntry': None,
            'timelineItems': {'nodes': [moved] if moved else []}, **change}


def moved(previous='feat/parent', current='main', at='2026-10-21T00:00:00Z'):
    return {'previousRefName': previous, 'currentRefName': current, 'createdAt': at}


class GitHub:
    """Pull request state that answers the REST calls the controller makes."""

    def __init__(self, *nodes):
        self.nodes = list(nodes)
        self.pulls = {n['number']: {'number': n['number'], 'draft': n['isDraft'], 'base': {'ref': n['baseRefName']},
                                    'head': {'sha': n['headRefOid'], 'ref': n['headRefName']}, 'mergeable': None}
                      for n in nodes}
        self.behind = {n['number']: 3 for n in nodes}
        self.update = {}       # number -> 'merge' | 'conflict' | 'stale' | 'refused'
        self.mergeable = {}    # number -> value after the first read
        self.lag = {}          # number -> reads before a requested merge appears
        self.base_refusal = {}
        self.unheld = set()    # numbers whose bot merge leaves no held CI run
        self.runs = {}         # head -> runs GitHub reports for that head
        self.calls = []

    def merge_appears(self, number, pull):
        head = pull['head']['sha'] = pull.pop('pending')
        decoys = [{'id': number * 100 + 1, 'head_sha': head, 'path': '.github/workflows/dco.yml', 'triggering_actor': {'login': d.BOT}},
                  {'id': number * 100 + 2, 'head_sha': head, 'path': '.github/workflows/ci.yml', 'triggering_actor': {'login': 'someone'}}]
        held = [] if number in self.unheld else [{'id': number * 100, 'head_sha': head, 'path': '.github/workflows/ci.yml',
                                                  'triggering_actor': {'login': d.BOT}}]
        self.runs[head] = held + decoys

    def api(self, path, method='GET', body=None):
        self.calls.append((method, path, body))
        parts = path.split('/')
        if path.startswith(f'repos/{REPO}/actions/runs?'):
            return {'workflow_runs': self.runs.get(path.rsplit('head_sha=', 1)[1], [])}
        if path.startswith(f'repos/{REPO}/compare/main...'):
            sha = parts[-1].split('...')[-1]
            number = next(n for n, p in self.pulls.items() if p['head']['sha'] == sha)
            return {'behind_by': self.behind[number]}
        if path.startswith(f'repos/{REPO}/pulls/'):
            number = int(parts[4])
            pull = self.pulls[number]
            if path.endswith('/update-branch'):
                assert body == {'expected_head_sha': pull['head']['sha']} or self.update.get(number) == 'stale'
                outcome = self.update.get(number, 'merge')
                if outcome == 'stale':
                    pull['head']['sha'] = 'f' * 40
                    raise failure()
                if outcome in {'conflict', 'refused'}:
                    raise failure()
                self.lag.setdefault(number, 0)
                pull['pending'] = 'e' * 39 + str(number % 10)
                return {'message': 'Updating pull request branch.'}
            if method == 'PATCH':
                refusal = self.base_refusal.get(number)
                if refusal:
                    pull['base']['ref'] = refusal
                    raise failure()
                pull['base']['ref'] = body['base']
                return pull
            if 'pending' in pull:
                if self.lag[number] == 0:
                    self.merge_appears(number, pull)
                else:
                    self.lag[number] -= 1
            if number in self.mergeable:
                pull['mergeable'] = self.mergeable[number]
            return json.loads(json.dumps(pull))
        if method in {'POST', 'DELETE'}:
            return {}
        raise AssertionError(f'unexpected call {method} {path}')

    def writes(self, kind):
        return [(path, body) for method, path, body in self.calls if method != 'GET' and kind in path]

    def run(self, event):
        sleeps = []
        with patch.object(d, 'api', side_effect=self.api), patch.object(d, 'open_pulls', return_value=self.nodes):
            record = d.update_dependents(REPO, event, sleep=sleeps.append)
        return record, sleeps


class DependentSelectionTests(unittest.TestCase):
    def test_selects_prs_on_the_landed_branch_or_moved_from_it_after_that_pr_opened(self):
        event = landed()['pull_request']
        chosen = [node(2), node(3, base='main', moved=moved())]
        refused = [node(4, base='feat/other'), node(5, base='main'),
                   node(6, base='main', moved=moved(previous='feat/other')),
                   node(7, base='main', moved=moved(at='2026-10-19T00:00:00Z')),
                   node(8, base='release', moved=moved(current='release')),
                   node(9, headRepository={'nameWithOwner': 'fork/r'}), node(10, headRepository=None),
                   node(11, mergeQueueEntry={'id': 'queued'})]
        self.assertEqual([p['number'] for p in chosen + refused if d.dependent(p, event, REPO)], [2, 3])

    def test_open_pulls_follows_pages_and_refuses_graphql_errors(self):
        pages = [{'data': {'repository': {'pullRequests': {'pageInfo': {'hasNextPage': True, 'endCursor': 'c1'}, 'nodes': [node(2)]}}}},
                 {'data': {'repository': {'pullRequests': {'pageInfo': {'hasNextPage': False, 'endCursor': None}, 'nodes': [node(3)]}}}}]
        with patch.object(d, 'api', side_effect=pages) as api:
            self.assertEqual([p['number'] for p in d.open_pulls(REPO)], [2, 3])
        self.assertEqual([call.args[2]['variables']['cursor'] for call in api.call_args_list], [None, 'c1'])
        with patch.object(d, 'api', return_value={'errors': ['denied']}), self.assertRaises(ValueError):
            list(d.open_pulls(REPO))


class LandedTests(unittest.TestCase):
    def test_only_a_same_repository_merge_into_main_updates_dependents(self):
        for change in ({'merged': False}, {'base': {'ref': 'feat/other'}},
                       {'head': {'ref': 'feat/parent', 'repo': {'full_name': 'fork/r'}}},
                       {'head': {'ref': 'feat/parent', 'repo': None}}):
            github = GitHub(node(2))
            self.assertEqual(github.run(landed(**change))[0], {'state': 'not_applicable'})
            self.assertEqual(github.calls, [])

    def test_retargets_merges_the_exact_head_and_dispatches_checks_once_the_merge_appears(self):
        github = GitHub(node(2), node(3, base='main', moved=moved()))
        github.lag = {2: 2, 3: 0}
        record, sleeps = github.run(landed())
        self.assertEqual(record, {'state': 'reconciled', 'landed': 1, 'failed': [], 'held_runs_remaining': [],
                                  'outcomes': {'2': 'updated_and_checked', '3': 'updated_and_checked'}})
        self.assertEqual(github.writes('pulls/2')[0], (f'repos/{REPO}/pulls/2', {'base': 'main'}))
        self.assertFalse(any(path == f'repos/{REPO}/pulls/3' for path, _ in github.writes('pulls/3')))
        dispatches = [(path.split('/')[-2], body) for path, body in github.writes('dispatches')]
        self.assertEqual(dispatches, [
            ('ci.yml', {'ref': 'feat/3', 'inputs': {'profile': 'fast'}}),
            ('dco.yml', {'ref': 'main', 'inputs': {'pull_request': '3'}}),
            ('ci.yml', {'ref': 'feat/2', 'inputs': {'profile': 'fast'}}),
            ('dco.yml', {'ref': 'main', 'inputs': {'pull_request': '2'}})])
        self.assertEqual(sleeps, [d.INTERVAL, d.INTERVAL])

    def test_drafts_are_updated_without_dispatching_checks_and_current_branches_are_left_alone(self):
        github = GitHub(node(2, isDraft=True), node(3))
        github.behind[3] = 0
        record, _ = github.run(landed())
        self.assertEqual(record['outcomes'], {'2': 'updated', '3': 'current'})
        self.assertEqual(github.writes('dispatches'), [])
        self.assertEqual(github.writes('update-branch'), [(f'repos/{REPO}/pulls/2/update-branch', {'expected_head_sha': node(2)['headRefOid']})])

    def test_a_concurrent_push_is_kept_and_a_conflict_is_labelled_for_the_owner(self):
        github = GitHub(node(2), node(3))
        github.update = {2: 'stale', 3: 'conflict'}
        github.mergeable = {3: False}
        record, _ = github.run(landed())
        self.assertEqual(record, {'state': 'reconciled', 'landed': 1, 'failed': [], 'held_runs_remaining': [],
                                  'outcomes': {'2': 'superseded', '3': 'conflict'}})
        self.assertEqual(github.writes('issues/2'), [])
        labels, comment = github.writes('issues/3')
        self.assertEqual(labels, (f'repos/{REPO}/issues/3/labels', {'labels': [d.LABEL]}))
        self.assertIn('#1 landed on `main`', comment[1]['body'])
        self.assertIn(f'`{d.LABEL}` label clears on that push', comment[1]['body'])
        self.assertEqual(github.writes('dispatches'), [])

    def test_an_unexplained_refusal_fails_the_run_without_stopping_other_dependents(self):
        for mergeable in (True, None):
            with self.subTest(mergeable=mergeable):
                github = GitHub(node(2), node(3))
                github.update = {2: 'refused'}
                if mergeable is not None:
                    github.mergeable = {2: mergeable}
                with patch('builtins.print'):
                    record, sleeps = github.run(landed())
                self.assertEqual(record['state'], 'incomplete')
                self.assertEqual(record['failed'], [2])
                self.assertEqual(record['outcomes'], {'3': 'updated_and_checked'})
                self.assertEqual(github.writes('issues/2'), [])
                if mergeable is None:
                    self.assertEqual(len(sleeps), d.CONFLICT_POLLS)

    def test_base_refusal_is_accepted_only_when_the_pr_already_targets_main(self):
        github = GitHub(node(2), node(3))
        github.base_refusal = {2: 'main', 3: 'feat/parent'}
        with patch('builtins.print'):
            record, _ = github.run(landed())
        self.assertEqual(record['outcomes'], {'2': 'updated_and_checked'})
        self.assertEqual(record['failed'], [3])
        self.assertEqual(github.writes('pulls/3/update-branch'), [])

    def test_deletes_only_the_ci_run_github_holds_for_each_bot_merge(self):
        github = GitHub(node(2), node(3, isDraft=True), node(4))
        github.unheld = {4}
        record, sleeps = github.run(landed())
        self.assertEqual(record['state'], 'reconciled')
        self.assertEqual(record['held_runs_remaining'], [4])
        deleted = [path for method, path, _ in github.calls if method == 'DELETE']
        self.assertEqual(deleted, [f'repos/{REPO}/actions/runs/200', f'repos/{REPO}/actions/runs/300'])
        self.assertEqual(sleeps, [d.INTERVAL] * d.HELD_POLLS)

    def test_a_merge_that_never_appears_is_reported_without_dispatching_checks(self):
        github = GitHub(node(2))
        github.update = {2: 'merge'}
        github.lag = {2: d.POLLS + 1}
        record, sleeps = github.run(landed())
        self.assertEqual(record['state'], 'incomplete')
        self.assertEqual(record['outcomes'], {'2': 'unconfirmed'})
        self.assertEqual(github.writes('dispatches'), [])
        self.assertEqual(len(sleeps), d.POLLS)


class EventTests(unittest.TestCase):
    def test_a_push_clears_only_an_existing_conflict_label(self):
        event = {'action': 'synchronize', 'pull_request': {'number': 4, 'labels': [{'name': d.LABEL}]}}
        with patch.object(d, 'api') as api:
            self.assertEqual(d.reconcile({'GITHUB_REPOSITORY': REPO, 'GITHUB_EVENT_NAME': 'pull_request_target'}, event),
                             {'state': 'cleared', 'pull_request': 4})
            api.assert_called_once_with(f'repos/{REPO}/issues/4/labels/{d.LABEL}', 'DELETE')
            event['pull_request']['labels'] = [{'name': 'no-combine'}]
            self.assertEqual(d.reconcile({'GITHUB_REPOSITORY': REPO, 'GITHUB_EVENT_NAME': 'pull_request_target'}, event),
                             {'state': 'not_applicable'})
            api.assert_called_once()

    def test_runs_only_from_pull_request_events(self):
        environment = {'GITHUB_REPOSITORY': REPO, 'GITHUB_EVENT_NAME': 'pull_request_target'}
        self.assertEqual(d.reconcile(environment, {'action': 'labeled'}), {'state': 'not_applicable'})
        with patch.object(d, 'update_dependents', return_value={'state': 'reconciled'}) as update:
            self.assertEqual(d.reconcile(environment, landed()), {'state': 'reconciled'})
            update.assert_called_once_with(REPO, landed())
        with self.assertRaises(ValueError):
            d.reconcile({**environment, 'GITHUB_EVENT_NAME': 'push'}, landed())

    def test_main_records_the_outcome_and_fails_an_incomplete_run(self):
        for state, exits in (('reconciled', False), ('incomplete', True)):
            with self.subTest(state=state), tempfile.TemporaryDirectory() as directory:
                event, summary = Path(directory) / 'event.json', Path(directory) / 'summary.md'
                event.write_text(json.dumps(landed()))
                environment = {'GITHUB_EVENT_PATH': str(event), 'GITHUB_STEP_SUMMARY': str(summary),
                               'GITHUB_REPOSITORY': REPO, 'GITHUB_EVENT_NAME': 'pull_request_target'}
                with patch.dict(os.environ, environment), patch('builtins.print'), \
                        patch.object(d, 'update_dependents', return_value={'state': state}):
                    if exits:
                        with self.assertRaises(SystemExit):
                            d.main()
                    else:
                        d.main()
                self.assertIn(f'"state": "{state}"', summary.read_text())

    def test_workflow_gates_label_clearing_on_the_controller_label(self):
        workflow = (ROOT / '.github/workflows/dependent-update.yml').read_text()
        self.assertIn(f"contains(github.event.pull_request.labels.*.name, '{d.LABEL}')", workflow)
        self.assertIn('python3 -m ci_policy.dependent_update', workflow)


if __name__ == '__main__':
    unittest.main()
