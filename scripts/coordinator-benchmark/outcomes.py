"""Independent CLI outcome oracles. Run only inside the isolated grading container."""
import argparse
from unittest_structure import preserved_definitions
from contextlib import contextmanager
import json
import pathlib
import random
import shutil
import subprocess
import sys
import tempfile

PROJECT = pathlib.Path('/candidate')
BASELINE = pathlib.Path('/baseline')
checks = []


def check(name, predicate, detail=''):
    checks.append({'id': name, 'passed': bool(predicate), 'detail': detail})


def same_json(observed, expected):
    if type(observed) is not type(expected):
        return False
    if isinstance(expected, dict):
        return observed.keys() == expected.keys() and all(same_json(observed[k], v) for k, v in expected.items())
    if isinstance(expected, list):
        return len(observed) == len(expected) and all(same_json(a, b) for a, b in zip(observed, expected))
    return observed == expected


@contextmanager
def workspace():
    with tempfile.TemporaryDirectory() as temporary:
        target = pathlib.Path(temporary) / 'work'
        shutil.copytree(PROJECT, target)
        yield target


def invoke(args):
    try:
        with workspace() as work:
            runner = pathlib.Path(__file__).with_name('candidate_cli.py')
            receipt = work.parent / 'completion.json'
            p = subprocess.run(['python3', '-B', str(runner), str(receipt), *args], cwd=work,
                               text=True, capture_output=True, timeout=8)
            result = json.loads(receipt.read_text())
        return (p.returncode if result['uncaught_exception'] is False else -1, p.stdout, p.stderr)
    except (OSError, ValueError, KeyError, TypeError):
        return -1, '', 'candidate command did not produce a completion record'


def document(value, args=()):
    with tempfile.NamedTemporaryFile('w', suffix='.json') as f:
        json.dump(value, f)
        f.flush()
        return invoke([f.name, *args])


def rejects(value, args=()):
    code, out, err = document(value, args)
    return code > 0 and out.strip() == '' and bool(err.strip())


def parsed(result):
    code, out, _ = result
    try:
        return json.loads(out) if code == 0 else None
    except (ValueError, TypeError):
        return None


def test_result(pattern='test*.py', mutation=None, default_limit=8, new_only=False, original_function=None):
    runner = pathlib.Path(__file__).with_name('candidate_tests.py')
    try:
        with workspace() as work:
            command = ['python3', '-B', str(runner), '--pattern', pattern, '--baseline', str(BASELINE.resolve())]
            if new_only:
                command.append('--new-only')
            if original_function:
                command.extend(['--original-function', *original_function])
            if mutation:
                command.extend(['--mutation', mutation, '--default-limit', str(default_limit)])
            p = subprocess.run(command, cwd=work, text=True, capture_output=True, timeout=20)
        result = json.loads(p.stdout) if p.returncode == 0 else None
        fields = {'discovery_errors', 'run', 'failures', 'errors', 'skipped', 'expected_failures', 'unexpected_successes'}
        return result if isinstance(result, dict) and set(result) == fields and all(
            type(value) is int and value >= 0 for value in result.values()) else None
    except ValueError:
        return None


def successful_tests(result):
    return bool(result and result['run'] > 0 and all(result[key] == 0 for key in
                ('discovery_errors', 'failures', 'errors', 'skipped', 'expected_failures', 'unexpected_successes')))


def detects_mutation(result):
    return bool(result and result['run'] > 0 and result['failures'] + result['errors'] > 0
                and all(result[key] == 0 for key in
                        ('discovery_errors', 'skipped', 'expected_failures', 'unexpected_successes')))


def preserved_tests(byte_identical=False):
    for original in BASELINE.rglob('test*.py'):
        target = PROJECT / original.relative_to(BASELINE)
        try:
            valid = (original.read_bytes() == target.read_bytes() if byte_identical else
                     preserved_definitions(original.read_text(), target.read_text()))
            check('preserved-' + original.name, valid)
        except OSError as exc:
            check('preserved-' + original.name, False, str(exc))
    result = test_result()
    check('candidate-tests', successful_tests(result), json.dumps(result))


def regression_coverage(default_limit):
    result = test_result(new_only=True)
    check('new-regressions-pass', successful_tests(result), json.dumps(result))
    mutations = ['ascending', 'reversed-ties']
    if default_limit == 3:
        mutations.append('old-default')
    for mutation in mutations:
        result = test_result(mutation=mutation, default_limit=default_limit, new_only=True)
        detected = detects_mutation(result)
        check('regression-detects-' + mutation, detected, json.dumps(result))


