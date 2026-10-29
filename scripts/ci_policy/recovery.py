"""Retry proven runner faults on admitted verification requests.

This runs only from the default branch. Artifacts are bounded JSON data, never
executed or extracted; fork code never runs with this workflow's write token.
"""
import io
import json
import os
from pathlib import Path
import subprocess
import zipfile

from .github import api, pages, repository
from .retry_evidence import permits_retry, current_group

MAX_ARTIFACT_BYTES = 32 * 1024 * 1024


def evidence(run):
    records, failures = [], []
    for artifact in pages(f"{repository()}/actions/runs/{run['id']}/artifacts", 'artifacts'):
        if not artifact['name'].startswith('receipt-'):
            continue
        if artifact['expired'] or artifact['size_in_bytes'] > MAX_ARTIFACT_BYTES:
            continue
        if not artifact['name'].endswith('-' + str(run['run_attempt'])):
            continue
        raw = subprocess.check_output(['gh', 'api', f"{repository()}/actions/artifacts/{artifact['id']}/zip"])
        if len(raw) > MAX_ARTIFACT_BYTES:
            continue
        with zipfile.ZipFile(io.BytesIO(raw)) as archive:
            for member in archive.infolist():
                if member.file_size > 1024 * 1024:
                    continue
                path = member.filename
                if not path.endswith(('ci/run.json', 'ci/failures.json')):
                    continue
                value = json.loads(archive.read(member))
                if path.endswith('ci/run.json') and isinstance(value, dict):
                    records.append(value)
                elif path.endswith('ci/failures.json') and isinstance(value, list):
                    failures.extend(value)
    return records, failures


def recover(run):
    if run.get('conclusion') != 'failure' or run.get('event') not in {'merge_group', 'workflow_dispatch'}:
        return
    if run.get('path') not in {'.github/workflows/ci.yml', '.github/workflows/qualification.yml'}:
        return
    if (run.get('repository') or {}).get('full_name') != os.environ['GITHUB_REPOSITORY']:
        return
    if (run.get('head_repository') or {}).get('full_name') != os.environ['GITHUB_REPOSITORY']:
        return
    records, _ = evidence(run)
    jobs = list(pages(f"{repository()}/actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs", 'jobs'))
    if permits_retry(run, records, jobs) and current_group(run):
        api(f"{repository()}/actions/runs/{run['id']}/rerun-failed-jobs", 'POST')
        return


def main():
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
    recover(api(f"{repository()}/actions/runs/{event['workflow_run']['id']}"))


if __name__ == '__main__':
    main()
