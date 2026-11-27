"""Bounded receipt parsing for trusted post-merge maintainability observations."""
import io
import json
import subprocess
import zipfile

from .github import pages, repository
from .budget_snapshot import key, validate
from . import memory_budgets

MAX_ARCHIVE_BYTES = 32 * 1024 * 1024
MAX_REPORT_BYTES = 8 * 1024 * 1024


def _read_zip_memory_observations(raw, tree, policy):
    observations = {}
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        for member in archive.infolist():
            if not member.filename.endswith('.json') or member.filename.endswith('.progress.json'):
                continue
            if member.file_size <= 0 or member.file_size > MAX_REPORT_BYTES:
                continue
            try:
                record = json.loads(archive.read(member))
            except (ValueError, UnicodeDecodeError):
                continue
            if not isinstance(record, dict) or record.get('status') != 'passed' or record.get('source_tree') != tree:
                continue
            if not isinstance(record.get('evidence'), list):
                continue
            memory_budgets.collect_receipt_observations(record, member.filename, observations, policy)
    return observations


def _merge_hosted_memory_artifacts(run, tree, all_artifacts, artifacts, repo_root=None, reviews_dir=memory_budgets.REVIEWS_DIR):
    suffix = f"-{run['run_attempt']}"
    prefix = 'receipt-verification-nightly-'
    build_name = f"{prefix}build{suffix}"
    collected = {}
    policy = memory_budgets.load_policy()
    for artifact in all_artifacts:
        name = artifact.get('name', '')
        if not name.startswith(prefix) or not name.endswith(suffix) or name == build_name or artifact.get('expired'):
            continue
        if not 0 < artifact.get('size_in_bytes', 0) <= MAX_ARCHIVE_BYTES:
            continue
        raw = subprocess.check_output(['gh', 'api', f"{repository()}/actions/artifacts/{artifact['id']}/zip"])
        if len(raw) > MAX_ARCHIVE_BYTES:
            continue
        for obs_key, item in _read_zip_memory_observations(raw, tree, policy).items():
            prev = collected.get(obs_key)
            if prev is None or (item[1].get('peak_bytes') or -1) > (prev[1].get('peak_bytes') or -1):
                collected[obs_key] = item
    measured = {k: obs for k, (_, obs) in collected.items() if type(obs.get('peak_bytes')) is int and obs['peak_bytes'] > 0}
    if not measured:
        return
    root = repo_root or memory_budgets.ROOT
    for row in memory_budgets.tracking_artifacts_from_measured(root, measured, reviews_dir=reviews_dir):
        identity = key(row['category'], row['id'])
        existing = artifacts.get(identity)
        if existing is None or row['measured'] > existing['measured']:
            artifacts[identity] = row


def read_snapshot(run, tree):
    name = f"receipt-verification-nightly-build-{run['run_attempt']}"
    all_artifacts = list(pages(f"{repository()}/actions/runs/{run['id']}/artifacts", 'artifacts'))
    candidates = [a for a in all_artifacts if a['name'] == name and not a['expired']]
    if len(candidates) != 1:
        raise ValueError('expected one current-attempt nightly build receipt')
    artifact = candidates[0]
    if not 0 < artifact['size_in_bytes'] <= MAX_ARCHIVE_BYTES:
        raise ValueError('build receipt exceeds the download bound')
    raw = subprocess.check_output(['gh', 'api', f"{repository()}/actions/artifacts/{artifact['id']}/zip"])
    if len(raw) > MAX_ARCHIVE_BYTES:
        raise ValueError('build receipt exceeds the archive bound')
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        members = [m for m in archive.infolist() if m.filename == 'reports/maintainability.json'
                   or m.filename.endswith('/reports/maintainability.json')]
        if len(members) != 1 or members[0].file_size > MAX_REPORT_BYTES:
            raise ValueError('expected one bounded maintainability report')
        report = json.loads(archive.read(members[0]))
    snapshot, artifacts = validate(report, run['head_sha'], tree)
    _merge_hosted_memory_artifacts(run, tree, all_artifacts, artifacts)
    return snapshot, artifacts
