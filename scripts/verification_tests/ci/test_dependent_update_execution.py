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
LANDED_MERGED = '2026-10-26T22:52:04Z'


def failure():
    return subprocess.CalledProcessError(1, ['gh', 'api'], stderr='HTTP 422')


def landed(**change):
    pull = {'number': 1, 'merged': True, 'created_at': LANDED_OPENED, 'merged_at': LANDED_MERGED,
            'base': {'ref': 'main', 'sha': 'b' * 40},
            'head': {'ref': 'feat/parent', 'sha': 'a' * 40, 'repo': {'full_name': REPO}}, 'labels': []}
    return {'action': 'closed', 'pull_request': {**pull, **change}}


def node(number, base='feat/parent', moved=None, **change):
    return {'number': number, 'headRefOid': f'{number:040x}', 'headRefName': f'feat/{number}', 'baseRefName': base,
            'createdAt': '2026-10-21T00:00:00Z', 'updatedAt': '2026-10-21T00:00:00Z',
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
        self.update = {}       # number -> 'merge' | 'conflict' | 'refused'
        self.pushes = {}       # number -> owner pushes that race the next update requests
        self.settle_pushes = {}  # number -> owner pushes after an update request was accepted
        self.pushed_behind = {}  # number -> how far behind main the owner's pushes leave it
        self.read_push = {}    # number -> an owner push seen first by the next PR read
        self.mergeable = {}    # number -> value after the first read
        self.lag = {}          # number -> reads before a requested merge appears
        self.base_refusal = {}
        self.unheld = set()    # numbers whose bot merge leaves no held CI run
        self.inherited = set() # numbers whose heads contain the landed parent's unique commit
        self.runs = {}         # head -> runs GitHub reports for that head
        self.calls = []

    def merge_appears(self, number, pull):
        head = pull['head']['sha'] = pull.pop('pending')
        self.behind[number] = 0
        decoys = [{'id': number * 100 + 1, 'head_sha': head, 'path': '.github/workflows/dco.yml', 'triggering_actor': {'login': d.BOT}},
                  {'id': number * 100 + 2, 'head_sha': head, 'path': '.github/workflows/ci.yml', 'triggering_actor': {'login': 'someone'}}]
        held = [] if number in self.unheld else [{'id': number * 100, 'head_sha': head, 'path': '.github/workflows/ci.yml',
                                                  'triggering_actor': {'login': d.BOT}}]
        self.runs[head] = held + decoys

    def push(self, number, pull):
        pull['head']['sha'] = f'{number:04d}{len(self.calls):036d}'
        self.behind[number] = self.pushed_behind.get(number, self.behind[number])

    def api(self, path, method='GET', body=None):
        self.calls.append((method, path, body))
        parts = path.split('/')
        if path.endswith('?per_page=1') and path.startswith(f'repos/{REPO}/compare/'):
            return {'ahead_by': 1, 'commits': [{'sha': 'c' * 40}]}
        if path.startswith(f'repos/{REPO}/compare/{"c" * 40}...'):
            sha = path.rsplit('...', 1)[1]
            number = next(n for n, p in self.pulls.items() if p['head']['sha'] == sha)
            return {'behind_by': 0 if number in self.inherited else 1}
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
                assert body == {'expected_head_sha': pull['head']['sha']}
                if self.pushes.get(number):
                    self.pushes[number] -= 1
                    self.push(number, pull)
                    raise failure()
                outcome = self.update.get(number, 'merge')
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
            if self.read_push.pop(number, False):
                self.push(number, pull)
            if 'pending' in pull:
                if self.settle_pushes.get(number):
                    self.settle_pushes[number] -= 1
                    pull.pop('pending')
                    self.push(number, pull)
                elif self.lag[number] == 0:
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

    def test_silent_retarget_uses_the_latest_explicit_dependency_event(self):
        event = landed()['pull_request']
        older = node(2, base='main', createdAt='2026-10-19T00:00:00Z',
                     moved=moved(previous='main', current='feat/parent'))
        self.assertTrue(d.dependent(older, event, REPO))
        for change in [moved(previous='main', current='feat/other'),
                       moved(previous='main', current='feat/parent', at='2026-10-19T00:00:00Z'),
                       moved(previous='main', current='feat/parent', at='2026-10-27T00:00:00Z')]:
            with self.subTest(change=change):
                self.assertFalse(d.dependent(node(3, base='main', moved=change), event, REPO))

    def test_auto_retarget_candidate_requires_recent_same_repository_main_target(self):
        event = landed()['pull_request']
        recent = node(2, base='main', updatedAt='2026-10-26T22:52:06Z')
        self.assertTrue(d.auto_retarget_candidate(recent, event, REPO))
        for candidate in [node(3, base='main'), node(4, base='main', createdAt='2026-10-19T00:00:00Z',
                                                      updatedAt='2026-10-26T22:52:06Z'),
                          node(7, base='main', createdAt='2026-10-26T22:52:33Z',
                               updatedAt='2026-10-26T22:52:34Z'),
                          node(5, base='main', updatedAt='2026-10-26T22:52:06Z', mergeQueueEntry={'id': 'queued'}),
                          node(6, base='main', updatedAt='2026-10-26T22:52:06Z',
                               headRepository={'nameWithOwner': 'fork/r'})]:
            self.assertFalse(d.auto_retarget_candidate(candidate, event, REPO))

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

    def test_older_pr_silently_retargeted_from_a_new_dependency_gets_main_and_checks(self):
        github = GitHub(node(2, base='main', createdAt='2026-10-19T00:00:00Z',
                             moved=moved(previous='main', current='feat/parent')))
        record, _ = github.run(landed())
        self.assertEqual(record['outcomes'], {'2': 'updated_and_checked'})
        self.assertEqual(github.writes('pulls/2/update-branch'), [
            (f'repos/{REPO}/pulls/2/update-branch', {'expected_head_sha': node(2)['headRefOid']})])
        self.assertEqual(len(github.writes('dispatches')), 2)
        self.assertFalse(any('?per_page=1' in path for _, path, _ in github.calls))

    def test_auto_retarget_without_timeline_event_updates_only_inherited_history(self):
        github = GitHub(node(2, base='main', isDraft=True, updatedAt='2026-10-26T22:52:06Z'),
                        node(3, base='main', updatedAt='2026-10-26T22:52:06Z'))
        github.inherited.add(2)
        record, _ = github.run(landed())
        self.assertEqual(record['outcomes'], {'2': 'updated'})
        self.assertEqual(github.writes('pulls/2/update-branch'), [
            (f'repos/{REPO}/pulls/2/update-branch', {'expected_head_sha': node(2)['headRefOid']})])
        self.assertEqual(github.writes('pulls/3/update-branch'), [])
        self.assertEqual(github.writes('dispatches'), [])
        comparisons = [path for _, path, _ in github.calls if '/compare/' in path and not '/compare/main...' in path]
        self.assertEqual(comparisons, [
            f'repos/{REPO}/compare/{"b" * 40}...{"a" * 40}?per_page=1',
            f'repos/{REPO}/compare/{"c" * 40}...{node(2)["headRefOid"]}',
            f'repos/{REPO}/compare/{"c" * 40}...{node(3)["headRefOid"]}'])

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

    def test_a_retargeted_ready_branch_gets_checks_even_when_already_current(self):
        github = GitHub(node(2, isDraft=True), node(3), node(4, isDraft=True))
        github.behind.update({3: 0, 4: 0})
        record, _ = github.run(landed())
        self.assertEqual(record['outcomes'], {'2': 'updated', '3': 'current_and_checked', '4': 'current'})
        self.assertEqual([body for _, body in github.writes('dispatches')], [
            {'ref': 'feat/3', 'inputs': {'profile': 'fast'}}, {'ref': 'main', 'inputs': {'pull_request': '3'}}])
        self.assertEqual(github.writes('update-branch'), [(f'repos/{REPO}/pulls/2/update-branch', {'expected_head_sha': node(2)['headRefOid']})])

    def test_an_owner_push_during_the_update_is_kept_and_the_new_head_is_brought_current(self):
        github = GitHub(node(2), node(3), node(4))
        github.pushes = {2: 1, 3: 1}
        github.pushed_behind = {3: 0}
        github.read_push = {4: True}
        github.behind[4] = 0
        record, _ = github.run(landed())
        self.assertEqual(record['state'], 'reconciled')
        self.assertEqual(record['outcomes'], {'2': 'updated_and_checked', '3': 'current_and_checked', '4': 'current_and_checked'})
        requests = [body['expected_head_sha'] for _, body in github.writes('pulls/2/update-branch')]
        self.assertEqual(requests[0], node(2)['headRefOid'])
        self.assertEqual(len(requests), 2)
        self.assertNotEqual(requests[1], requests[0])
        self.assertEqual(github.writes('pulls/4/update-branch'), [])
        self.assertEqual(sorted(body['inputs'].get('pull_request', body['ref']) for _, body in github.writes('dispatches')),
                         ['2', '3', '4', 'feat/2', 'feat/3', 'feat/4'])

    def test_owner_push_after_accepted_update_is_not_mistaken_for_the_main_merge(self):
        github = GitHub(node(2))
        github.settle_pushes = {2: 1}
        record, _ = github.run(landed())
        self.assertEqual(record['state'], 'reconciled')
        self.assertEqual(record['outcomes'], {'2': 'updated_and_checked'})
        requests = [body['expected_head_sha'] for _, body in github.writes('pulls/2/update-branch')]
        self.assertEqual(len(requests), 2)
        self.assertNotEqual(requests[0], requests[1])
        self.assertEqual(len(github.writes('dispatches')), 2)

    def test_repeated_owner_pushes_after_accepted_updates_leave_checks_undispatched(self):
        github = GitHub(node(2))
        github.settle_pushes = {2: d.ATTEMPTS}
        record, _ = github.run(landed())
        self.assertEqual(record['state'], 'incomplete')
        self.assertEqual(record['outcomes'], {'2': 'contended'})
        self.assertEqual(github.writes('dispatches'), [])

    def test_a_branch_that_keeps_moving_fails_the_run_instead_of_reporting_success(self):
        github = GitHub(node(2))
        github.pushes = {2: d.ATTEMPTS}
        record, _ = github.run(landed())
        self.assertEqual(record['state'], 'incomplete')
        self.assertEqual(record['outcomes'], {'2': 'contended'})
        self.assertEqual(len(github.writes('update-branch')), d.ATTEMPTS)
        self.assertEqual(github.writes('dispatches'), [])

    def test_a_conflict_is_labelled_for_the_owner(self):
        github = GitHub(node(2), node(3))
        github.update = {3: 'conflict'}
        github.mergeable = {3: False}
        record, _ = github.run(landed())
        self.assertEqual(record, {'state': 'reconciled', 'landed': 1, 'failed': [], 'held_runs_remaining': [],
                                  'outcomes': {'2': 'updated_and_checked', '3': 'conflict'}})
        self.assertEqual(github.writes('issues/2'), [])
        labels, comment = github.writes('issues/3')
        self.assertEqual(labels, (f'repos/{REPO}/issues/3/labels', {'labels': [d.LABEL]}))
        self.assertIn('#1 landed on `main`', comment[1]['body'])
        self.assertIn(f'`{d.LABEL}` label clears on that push', comment[1]['body'])
        self.assertFalse(any('3' in json.dumps(body) for _, body in github.writes('dispatches')))

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
