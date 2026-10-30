"""Durable reconciliation checkpoints, cumulative source-based warning intake, and
the measured content that ties a report's artifacts to current main."""
from pathlib import Path, PurePosixPath
import io
import json
import subprocess
import zipfile

from .budget_evidence import MAX_ARCHIVE_BYTES
from .budget_snapshot import SHA, key
from .github import pages, repository
import change_report

ROOT = Path(__file__).resolve().parents[2]
DIRECTORIES = frozenset({'source_directories', 'test_directories'})
POLICY = 'lycaon/test/contract/maintainability-budgets.yaml'
REVIEWS = 'lycaon/test/contract/maintainability-reviews/'
EXCEPTIONS = 'lycaon/test/contract/maintainability-exceptions/'


def ancestor(root, older, newer):
    if not isinstance(older, str) or not SHA.fullmatch(older):
        raise ValueError('invalid ancestry commit')
    result = subprocess.run(['git', '-C', str(root), 'merge-base', '--is-ancestor', older, newer],
                            capture_output=True, text=True)
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
    root, head = ROOT, run['head_sha']
    base = prior['source_sha'] if prior else initial_base(root)
    if base == head:
        return identities
    if not ancestor(root, base, head):
        raise ValueError('main no longer descends from the reconciliation checkpoint; use explicit backfill')
    paths = change_report.changed_paths(root, base, head)
    identities.update(changed_records(root, base, head, paths, (REVIEWS,)) & set(rows))
    lines = change_report.changed_lines(root, base, paths, head)
    added, removed = change_report.added_and_removed(root, base, head)
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


def changed_records(root, base, head, paths, prefixes):
    """Identities named by review or exception records that changed between two commits."""
    identities = set()
    for path in paths:
        if not path.startswith(prefixes) or not path.endswith('.json'):
            continue
        for revision in (base, head):
            entry = change_report.git(root, 'ls-tree', revision, '--', path)
            if not entry.stdout.strip():
                continue
            record = json.loads(change_report.git(root, 'show', f'{revision}:{path}').stdout)
            identities.add(key(record['category'], record['artifact']))
    return identities


def measured_scope(row):
    """What a measurement reads: one file, or the immediate entries of a directory.

    Go types are measured across their package directory. A reported source
    outside that scope cannot be tied to it and refuses reconciliation.
    """
    category, artifact = row['category'], row['id']
    if category.startswith('go_'):
        scope = ('directory', artifact.rpartition('/')[0])
        inside = all(str(PurePosixPath(source).parent) == scope[1] for source in row.get('sources', []))
    else:
        scope = ('directory' if category in DIRECTORIES else 'file', artifact)
        inside = row.get('sources', [artifact]) == [artifact]
    if not scope[1] or not inside:
        raise ValueError(f'{category} {artifact} cannot be tied to its measured paths')
    return scope


class MainChanges:
    """What changed on current main since the report source, as measurement inputs."""

    def __init__(self, root, source):
        change_report.git(root, 'fetch', '--quiet', '--no-tags', 'origin', 'refs/heads/main')
        self.main = change_report.git(root, 'rev-parse', 'FETCH_HEAD').stdout.strip()
        if not ancestor(root, source, self.main):
            raise ValueError('the report source is not an ancestor of main')
        self.paths = frozenset(change_report.changed_paths(root, source, self.main))
        self.records = changed_records(root, source, self.main, self.paths, (REVIEWS, EXCEPTIONS))

    def changed(self, row):
        """Whether the artifact's measured paths, thresholds, exception, or review changed."""
        kind, path = measured_scope(row)
        if POLICY in self.paths or key(row['category'], row['id']) in self.records:
            return True
        if kind == 'file':
            return path in self.paths
        return any(str(PurePosixPath(changed_path).parent) == path for changed_path in self.paths)
