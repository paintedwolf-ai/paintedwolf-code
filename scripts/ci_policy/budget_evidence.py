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


def _read_compact_memory(raw, sha, tree):
    measured = {}
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        members = [m for m in archive.infolist() if m.filename == 'ci/memory.json' or m.filename.endswith('/ci/memory.json')]
        for member in members:
            if member.file_size <= 0 or member.file_size > MAX_REPORT_BYTES:
                continue
            try:
                payload = json.loads(archive.read(member))
            except (ValueError, UnicodeDecodeError):
                continue
            if (not isinstance(payload, dict) or payload.get('schema_version') != 1
                    or payload.get('source_sha') != sha or payload.get('source_tree') != tree):
                continue
            for obs in payload.get('observations', []):
                if (not isinstance(obs, dict) or obs.get('lane') not in {'ordinary', 'race'}
                        or not isinstance(obs.get('package'), str)
                        or type(obs.get('peak_bytes')) is not int or obs['peak_bytes'] <= 0
                        or type(obs.get('limit_bytes')) is not int or obs['limit_bytes'] <= 0):
                    continue
                obs_key = (obs['lane'], obs['package'])
                prev = measured.get(obs_key)
                if prev is None or obs['peak_bytes'] > prev['peak_bytes']:
                    measured[obs_key] = obs
    return measured


def _merge_hosted_memory_artifacts(run, tree, all_artifacts, artifacts, repo_root=None, reviews_dir=None):
    suffix = f"-{run['run_attempt']}"
    prefix = 'receipt-verification-nightly-'
    build_name = f"{prefix}build{suffix}"
    measured = {}
    for artifact in all_artifacts:
        name = artifact.get('name', '')
        if not name.startswith(prefix) or not name.endswith(suffix) or name == build_name or artifact.get('expired'):
            continue
        if not 0 < artifact.get('size_in_bytes', 0) <= MAX_ARCHIVE_BYTES:
            continue
        raw = subprocess.check_output(['gh', 'api', f"{repository()}/actions/artifacts/{artifact['id']}/zip"])
        if len(raw) > MAX_ARCHIVE_BYTES:
            continue
        for obs_key, obs in _read_compact_memory(raw, run['head_sha'], tree).items():
            prev = measured.get(obs_key)
            if prev is None or obs['peak_bytes'] > prev['peak_bytes']:
                measured[obs_key] = obs
    observed = {(f"package_memory_{lane}", memory_budgets.artifact_id(pkg)) for lane, pkg in measured}
    if not measured:
        return observed
    root = repo_root or memory_budgets.ROOT
    reviews = reviews_dir if reviews_dir is not None else memory_budgets.REVIEWS_DIR
    for row in memory_budgets.tracking_artifacts_from_measured(root, measured, reviews_dir=reviews):
        identity = key(row['category'], row['id'])
        existing = artifacts.get(identity)
        if existing is None or row['measured'] > existing['measured']:
            artifacts[identity] = row
    return observed


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
    observed = _merge_hosted_memory_artifacts(run, tree, all_artifacts, artifacts)
    snapshot['memory_observed'] = sorted(observed)
    return snapshot, artifacts