def rank_observation(records, limit=None, positional=False):
    argument = '' if limit is None else (', ' if positional else ', limit=') + str(limit)
    program = ('import json\nfrom ranking import rank_profiles\n'
               f'records = {records!r}\n'
               f'result = rank_profiles(records{argument})\n'
               'print(json.dumps({"result": result, "input": records}))\n')
    try:
        with workspace() as work:
            p = subprocess.run(['python3', '-B', '-c', program], cwd=work,
                               text=True, capture_output=True, timeout=3)
        return json.loads(p.stdout) if p.returncode == 0 else None
    except ValueError:
        return None


def report_text(records):
    return ('Service          Score\n---------------- -----\n'
            + ''.join(f"{r['name']:<16} {r['score']:>5}\n" for r in records)
            + f'Total: {len(records)}\n')


def ranking(default_limit=8):
    rng = random.Random(9173)
    inputs = []
    for case in range(5):
        records = [{'name': f'service-{i:02}', 'score': rng.choice([0, 42, 93, 100])}
                   for i in range(case * 4)]
        rng.shuffle(records)
        inputs.append(records)
    inputs.append([
        {'name': 'a', 'score': 42, 'metadata': {'tag': ['keep']}},
        {'name': 'A', 'score': 42}, {'name': 'a', 'score': 42, 'tag': 'second'},
        {'name': '', 'score': 100}, {'name': 'é', 'score': 42},
    ])
    for case, records in enumerate(inputs):
        ordered = sorted(records, key=lambda r: (-r['score'], r['name']))
        for limit in [None, 0, 1, 7, len(records) + 4]:
            expected = ordered[:default_limit if limit is None else limit]
            check(f'ranking-{case}-limit-{limit}',
                  same_json(rank_observation(records, limit), {'result': expected, 'input': records}))
            if limit is not None:
                check(f'ranking-{case}-positional-limit-{limit}', same_json(
                    rank_observation(records, limit, positional=True), {'result': expected, 'input': records}))
        code, out, err = document(records)
        check(f'cli-{case}-default-limit', code == 0 and out == report_text(ordered[:default_limit]) and not err)
    invalid = [{}, [1], [{'name': 'x', 'score': True}], [{'name': 'x', 'score': -1}],
               [{'name': 'x', 'score': 101}], [{'name': 1, 'score': 2}], [{'name': 'x'}],
               [{'name': 'x', 'score': 1.5}], [{'score': 2}], [{'name': False, 'score': 1}]]
    for index, value in enumerate(invalid):
        check('preserved-validation-' + str(index), rejects(value))


def service(feature):
    expected = ('Service          Score\n---------------- -----\namber               93\n'
                'coral               93\nblue                42\nTotal: 3\n')
    code, out, err = invoke(['sample.json'])
    check('original-cli', code == 0 and out == expected and not err)
    if not feature:
        return
    records = [{'name': 'blue', 'score': 42}, {'name': 'coral', 'score': 93},
               {'name': 'amber', 'score': 93}]
    cases = [([], [records[2], records[1], records[0]]),
             (['--min-score', '50', '--limit', '1'], [records[2]]),
             (['--min-score', '100'], []), (['--min-score', '93'], [records[2], records[1]])]
    for i, (args, expected) in enumerate(cases):
        check('selection-' + str(i), parsed(document(records, ['--json', *args])) == expected)
    many = [{'name': str(i), 'score': i} for i in range(15)]
    check('default-limit', parsed(document(many, ['--json'])) == list(reversed(many))[:8])
    check('explicit-limit', parsed(document(many, ['--json', '--limit', '12'])) == list(reversed(many))[:12])
    for option, value in [('--min-score', '-1'), ('--min-score', '101'), ('--min-score', '1.5'),
                          ('--limit', '0'), ('--limit', '-1'), ('--limit', 'wat')]:
        check(option + '-' + value, rejects(records, [option, value]))
    for i, value in enumerate([{}, [1], [{'name': 'x', 'score': True}], [{'name': 'x', 'score': -1}],
                               [{'name': 'x', 'score': 101}], [{'name': 1, 'score': 2}], [{'name': 'x'}]]):
        check('invalid-record-' + str(i), rejects(value))
    code, out, err = invoke(['--help'])
    check('help-exit', code == 0 and bool(out.strip()))


