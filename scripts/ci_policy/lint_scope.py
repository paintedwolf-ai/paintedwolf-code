"""Run Go analysis over an explicit scope, retaining whole-program deadcode."""
import argparse
from pathlib import Path
import subprocess
import sys

from verification_plan import go_selection, validate_selection

ROOT = Path(__file__).resolve().parents[2]


def commands(mode, binary, arguments):
    parser = argparse.ArgumentParser(add_help=False, allow_abbrev=False)
    parser.add_argument('--scope', action='append')
    options, flags = parser.parse_known_args(arguments)
    packages = options.scope or ['./...']
    if not go_selection(packages, 'Go lint')['shareable']:
        raise ValueError('Go lint scopes must name relative package directories within lycaon/')
    validate_selection(['test:digest'], packages)
    if mode not in {'fast', 'full'}:
        raise ValueError('Go lint mode must be fast or full')
    lint = [binary, 'run', *(['--fast-only'] if mode == 'fast' else []),
            *flags, *packages]
    # Full analysis includes govet over a wider build-tag set; fast-only analysis leaves it out.
    if mode == 'fast':
        return [['go', 'vet', *packages], lint]
    return [lint, ['bash', str(ROOT / 'scripts/deadcode-check.sh')]]


def main(arguments):
    mode, binary, *extra = arguments
    for command in commands(mode, binary, extra):
        result = subprocess.call(command, cwd=ROOT / 'lycaon')
        if result:
            return result
    return 0


if __name__ == '__main__':
    try:
        raise SystemExit(main(sys.argv[1:]))
    except ValueError as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(2)
