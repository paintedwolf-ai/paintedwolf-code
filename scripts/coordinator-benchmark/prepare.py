"""Prepare and verify a selected application for benchmarking without credentials or model calls."""
import argparse
import fcntl
import json
import os
import pathlib
import shutil
import sys

from built_runtime import verify_application, require_unadmitted
from grading_identity import implementation_files
from preparation import source_snapshot, build_application, validate_runtime, stage
from progress import save
from snapshot import file_hash, identity
from target_selection import add_arguments, select


def verify_prepared(root):
    marker = json.loads((root / 'prepared.json').read_text())
    if marker['version'] != 1:
        raise ValueError('unsupported benchmark preparation format')
    source = root / 'source'
    application = verify_application(source)
    evaluation = json.loads((source / 'evaluation.json').read_text())
    if marker['application'] != application or marker['evaluation'] != evaluation:
        raise ValueError('prepared application or evaluator changed')
    implementation = source / 'scripts/coordinator-benchmark'
    if marker['implementation_sha256'] != identity(implementation_files(implementation)):
        raise ValueError('prepared benchmark implementation changed')
    relative = pathlib.Path(marker['preflight']['receipt'])
    if relative.is_absolute() or len(relative.parts) < 2 or '..' in relative.parts:
        raise ValueError('prepared preflight receipt requires a relative path inside its directory')
    receipt = root / relative
    if not receipt.resolve().is_relative_to(root.resolve()) or file_hash(receipt) != marker['preflight']['sha256']:
        raise ValueError('prepared application preflight changed')
    validation = json.loads(receipt.read_text())
    if sorted(validation['cases']) != sorted(marker['cases']):
        raise ValueError('prepared control coverage differs from its validated receipt')
    return marker


def import_prepared(previous, destination):
    marker = verify_prepared(previous)
    source = destination / 'source'
    require_unadmitted(source)
    receipt = pathlib.Path(marker['preflight']['receipt'])
    ownership = destination / 'import.json'
    owned = {'source': str(previous.resolve()), 'preparation_sha256': identity(marker), 'preflight': receipt.parts[0]}
    if ownership.exists():
        if json.loads(ownership.read_text()) != owned:
            raise ValueError('interrupted import belongs to another preparation')
        for path in (source, destination / owned['preflight']):
            if path.is_symlink():
                raise ValueError('prepared import directory was replaced by a symlink')
            if path.exists():
                shutil.rmtree(path)
    elif source.exists() or (destination / owned['preflight']).exists():
        raise ValueError('prepared import requires an unused destination')
    save(ownership, owned)
    shutil.copytree(previous / 'source', source, symlinks=True,
                    ignore=shutil.ignore_patterns('__pycache__', '.task'))
    shutil.copytree(previous / receipt.parts[0], destination / receipt.parts[0], symlinks=True)
    save(destination / 'prepared.json', marker)
    save(destination / 'selection.json', json.loads((previous / 'selection.json').read_text()))
    if verify_prepared(destination) != marker or verify_prepared(previous) != marker:
        raise ValueError('prepared artifacts changed while importing')
    return marker


def prepare_target(root, selection, implementation, cases=None):
    if (root / 'prepared.json').exists():
        marker = verify_prepared(root)
        if marker['implementation_sha256'] != implementation:
            raise ValueError('preparation belongs to another benchmark revision; use its frozen runner or --repair-from')
        require_coverage(marker, cases)
        return marker
    if 'prepared' in selection:
        require_coverage(verify_prepared(pathlib.Path(selection['prepared'])), cases)
        marker = import_prepared(pathlib.Path(selection['prepared']), root)
        if marker['implementation_sha256'] != implementation:
            raise ValueError('use the selected preparation\'s frozen runner or --repair-from')
        require_coverage(marker, cases)
        return marker
    with stage(root, 'source_snapshot'):
        source_snapshot(root, selection)
    source = root / 'source'
    manifest = json.loads((source / 'scripts/coordinator-benchmark/benchmark.json').read_text())
    cases = sorted(set(cases if cases is not None else [op['id'] for op in manifest['operations']]))
    if not cases or not set(cases) <= {op['id'] for op in manifest['operations']}:
        raise ValueError('preparation requires operations from its frozen bank')
    if identity(implementation_files(source / 'scripts/coordinator-benchmark')) != implementation:
        entry = source / 'scripts/coordinator-benchmark/prepare.py'
        if selection['benchmark']['kind'] in {'tag', 'commit'} and entry.is_file():
            os.execv(sys.executable, [sys.executable, str(entry.with_name('python_runtime.py')), str(entry),
                                     '--resume', '--out', str(root)])
            raise RuntimeError('selected benchmark handoff returned')
        raise ValueError('selected benchmark differs from this runner; use the selected revision\'s runner')
    application = build_application(root, dict(os.environ))
    with stage(root, 'application_preflight'):
        preflight = validate_runtime(root, source, cases)
    verify_application(source)
    marker = {'version': 1, 'application': application,
              'evaluation': json.loads((source / 'evaluation.json').read_text()),
              'implementation_sha256': implementation, 'preflight': preflight,
              'lineage': selection.get('lineage'), 'cases': cases}
    save(root / 'prepared.json', marker)
    return verify_prepared(root)


def require_coverage(marker, cases):
    if cases is not None and not set(cases) <= set(marker['cases']):
        raise ValueError('prepared controls do not cover the selected bank; prepare the required operations first')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    add_arguments(parser)
    parser.add_argument('--benchmark-ref', help='benchmark tag or full commit identity; default is this checkout')
    parser.add_argument('--out', required=True, type=pathlib.Path)
    parser.add_argument('--resume', action='store_true', help='finish this preparation with its frozen selections')
    parser.add_argument('--cases', help='comma-separated operations; default is the complete bank')
    args = parser.parse_args()
    root = args.out.resolve()
    if args.resume and any((args.application_ref, args.application_worktree, args.prepared, args.repair_from,
                           args.harness_ref, args.harness_worktree, args.benchmark_ref, args.reason, args.cases)):
        raise ValueError('resume preserves its target and operation coverage; omit selectors')
    selection = json.loads((root / 'selection.json').read_text()) if args.resume else select(args)
    if not args.resume:
        selection['cases'] = args.cases.split(',') if args.cases is not None else None
    root.mkdir(parents=True, exist_ok=args.resume, mode=0o700)
    with (root / 'runner.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        marker = prepare_target(root, selection, identity(implementation_files(pathlib.Path(__file__).parent)),
                                selection.get('cases'))
    print('Prepared application ' + marker['application']['version'] + ' for benchmark execution: ' + str(root))


if __name__ == '__main__':
    main()
