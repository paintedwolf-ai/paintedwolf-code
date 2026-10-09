"""Reviewed, expiring exceptions for exact top-level Go tests; never auto-quarantine failures."""
from datetime import date
import json
import os
from pathlib import Path
import re
import subprocess

POLICY = Path(__file__).with_name('quarantine.json')


def entries(today=None):
    today = today or date.today()
    values = json.loads(POLICY.read_text())
    if not isinstance(values, list):
        raise ValueError('quarantine must be a list')
    seen = set()
    for row in values:
        if set(row) != {'package', 'test', 'issue', 'owner', 'expires'}:
            raise ValueError('quarantine entries require package, test, issue, owner, and expires')
        if (not re.fullmatch(r'github.com/lycaon/lycaon/[A-Za-z0-9_/.-]+', row['package'])
                or not re.fullmatch(r'Test[A-Za-z0-9_]+', row['test'])
                or not re.fullmatch(r'https://github.com/paintedwolf-ai/paintedwolf-code/issues/[1-9][0-9]*', row['issue'])
                or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9-]*', row['owner'])):
            raise ValueError('quarantine must name an exact package/test, tracking issue, and GitHub owner')
        if date.fromisoformat(row['expires']) < today:
            raise ValueError(f"expired quarantine for {row['package']}/{row['test']}: {row['issue']}")
        key = (row['package'], row['test'])
        if key in seen:
            raise ValueError('duplicate quarantine entry')
        seen.add(key)
    return values


def arguments(package, original, observe=False):
    rows = entries()
    tests = [row['test'] for row in rows if row['package'] == package]
    if observe or not tests:
        return original
    arguments = list(original)
    skipped = '^(' + '|'.join(tests) + ')$'
    for index, arg in enumerate(arguments):
        if arg.startswith('-test.skip='):
            arguments[index] = arg + '|' + skipped
            break
    else:
        arguments.append('-test.skip=' + skipped)
    return arguments


def main():
    failed = False
    for row in entries():
        print(f"Quarantined test: {row['package']}/{row['test']}; owner {row['owner']}; expires {row['expires']}; {row['issue']}", flush=True)
        package = './' + row['package'].removeprefix('github.com/lycaon/lycaon/')
        result = subprocess.run(['./task', 'test:full', '--', package, '-run=^' + row['test'] + '$'],
                                env={**os.environ, 'PW_QUARANTINE_OBSERVE': '1'})
        failed = failed or result.returncode != 0
    return int(failed)


if __name__ == '__main__':
    raise SystemExit(main())
