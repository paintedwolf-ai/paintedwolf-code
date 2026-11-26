"""CLI entrypoint for zero-model Portable Command execution, replay, suite runs, and envelope probing."""
import argparse
import json
import pathlib
import sys

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parents[1]
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from portable_command.harness import resolve_case, run_case
from portable_command.suite_runner import probe_command_envelope, run_suite


def build_parser():
    parser = argparse.ArgumentParser(description='Run, replay, probe, or sweep Portable Command fixtures through an isolated zero-model application.')
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument('--case', help='Frozen case ID from suite.json')
    group.add_argument('--replay', help='Case ID or path to a saved sanitized-fixture.json bundle')
    group.add_argument('--suite', action='store_true', help='Run all 40 Q01-Q22 cases (or --cases subset) and write suite-report.json')
    group.add_argument('--probe-envelope', action='store_true', help='Probe offered command tool schema and single-call progress_update envelope')
    parser.add_argument('--cases', help='Comma-separated case IDs to run with --suite')
    parser.add_argument('--out', type=pathlib.Path, required=True, help='Fresh output directory for the isolated run')
    parser.add_argument('--export', type=pathlib.Path, help='Optional directory for a sanitized replay & evidence bundle')
    return parser


def main(argv=None, *, runner=run_case, suite_runner=run_suite, envelope_prober=probe_command_envelope):
    args = build_parser().parse_args(argv)
    if args.probe_envelope:
        report = envelope_prober(args.out)
        summary = {
            'mode': 'probe-envelope',
            'session_id': report['session_id'],
            'schema_has_progress_update': report['schema_has_progress_update'],
            'command_schema_properties': report['command_schema_properties'],
            'envelope_status': report['envelope_status'],
            'native_envelope_qualified': report['native_envelope_qualified'],
            'sibling_fallback_used': report['sibling_fallback_used'],
        }
        print(json.dumps(summary, indent=2, sort_keys=True))
        return 0
    if args.suite:
        case_ids = [c.strip() for c in args.cases.split(',') if c.strip()] if args.cases else None
        report = suite_runner(args.out, case_ids=case_ids, export_dir=args.export)
        print(json.dumps({'mode': 'suite', 'summary': report['summary']}, indent=2, sort_keys=True))
        return 0 if report['summary']['failed'] == 0 else 1
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
