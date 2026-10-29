"""Order hosted lane checks without changing their coverage."""
import io
import json
import os
from pathlib import Path
import subprocess
from urllib.parse import urlencode
import zipfile

from ci_policy.github import api, repository
from verification_plan import expand
from verification_execute import json_stream

ROOT = Path(__file__).resolve().parents[2]
HISTORY_LIMIT = 32 * 1024 * 1024


def failed_target(raw, run, targets):
    if len(raw) > HISTORY_LIMIT:
        return ''
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        values = {}
        for name in ('run.json', 'failures.json'):
            members = [member for member in archive.infolist() if member.filename.endswith('ci/' + name)]
            if len(members) != 1 or members[0].file_size > 1024 * 1024:
                return ''
            values[name] = json.loads(archive.read(members[0]))
    record, failures = values['run.json'], values['failures.json']
    if (not isinstance(record, dict) or not isinstance(failures, list) or record.get('lane') != 'ready'
            or record.get('status') != 'failed' or record.get('shard') != ''):
        return ''
    identity = record.get('identity')
    if (not isinstance(identity, dict) or identity.get('run') != str(run['id'])
            or identity.get('attempt') != str(run['run_attempt'])):
        return ''
    # Pull request receipts name the synthetic merge commit, not the API's head_sha.
    stages = {stage['name']: target for target in targets for stage in expand([target])}
    selected = set()
    for failure in failures:
        if not isinstance(failure, dict):
            return ''
        if failure.get('status') == 'failed':
            target = stages.get(failure.get('stage'))
            if target is None:
                return ''
            selected.add(target)
    return selected.pop() if len(selected) == 1 else ''


def previous_target(event, run_id, targets):
    """Missing or uncertain history leaves the complete lane in its normal order."""
    try:
        number = event['pull_request']['number']
        query = urlencode({'event': 'pull_request', 'branch': event['pull_request']['head']['ref'], 'per_page': 100})
        current = api(f'{repository()}/actions/runs/{run_id}')
        runs = api(f"{repository()}/actions/workflows/{current['workflow_id']}/runs?{query}")
        candidates = [run for run in runs['workflow_runs']
                      if run['id'] < int(run_id) and run.get('workflow_id') == current['workflow_id']
                      and run.get('event') == 'pull_request'
                      and any(pull.get('number') == number for pull in run.get('pull_requests', []))]
        if not candidates:
            return ''
        run = max(candidates, key=lambda value: value['id'])
        if run.get('status') != 'completed' or run.get('conclusion') != 'failure':
            return ''
        artifacts = api(f"{repository()}/actions/runs/{run['id']}/artifacts?per_page=100")['artifacts']
        matching = [artifact for artifact in artifacts
                    if artifact.get('name') == f"receipt-verification-fast-ready-{run['run_attempt']}"
                    and not artifact.get('expired') and artifact['size_in_bytes'] <= HISTORY_LIMIT]
        if len(matching) != 1:
            return ''
        raw = subprocess.check_output(['gh', 'api', f"{repository()}/actions/artifacts/{matching[0]['id']}/zip"], timeout=30)
        return failed_target(raw, run, targets)
    except (KeyError, TypeError, AttributeError, ValueError, RuntimeError, OSError,
            subprocess.SubprocessError, zipfile.BadZipFile):
        return ''


def package_phases(selected):
    patterns = selected or ["./..."]
    result = subprocess.run(["go", "list", "-json", "-tags=integration", *patterns],
                            cwd=ROOT / "lycaon", text=True, capture_output=True, check=True,
                            env={**os.environ, "GOPROXY": "off", "GOTOOLCHAIN": "local"})
    records = list(json_stream(result.stdout))
    if not records or any(row.get("Error") or row.get("DepsErrors") for row in records):
        raise ValueError("merge preflight requires a complete package selection")
    packages = sorted({row["ImportPath"] for row in records})
    contracts = [name for name in packages if "/test/contract/" in name or name.endswith("/test/contract")]
    behavior = [name for name in packages if name not in contracts]
    if not contracts:
        raise ValueError("merge preflight selection omitted repository contracts")
    return [part for part in (contracts, behavior) if part]


def commands(lane, targets, selected=None):
    early = lane.get("early_targets", [])
    remaining = [target for target in targets if target not in early]
    if lane.get("contract_first"):
        return [["./task", "test:full", "--", *part] for part in package_phases(selected)]
    arguments = ["--", *lane["arguments"]] if "arguments" in lane else []
    return [["./task", *part, *arguments] for part in (early, remaining) if part]


if __name__ == '__main__':
    from ci_policy.hosted_plan import lanes
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
    print(previous_target(event, os.environ['GITHUB_RUN_ID'], lanes()['ready']['targets']))
