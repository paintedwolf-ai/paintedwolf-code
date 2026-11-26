"""CLI entrypoint for zero-model Portable Command execution and second-process replay."""
import argparse
import json
import pathlib
import sys

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parents[1]
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from portable_command.harness import resolve_case, run_case


def build_parser():
    parser = argparse.ArgumentParser(description='Run or replay one Portable Command fixture through an isolated zero-model application.')
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument('--case', help='Frozen case ID from suite.json')
    group.add_argument('--replay', help='Case ID or path to a saved sanitized-fixture.json bundle')
    parser.add_argument('--out', type=pathlib.Path, required=True, help='Fresh output directory for the isolated run')
    parser.add_argument('--export', type=pathlib.Path, help='Optional directory for a sanitized replay & evidence bundle')
    return parser


def main(argv=None, *, runner=run_case):
    args = build_parser().parse_args(argv)
    target = args.case or args.replay
    case = resolve_case(target)
    result = runner(case, args.out, export_dir=args.export)
    summary = {
        'mode': 'replay' if args.replay else 'run',
        'case_id': result['case_id'],
        'session_id': result['session_id'],
        'qualification': result['grade']['qualification'],
        'passed': result['grade']['passed'],
        'dimensions': result['grade']['dimensions'],
    }
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if result['grade']['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
