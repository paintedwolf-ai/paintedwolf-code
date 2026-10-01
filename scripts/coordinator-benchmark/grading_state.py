"""Serialize grading output and selected-report receipts."""
from contextlib import contextmanager
import fcntl
import json
import pathlib
from progress import save
from snapshot import file_hash


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
