"""Select application and evaluator identities before build or credential access."""
import json
import pathlib
from snapshot import ROOT, file_hash
from source_revision import resolve, working_tree, read_file
from built_runtime import verify_application
from release_policy import decision


def add_arguments(parser):
    group = parser.add_mutually_exclusive_group()
    group.add_argument('--application-ref', help='application tag or full commit identity')
    group.add_argument('--application-worktree', action='store_true', help='explicit development target; cannot publish release scores')
    group.add_argument('--prepared', type=pathlib.Path, help='verified preparation made by BENCHMARK=prepare')
    group.add_argument('--repair-from', type=pathlib.Path, help='retain this run or preparation as the application target for a new execution')
    harness = parser.add_mutually_exclusive_group()
    harness.add_argument('--harness-ref', help='evaluation-only harness repair tag or full commit identity')
    harness.add_argument('--harness-worktree', action='store_true', help='test an evaluation-only harness repair from this checkout')
    parser.add_argument('--reason', help='required explanation when repairing an existing run')


def select(args):
    harness_worktree = args.harness_worktree
    benchmark_ref = getattr(args, 'benchmark_ref', None)
    if args.prepared:
        if args.harness_ref or harness_worktree or args.reason or benchmark_ref:
            raise ValueError('prepared inputs are frozen; use --repair-from to change the evaluator')
        return {'prepared': str(args.prepared.resolve())}
    if not any((args.application_ref, args.application_worktree, args.repair_from)):
        raise ValueError('select --application-ref, --application-worktree, --prepared, or --repair-from')
    if bool(args.repair_from) != bool(args.reason and args.reason.strip()):
        raise ValueError('--repair-from requires --reason; a reason belongs to an explicit repair')
    if args.reason and len(args.reason.strip()) > 2048:
        raise ValueError('repair reason must be at most 2048 characters')
    current = working_tree(ROOT) if not benchmark_ref or args.application_worktree or harness_worktree else None
    benchmark = resolve(ROOT, benchmark_ref) if benchmark_ref else current
    selection = {'benchmark': benchmark}
    if args.repair_from:
        previous = args.repair_from.resolve()
        source = previous / 'source'
        application = verify_application(source)
        evaluation = json.loads((source / 'evaluation-source.json').read_text())
        selection.update(application=application['target'], harness=evaluation['harness_source'],
                         retained_source=str(source), replace_harness=bool(args.harness_ref or harness_worktree))
        plan_path = previous / 'plan.json'
        parent = json.loads(plan_path.read_text()) if plan_path.exists() else None
        selection['lineage'] = {'reason': args.reason.strip(), 'application_source_sha256': application['source_sha256'],
                                'previous_evaluation_sha256': file_hash(source / 'evaluation.json'),
                                'previous_execution_id': parent['run_id'] if parent else None,
                                'previous_plan_sha256': file_hash(plan_path) if parent else None}
    else:
        selection['application'] = current if args.application_worktree else resolve(ROOT, args.application_ref)
    if args.harness_ref:
        selection['harness'] = resolve(ROOT, args.harness_ref)
    elif harness_worktree:
        selection['harness'] = current
    application = selection['application']
    if args.repair_from:
        version = verify_application(pathlib.Path(selection['retained_source']))['version']
    else:
        version = read_file(ROOT, application, 'VERSION').decode().strip()
    selection['version'] = version
    decision(version)
    if getattr(args, 'mode', None) == 'release' and application['kind'] == 'worktree':
        raise ValueError('release scores require an application tag or full commit identity')
    return selection