def plan(jobs, parallelism):
    remaining = {j['id']: j for j in jobs}
    completed, stages = set(), []
    while remaining:
        ready = sorted((j for j in remaining.values() if set(j['depends_on']) <= completed),
                       key=lambda j: (-j['priority'], j['id']))[:parallelism]
        if not ready:
            raise ValueError('cycle')
        stage = [j['id'] for j in ready]
        stages.append(stage)
        completed.update(stage)
        for name in stage:
            del remaining[name]
    return {'stages': stages}


def planner():
    rng = random.Random(72831)
    for case in range(8):
        jobs = []
        for i in range(case * 3):
            jobs.append({'id': f'job-{i:02}', 'priority': rng.randint(-4, 4),
                         'depends_on': [j['id'] for j in jobs if rng.random() < .13]})
        rng.shuffle(jobs)
        for limit in [1, 2, 4]:
            check(f'dag-{case}-parallelism-{limit}',
                  parsed(document(jobs, ['--parallelism', str(limit)])) == plan(jobs, limit))
        check(f'dag-{case}-default', parsed(document(jobs)) == plan(jobs, 2))
    job = {'id': 'a', 'priority': 0, 'depends_on': []}
    bad = [None, {}, [None], [{**job, 'id': ''}], [{**job, 'id': 2}], [job, job],
           [{**job, 'priority': True}], [{**job, 'priority': 2.5}],
           [{**job, 'depends_on': 'a'}], [{**job, 'depends_on': [2]}],
           [{**job, 'depends_on': ['a']}], [{**job, 'depends_on': ['missing']}],
           [{**job, 'depends_on': ['b', 'b']}, {**job, 'id': 'b'}],
           [{**job, 'depends_on': ['b']}, {**job, 'id': 'b', 'depends_on': ['a']}],
           [{**job, 'unknown': 1}], [{'id': 'a', 'priority': 1}],
           [{'id': 'a', 'depends_on': []}], [{'priority': 1, 'depends_on': []}]]
    for i, value in enumerate(bad):
        check('invalid-document-' + str(i), rejects(value))
    for value in ['0', '-1', '1.5', 'wat']:
        check('invalid-parallelism-' + value, rejects([job], ['--parallelism', value]))
    code, out, err = invoke(['--help'])
    check('help-exit', code == 0 and bool(out.strip()))


def orientation():
    expected = {'entry_file': 'app.py', 'pipeline': ['source', 'ranking', 'assembly'],
                'score_order': 'ascending', 'tie_order': 'ascending', 'default_limit': 8,
                'mutates_input': False, 'unused_module': 'collector'}
    try:
        observed = json.loads((PROJECT / 'analysis.json').read_text())
        check('grounded-analysis', same_json(observed, expected))
    except (OSError, ValueError) as exc:
        check('grounded-analysis', False, str(exc))
    for original in BASELINE.rglob('*'):
        if original.is_file():
            target = PROJECT / original.relative_to(BASELINE)
            check('preserved-' + str(original.relative_to(BASELINE)),
                  target.is_file() and original.read_bytes() == target.read_bytes())


def revision():
    records = [{'name': 'amber', 'score': 40}, {'name': 'Amber', 'score': 95},
               {'name': 'amethyst', 'score': 90}, {'name': 'blue', 'score': 99}]
    for prefix, expected in [('am', [records[2], records[0]]), ('A', [records[1]]),
                              ('missing', []), ('', [records[3], records[1], records[2], records[0]])]:
        check('prefix-' + repr(prefix), parsed(document(records, ['--json', '--name-prefix', prefix])) == expected)
    check('combined-filters', parsed(document(records, ['--json', '--name-prefix', 'am',
                                                       '--min-score', '50', '--limit', '1'])) == [records[2]])


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('case', choices=['repair', 'service', 'planner', 'orientation', 'revision', 'changed-limit'])
    args = parser.parse_args()
    if args.case == 'orientation':
        orientation()
    else:
        preserved_tests()
    if args.case == 'planner':
        planner()
    elif args.case != 'orientation':
        if args.case in {'repair', 'changed-limit'}:
            ranking(3 if args.case == 'changed-limit' else 8)
            regression_coverage(3 if args.case == 'changed-limit' else 8)
        service(args.case in {'service', 'revision'})
        if args.case == 'revision':
            revision()
    print(json.dumps({'passed': all(c['passed'] for c in checks), 'checks': checks}, indent=2))


if __name__ == '__main__':
    try:
        main()
    except subprocess.TimeoutExpired:
        print(json.dumps({'measurement_error': {'kind': 'oracle_timeout'}}))
