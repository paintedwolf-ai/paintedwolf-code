"""Select application targets, measurement operations, and roster revisions."""
import copy
import json
import os
import pathlib
import re
import subprocess
import tempfile
import uuid
from snapshot import ROOT, file_hash, identity
from built_runtime import verify_application
from release_policy import decision

CONTRACT = 'lycaon/internal/harnessfixture/contract.json'


def selected_operations(manifest, mode='exploration', tier='all', cases=None):
    operations = manifest['operations']
    selected = [op['id'] for op in operations if
                (op['role'] == 'calibration' if mode == 'calibration' else
                 op['role'] == 'scored' and tier in {'all', op.get('tier', 'gate')})]
    if cases is not None:
        requested = set(cases.split(','))
        if not requested or '' in requested or not requested <= set(selected):
            raise ValueError('case selection must name operations in the selected mode and tier')
        selected = [name for name in selected if name in requested]
    if not selected:
        raise ValueError('benchmark selection has no operations')
    return selected


def git(root, *args):
    return subprocess.check_output(['git', *args], cwd=root)


def resolve(root, ref):
    if not ref or ref.startswith('-'):
        raise ValueError('select an application tag or full commit identity')
    tag = 'refs/tags/' + ref.removeprefix('refs/tags/')
    tagged = subprocess.run(['git', 'show-ref', '--verify', '--quiet', tag], cwd=root).returncode
    if tagged not in (0, 1):
        raise ValueError('could not inspect the selected tag')
    if tagged and not re.fullmatch(r'[0-9a-f]{40}|[0-9a-f]{64}', ref):
        raise ValueError('application revisions must be tags or full commit identities; branches are mutable')
    selected = tag if not tagged else ref
    commit = git(root, 'rev-parse', '--verify', selected + '^{commit}').decode().strip()
    tree = git(root, 'rev-parse', commit + '^{tree}').decode().strip()
    return {'ref': ref, 'revision': commit, 'tree': tree, 'dirty': False, 'kind': 'tag' if not tagged else 'commit'}


def working_tree(root):
    commit = subprocess.check_output(['bash', 'scripts/repo-snapshot-lock.sh', 'read', '--',
                                     'bash', 'scripts/test-source-snapshot.sh', 'capture'], cwd=root, text=True).strip()
    parent = git(root, 'rev-parse', commit + '^').decode().strip()
    tree = git(root, 'rev-parse', commit + '^{tree}').decode().strip()
    clean = git(root, 'rev-parse', parent + '^{tree}').decode().strip() == tree
    return {'ref': None, 'revision': parent, 'snapshot': commit, 'tree': tree,
            'dirty': not clean, 'kind': 'worktree'}


def read_file(root, revision, path):
    return git(root, 'show', revision['tree'] + ':' + path)


def contract(root, revision):
    value = json.loads(read_file(root, revision, CONTRACT))
    if (set(value) != {'revision', 'benchmark_paths', 'harness_paths'}
            or type(value['revision']) is not int or value['revision'] < 1):
        raise ValueError('unsupported application evaluation contract')
    for group in ('benchmark_paths', 'harness_paths'):
        if not isinstance(value[group], list) or not value[group]:
            raise ValueError('evaluation contract requires explicit source boundaries')
        for name in value[group]:
            if not isinstance(name, str):
                raise ValueError('evaluation source boundary requires a relative path')
            path = pathlib.PurePosixPath(name)
            if path.is_absolute() or '..' in path.parts or name in {'.', ''}:
                raise ValueError('evaluation source boundary escapes the application')
    return value


def export(root, revision, destination):
    names = git(root, 'ls-tree', '-r', '--name-only', '-z', revision['tree'])
    destination.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory() as temporary:
        environment = {**os.environ, 'GIT_INDEX_FILE': str(pathlib.Path(temporary) / 'index')}
        subprocess.run(['git', 'read-tree', revision['tree']], cwd=root, env=environment, check=True)
        subprocess.run(['git', 'checkout-index', '--stdin', '-z', '--prefix=' + str(destination) + '/'],
                       cwd=root, env=environment, input=names, check=True)
    return names.decode().rstrip('\0').split('\0')


def replace_candidate(plan, removed, replacement):
    previous = next(m for m in plan['models'] if m['id'] == removed)
    if replacement['id'] in {m['id'] for m in plan['models']}:
        raise ValueError('a replacement needs a new candidate identity')
    common = lambda config: {k: v for k, v in config.items() if k not in {'coordinator', 'configuration_sha256'}}
    if common(replacement['configuration']) != common(previous['configuration']):
        raise ValueError('replacement changes the application or supporting models')
    revised = copy.deepcopy(plan)
    revised['models'] = [copy.deepcopy(replacement) if m['id'] == removed else m for m in revised['models']]
    public_ref = {k: v for k, v in replacement.items() if k != 'configuration'}
    revised['roster']['models'] = [public_ref if m['id'] == removed else m for m in revised['roster']['models']]
    policy = revised['execution']['policy']
    mapping = {p: resource['id'] for resource in policy['resources'] for p in resource['providers']}
    config = replacement['configuration']
    refs = [config['coordinator'], config['lite'], *config['workers']]
    if any(ref['provider'] not in mapping for ref in refs):
        raise ValueError('replacement requires an unplanned provider capacity')
    policy['models'].pop(removed)
    policy['models'][replacement['id']] = {'coordinator': mapping[refs[0]['provider']],
        'resources': sorted({mapping[ref['provider']] for ref in refs})}
    used = {resource for model in policy['models'].values() for resource in model['resources']}
    policy['resources'] = [r for r in policy['resources'] if r['id'] in used]
    next_index = max(item['index'] for item in revised['episodes']) + 1
    for item in revised['episodes']:
        if item['model'] == removed:
            item['model'], item['index'] = replacement['id'], next_index
            next_index += 1
    revised['benchmark']['execution'] = copy.deepcopy(revised['execution'])
    revised['comparison_id'] = identity({'benchmark': revised['benchmark'],
                                         'application_configuration': common(replacement['configuration'])})
    revised['run_id'] = str(uuid.uuid4())
    return revised


def retained_records(previous, revised, records):
    old_items = {str(i['index']): i for i in previous['episodes']}
    retained = {}
    for item in revised['episodes']:
        key = str(item['index'])
        if key not in records:
            continue
        if old_items.get(key) != item:
            raise ValueError('completed capture identity changed')
        if 'error' in records[key]:
            raise ValueError('an unresolved attempt cannot become completed')
        retained[key] = copy.deepcopy(records[key])
    return retained


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
