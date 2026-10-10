"""Durable reconciliation checkpoints and cumulative source-based warning intake."""
from pathlib import Path
import io
import json
import subprocess
import zipfile

from .budget_evidence import MAX_ARCHIVE_BYTES
from .budget_snapshot import SHA
from .github import pages, repository
import change_report


def ancestor(older, newer):
    if not isinstance(older, str) or not SHA.fullmatch(older):
        raise ValueError('invalid checkpoint commit')
    result = subprocess.run(['git', 'merge-base', '--is-ancestor', older, newer], capture_output=True, text=True)
    if result.returncode not in {0, 1}:
        raise ValueError('cannot establish main commit ancestry')
    return result.returncode == 0


def checkpoint():
    path = f'{repository()}/actions/workflows/maintainability-issues.yml/runs?branch=main&status=success'
    for run in pages(path, 'workflow_runs'):
        name = f"maintainability-issues-{run['id']}-{run['run_attempt']}"
        for artifact in pages(f"{repository()}/actions/runs/{run['id']}/artifacts", 'artifacts'):
            if artifact['name'] != name:
                continue
            if artifact['expired'] or not 0 < artifact['size_in_bytes'] <= MAX_ARCHIVE_BYTES:
                raise ValueError('reconciliation history is unavailable; replay with backfill to establish a new checkpoint')
            raw = subprocess.check_output(['gh', 'api', f"{repository()}/actions/artifacts/{artifact['id']}/zip"])
            if len(raw) > MAX_ARCHIVE_BYTES:
                raise ValueError('reconciliation history exceeds the download bound')
            with zipfile.ZipFile(io.BytesIO(raw)) as archive:
                members = [m for m in archive.infolist() if m.filename == 'maintainability-checkpoint.json']
                if not members:
                    continue
                if len(members) != 1 or members[0].file_size > 4096:
                    raise ValueError('invalid reconciliation checkpoint')
                value = json.loads(archive.read(members[0]))
            if not isinstance(value, dict) or value.get('schema_version') != 1:
                raise ValueError('unsupported reconciliation checkpoint')
            if not isinstance(value.get('source_sha'), str) or not SHA.fullmatch(value['source_sha']):
                raise ValueError('invalid checkpoint source identity')
            return value
    return None


def merged_pulls(run, repo):
    return [p for p in pages(f"{repository()}/commits/{run['head_sha']}/pulls")
            if p.get('merged_at') and p.get('base', {}).get('ref') == 'main'
            and p.get('base', {}).get('repo', {}).get('full_name') == repo]


def intake(run, rows, prior):
    identities = {identity for identity, row in rows.items() if row['touched']}
    root = Path(__file__).resolve().parents[2]
    base = prior['source_sha'] if prior else initial_base(root)
    if base == run['head_sha']:
        return identities
    if not ancestor(base, run['head_sha']):
        raise ValueError('main no longer descends from the reconciliation checkpoint; use explicit backfill')
    if change_report.git(root, 'rev-parse', 'HEAD').stdout.strip() != run['head_sha']:
        raise ValueError('the reconciliation checkout is not the measured main commit')
    paths = change_report.changed_paths(root, base)
    lines = change_report.changed_lines(root, base, paths)
    added, removed = change_report.added_and_removed(root, base)
    directories = {str(Path(path).parent) for path in added + removed}
    for identity, row in rows.items():
        category = row['category']
        if category in {'source_directories', 'test_directories'}:
            touched = row['id'] in directories
        elif category.startswith('go_'):
            touched = any(any(span['first'] <= line <= span['last'] for line in lines.get(span['file'], []))
                          for span in row['spans'])
        else:
            touched = row['id'] in paths
        if touched:
            identities.add(identity)
    return identities


def initial_base(root):
    commits = change_report.git(root, 'log', '--format=%H', '--diff-filter=A', '--',
                                '.github/workflows/maintainability-issues.yml').stdout.splitlines()
    if not commits or not SHA.fullmatch(commits[-1]):
        raise ValueError('cannot establish the maintainability tracking baseline')
    return change_report.git(root, 'rev-parse', commits[-1] + '^').stdout.strip()
