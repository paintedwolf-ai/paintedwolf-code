"""Execute candidate unittest coverage inside the grading container."""
import argparse
import json
import pathlib
import sys
import subprocess
import unittest


def changed_test_paths(root, baseline):
    return {path.relative_to(root).as_posix() for path in root.rglob('test*.py')
            if path.is_file() and (not (baseline / path.relative_to(root)).exists()
                or path.read_bytes() != (baseline / path.relative_to(root)).read_bytes())}


def tests_in(suite):
    for test in suite:
        if isinstance(test, unittest.TestSuite):
            yield from tests_in(test)
        else:
            yield test


def baseline_test_ids(baseline, pattern):
    result = subprocess.run([sys.executable, '-B', str(pathlib.Path(__file__).resolve()),
        '--list-tests', '--pattern', pattern], cwd=baseline, text=True, capture_output=True,
        check=True, timeout=10)
    return set(json.loads(result.stdout))


def mutate(root, baseline, mutation, default_limit, original_function):
    # Mutate only the disposable grading copy so subprocess-based tests see it too.
    if mutation:
        default = 8 if mutation == 'old-default' else default_limit
        key = '(-r["score"], r["name"])' if mutation == 'old-default' else '(r["score"], r["name"])'
        reverse = ', reverse=True' if mutation == 'reversed-ties' else ''
        body = (f'\ndef rank_profiles(records, limit={default}):\n'
                f'    return sorted(records, key=lambda r: {key}{reverse})[:limit]\n')
        target = root / 'ranking.py'
    elif original_function:
        module, function = original_function
        original = (baseline / (module + '.py')).read_text()
        body = (f'\n__original_namespace = {{}}\nexec({original!r}, __original_namespace)\n'
                f'{function} = __original_namespace[{function!r}]\n')
        target = root / (module + '.py')
    else:
        return
    with target.open('a') as stream:
        stream.write(body)


def execute(root, pattern, mutation=None, default_limit=8, baseline=None, new_only=False, original_function=None):
    root = root.resolve()
    sys.path.insert(0, str(root))
    mutate(root, baseline, mutation, default_limit, original_function)
    loader = unittest.TestLoader()
    suite = loader.discover(str(root), pattern=pattern)
    if new_only:
        existing = baseline_test_ids(baseline, pattern)
        suite = unittest.TestSuite(test for test in tests_in(suite) if test.id() not in existing)
    result = unittest.TextTestRunner(stream=sys.stderr).run(suite)
    return {'run': result.testsRun, 'failures': len(result.failures),
            'discovery_errors': len(loader.errors),
            'errors': len(result.errors), 'skipped': len(result.skipped),
            'expected_failures': len(result.expectedFailures),
            'unexpected_successes': len(result.unexpectedSuccesses)}


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--list-tests', action='store_true')
    parser.add_argument('--pattern', default='test*.py')
    parser.add_argument('--mutation', choices=['ascending', 'reversed-ties', 'old-default'])
    parser.add_argument('--default-limit', type=int, choices=[3, 8], default=8)
    parser.add_argument('--baseline', type=pathlib.Path)
    parser.add_argument('--new-only', action='store_true')
    parser.add_argument('--original-function', nargs=2, metavar=('MODULE', 'FUNCTION'))
    args = parser.parse_args()
    if args.list_tests:
        sys.path.insert(0, str(pathlib.Path.cwd()))
        suite = unittest.defaultTestLoader.discover(str(pathlib.Path.cwd()), pattern=args.pattern)
        print(json.dumps([test.id() for test in tests_in(suite)]))
    else:
        print(json.dumps(execute(pathlib.Path.cwd(), args.pattern, args.mutation, args.default_limit,
                                 args.baseline, args.new_only, args.original_function)))
