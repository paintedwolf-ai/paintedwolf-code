"""Bind every grading revision to its unchanged paid execution."""
import pathlib
import uuid
import shutil
import tempfile
from snapshot import identity, file_hash

GRADING_FILES = {'benchmark.json', 'built_runtime.py', 'snapshot.py', 'grading_state.py', 'candidate_process.py', 'execution_evidence.py', 'allowance_evidence.py', 'coordinator_evidence.py', 'fixture_contracts.py', 'fixture-contracts.json', 'sampling.py', 'oracle_process.py', 'focused_outcomes.py', 'focused_evidence.py', 'report.py', 'grade.py', 'outcomes.py', 'candidate_tests.py', 'candidate_cli.py', 'unittest_structure.py', 'worker_evidence.py',
                 'cost_evidence.py', 'verification_evidence.py', 'episode_evidence.py', 'workflow_evidence.py', 'grading_identity.py', 'artifacts.py', 'measurement_cache.py',
                 'ledger.py', 'timestamps.py', 'dispatch_graph.py', 'orchestration_evidence.py', 'decision_evidence.py', 'capability_evidence.py', 'closeout_evidence.py', 'structure.py', 'requirements.txt'}


def implementation_files(root):
    return {p.name: file_hash(p) for p in root.iterdir()
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
