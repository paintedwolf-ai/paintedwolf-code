"""Durable reconciliation checkpoints and warning intake across consecutive merges."""
from datetime import datetime
import io
import json
import subprocess
import zipfile

from .budget_evidence import MAX_ARCHIVE_BYTES, read_snapshot
from .budget_snapshot import SHA
from .github import api, pages, repository


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
            datetime.fromisoformat(value['qualification_created_at'].replace('Z', '+00:00'))
            return value
    return None


def merged_pulls(run, repo):
    return [p for p in pages(f"{repository()}/commits/{run['head_sha']}/pulls")
            if p.get('merged_at') and p.get('base', {}).get('ref') == 'main'
            and p.get('base', {}).get('repo', {}).get('full_name') == repo]


def intake(run, rows, prior, repo):
    identities = {identity for identity, row in rows.items() if row['touched']}
    if prior is None or prior['source_sha'] == run['head_sha']:
        return identities
    if not ancestor(prior['source_sha'], run['head_sha']):
        raise ValueError('main no longer descends from the reconciliation checkpoint; use explicit backfill')
    path = (f"{repository()}/actions/workflows/qualification.yml/runs?branch=main&event=push&status=completed"
            f"&created=>={prior['qualification_created_at']}")
    commits = {}
    for historical in pages(path, 'workflow_runs'):
        sha = historical['head_sha']
        if sha in {prior['source_sha'], run['head_sha']} or not SHA.fullmatch(sha):
            continue
        if historical.get('path') != '.github/workflows/qualification.yml' or historical.get('event') != 'push':
            continue
        if historical.get('head_branch') != 'main' or historical.get('repository', {}).get('full_name') != repo:
            continue
        if sha not in commits or historical['run_attempt'] > commits[sha]['run_attempt']:
            commits[sha] = historical
    for sha, historical in commits.items():
        if not ancestor(prior['source_sha'], sha) or not ancestor(sha, run['head_sha']) or not merged_pulls(historical, repo):
            continue
        tree = api(f'{repository()}/git/commits/{sha}')['tree']['sha']
        _, artifacts = read_snapshot(historical, tree)
        identities.update(identity for identity, row in artifacts.items() if row['touched'])
    return identities
