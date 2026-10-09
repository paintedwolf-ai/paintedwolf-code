"""Bounded receipt parsing for trusted post-merge maintainability observations."""
import io
import json
import subprocess
import zipfile

from .github import pages, repository
from .budget_snapshot import validate

MAX_ARCHIVE_BYTES = 32 * 1024 * 1024
MAX_REPORT_BYTES = 8 * 1024 * 1024


def read_snapshot(run, tree):
    name = f"receipt-verification-check-limits-{run['run_attempt']}"
    candidates = [a for a in pages(f"{repository()}/actions/runs/{run['id']}/artifacts", 'artifacts')
                  if a['name'] == name and not a['expired']]
    if len(candidates) != 1:
        raise ValueError('expected one current-attempt qualification limits receipt')
    artifact = candidates[0]
    if not 0 < artifact['size_in_bytes'] <= MAX_ARCHIVE_BYTES:
        raise ValueError('limits receipt exceeds the download bound')
    raw = subprocess.check_output(['gh', 'api', f"{repository()}/actions/artifacts/{artifact['id']}/zip"])
    if len(raw) > MAX_ARCHIVE_BYTES:
        raise ValueError('limits receipt exceeds the archive bound')
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        members = [m for m in archive.infolist() if m.filename == 'reports/maintainability.json'
                   or m.filename.endswith('/reports/maintainability.json')]
        if len(members) != 1 or members[0].file_size > MAX_REPORT_BYTES:
            raise ValueError('expected one bounded maintainability report')
        report = json.loads(archive.read(members[0]))
    return validate(report, run['head_sha'], tree)
