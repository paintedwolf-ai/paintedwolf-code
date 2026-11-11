"""Bind every grading revision, measurement cache, and report lease to its unchanged paid execution."""
from contextlib import contextmanager
import fcntl
import json
import pathlib
import shutil
import tempfile
import uuid
from progress import save
from snapshot import identity, file_hash

GRADING_FILES = {'benchmark.json', 'built_runtime.py', 'snapshot.py', 'candidate_process.py', 'fixture_contracts.py',
                 'fixture-contracts.json', 'sampling.py', 'focused_outcomes.py', 'focused_evidence.py', 'report.py',
                 'grade.py', 'outcomes.py', 'candidate_tests.py', 'candidate_cli.py', 'cost_evidence.py',
                 'verification_evidence.py', 'episode_evidence.py', 'grading_identity.py', 'artifacts.py',
                 'ledger.py', 'orchestration_evidence.py', 'requirements.txt'}
GRADING_FILES |= {'portable_command/' + name + '.py' for name in ('__init__', 'contracts', 'artifacts', 'grading', 'live')}


def implementation_files(root):
    return {p.relative_to(root).as_posix(): file_hash(p) for p in root.rglob('*')
            if (p.suffix == '.py' and not p.name.startswith('test_')) or p.name in {'benchmark.json', 'fixture-contracts.json', 'fixture-controls.json', 'requirements.txt'}}


def grading_hash(root):
    return identity({name: file_hash(root / name) for name in sorted(GRADING_FILES)})


def report_id(execution_id, digest):
    return str(uuid.uuid5(uuid.NAMESPACE_URL, f'paintedwolf-benchmark:{execution_id}:{digest}'))


def verify_grader(frozen, current, expected, regrade):
    original, selected = implementation_files(frozen), implementation_files(current)
    if identity(original) != expected:
        raise ValueError('frozen execution implementation changed')
    if selected != original and not regrade:
        raise ValueError('use the frozen report command, or explicitly select --regrade')
    execution = lambda files: {name: digest for name, digest in files.items() if name not in GRADING_FILES}
    if execution(original) != execution(selected):
        raise ValueError('regrading cannot change the execution implementation')
    return grading_hash(current)


def retain_grader(run, current, digest):
    parent = run / 'graders'
    parent.mkdir(exist_ok=True)
    destination = parent / digest
    if not destination.exists():
        with tempfile.TemporaryDirectory(prefix='.prepare-', dir=parent) as temporary:
            staged = pathlib.Path(temporary) / 'grader'
            staged.mkdir()
            for name in sorted(set(implementation_files(current)) | GRADING_FILES):
                (staged / name).parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(current / name, staged / name)
            if grading_hash(staged) != digest:
                raise ValueError('grader changed while preserving its source')
            try:
                staged.rename(destination)
            except OSError:
                if not destination.exists():
                    raise
    if (grading_hash(destination) != digest or implementation_files(destination) != implementation_files(current)
            or (destination / 'benchmark.json').read_bytes() != (current / 'benchmark.json').read_bytes()):
        raise ValueError('retained grader differs from its identity')
    return destination


@contextmanager
def report_lease(run):
    with (run / 'grading-report.lock').open('a') as lease:
        fcntl.flock(lease, fcntl.LOCK_EX)
        yield


def select_report(run, status, publish=False):
    """Select a completed receipt while its report lease is held."""
    if status.get('report_sha256'):
        source = pathlib.Path(status['report_path'])
        if not source.is_absolute() or source.is_symlink() or file_hash(source) != status['report_sha256']:
            raise ValueError('selected grading report differs from its receipt')
        result = json.loads(source.read_text())
        if result['run_id'] != status['report_id']:
            raise ValueError('selected grading report has a different revision')
        if publish:
            destination = run / ('public.json' if result['mode'] == 'release' else 'preview.json')
            save(destination, result)
    elif publish:
        for name in ('public.json', 'preview.json'):
            (run / name).unlink(missing_ok=True)
    save(run / 'grading-status.json', status)


def measure(capture, grading, inputs, evaluate):
    evidence = {str(path.relative_to(capture)): file_hash(path) for path in sorted(inputs)}
    key = identity({'grading': grading, 'evidence': evidence})
    path = capture / ('measurement-' + key + '.json')
    if path.exists():
        receipt = json.loads(path.read_text())
        if receipt['identity'] != key or receipt['result_sha256'] != identity(receipt['result']):
            raise ValueError('measurement receipt is corrupt')
        return receipt['result']
    result = evaluate()
    if result['outcome'] in {'passed', 'failed'}:
        save(path, {'identity': key, 'result': result, 'result_sha256': identity(result)})
    return result
