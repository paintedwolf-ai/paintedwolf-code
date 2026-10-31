"""Order hosted lane checks without changing their coverage."""
import io
import json
import os
from pathlib import Path
import subprocess
from urllib.parse import urlencode
import zipfile

from ci_policy.github import api, repository
from ci_policy.hosted_plan import invocation
from verification_plan import expand
from verification_execute import json_stream

ROOT = Path(__file__).resolve().parents[2]
HISTORY_LIMIT = 32 * 1024 * 1024
MAX_HISTORY_RUNS = 5
NATIVE_TARGETS = ('den:test:rust', 'decide:test')


def rotate_cursor(items, failed, passed=()):
    """Partition items into (failed, after_unverified, before_passed) starting at the first failure."""
    ordered = list(items)
    failed_set = set(failed) & set(ordered)
    passed_set = (set(passed) & set(ordered)) - failed_set
    if not failed_set:
        after_phase = [item for item in ordered if item not in passed_set]
        before_phase = [item for item in ordered if item in passed_set]
        return [], after_phase, before_phase
    first = min(ordered.index(item) for item in failed_set)
    failed_phase = [item for item in ordered if item in failed_set]
    after_phase = [item for idx, item in enumerate(ordered)
                   if item not in failed_set and idx > first and item not in passed_set]
    before_phase = [item for item in ordered if item not in failed_set and item not in after_phase]
    return failed_phase, after_phase, before_phase


def receipt_state(raw, run, targets):
    """Extract validated wrap-around state from a ready receipt archive."""
    if len(raw) > HISTORY_LIMIT:
        return None
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        values = {}
        for name in ('run.json', 'failures.json'):
            members = [member for member in archive.infolist() if member.filename.endswith('ci/' + name)]
            if len(members) != 1 or members[0].file_size > 1024 * 1024:
                return None
            values[name] = json.loads(archive.read(members[0]))
    record, failures = values['run.json'], values['failures.json']
    if (not isinstance(record, dict) or not isinstance(failures, list) or record.get('lane') != 'ready'
            or record.get('status') != 'failed' or record.get('shard') != ''):
        return None
    identity = record.get('identity')
    if (not isinstance(identity, dict) or identity.get('run') != str(run['id'])
            or identity.get('attempt') != str(run['run_attempt'])):
        return None
    if any(not isinstance(failure, dict) for failure in failures):
        return None
    # Pull request receipts name the synthetic merge commit, not the API's head_sha.
    target_stages = {target: [stage['name'] for stage in expand([target])] for target in targets}
    stages = {name: target for target, names in target_stages.items() for name in names}
    failed, passed, contract_failures = [], [], []
    for failure in failures:
        if failure.get('status') != 'failed':
            continue
        stage = failure.get('stage')
        subject = failure.get('subject', '')
        if stage == 'test:contract' and subject.startswith('github.com/lycaon/lycaon/test/contract'):
            rel = './' + subject.removeprefix('github.com/lycaon/lycaon/')
            if rel not in contract_failures:
                contract_failures.append(rel)
            continue
        target = stages.get(stage)
        if target is None:
            return None
        if target not in failed:
            failed.append(target)
    passed_stage_set = set(record.get('passed_stages', []))
    for target, names in target_stages.items():
        if names and all(name in passed_stage_set for name in names):
            if target not in failed and target not in passed:
                passed.append(target)
    native_failed, native_passed = [], []
    for phase in record.get('phases', []):
        if not isinstance(phase, dict) or not isinstance(phase.get('command'), list):
            continue
        cmd = phase['command']
        if len(cmd) >= 2 and cmd[0] == './task' and cmd[1] in NATIVE_TARGETS:
            if phase.get('exit_code') == 0 and cmd[1] not in native_passed:
                native_passed.append(cmd[1])
            elif phase.get('exit_code') not in (None, 0) and cmd[1] not in native_failed:
                native_failed.append(cmd[1])
        elif len(cmd) >= 2 and cmd[0] == './task' and phase.get('exit_code') == 0:
            # Rollout transition fallback for receipts written before passed_stages existed.
            executed = set(cmd[1:])
            for target in targets:
                members = set(invocation([target]))
                if members and members.issubset(executed) and target not in failed and target not in passed:
                    passed.append(target)
    if not failed and not native_failed and not contract_failures:
        return None
    return {
        'failed_targets': failed,
        'passed_targets': passed,
        'native_failed': native_failed,
        'native_passed': native_passed,
        'contract_failures': contract_failures,
    }


def failed_target(raw, run, targets):
    """Single-target wrapper retained for unit tests expecting a bare target name."""
    state = receipt_state(raw, run, targets)
    if not state or len(state['failed_targets']) != 1 or state['contract_failures'] or state['native_failed']:
        return ''
    return state['failed_targets'][0]


def previous_plan(event, run_id, targets):
    """Missing or uncertain history leaves the complete lane in its normal order."""
    try:
        number = event['pull_request']['number']
        query = urlencode({'event': 'pull_request', 'branch': event['pull_request']['head']['ref'], 'per_page': 100})
        current = api(f'{repository()}/actions/runs/{run_id}')
        runs = api(f"{repository()}/actions/workflows/{current['workflow_id']}/runs?{query}")
        candidates = sorted(
            [run for run in runs['workflow_runs']
             if run['id'] < int(run_id) and run.get('workflow_id') == current['workflow_id']
             and run.get('event') == 'pull_request'
             and any(pull.get('number') == number for pull in run.get('pull_requests', []))],
            key=lambda value: value['id'], reverse=True
        )
        merged = None
        for run in candidates[:MAX_HISTORY_RUNS]:
            if run.get('status') != 'completed' or run.get('conclusion') != 'failure':
                break
            artifacts = api(f"{repository()}/actions/runs/{run['id']}/artifacts?per_page=100")['artifacts']
            matching = [artifact for artifact in artifacts
                        if artifact.get('name') == f"receipt-verification-fast-ready-{run['run_attempt']}"
                        and not artifact.get('expired') and artifact['size_in_bytes'] <= HISTORY_LIMIT]
            if len(matching) != 1:
                return {}
            raw = subprocess.check_output(['gh', 'api', f"{repository()}/actions/artifacts/{matching[0]['id']}/zip"], timeout=30)
            state = receipt_state(raw, run, targets)
            if state is None:
                return {}
            if merged is None:
                merged = state
            else:
                merged['failed_targets'] = state['failed_targets']
                merged['passed_targets'] = state['passed_targets']
                merged['native_failed'] = state['native_failed']
                merged['native_passed'] = state['native_passed']
            if merged['failed_targets'] or merged['native_failed']:
                break
        return merged or {}
    except (KeyError, TypeError, AttributeError, ValueError, RuntimeError, OSError,
            subprocess.SubprocessError, zipfile.BadZipFile):
        return {}


def previous_target(event, run_id, targets):
    """Single-target wrapper retained for unit tests expecting a bare target name."""
    plan = previous_plan(event, run_id, targets)
    failed = plan.get('failed_targets', [])
    if len(failed) != 1 or plan.get('contract_failures') or plan.get('native_failed'):
        return ''
    return failed[0]


def contract_packages():
    """All contract test packages under lycaon/test/contract (including internal/*) in deterministic order."""
    root = ROOT / "lycaon"
    base = root / "test" / "contract"
    return sorted({"./" + path.parent.relative_to(root).as_posix() for path in base.rglob("*_test.go")})


def preflight_commands(preflight_args, history=None):
    """Rotate preflight contract packages to [failed + after, before] when a contract failed."""
    default = [["./task", *preflight_args]]
    if not history or not history.get("contract_failures") or preflight_args != ["test:contract", "--", "./test/contract/..."]:
        return default
    failed, after, before = rotate_cursor(contract_packages(), history["contract_failures"])
    if not failed:
        return default
    return [["./task", "test:contract", "--", *part] for part in ([*failed, *after], before) if part]


def ready_commands(targets, native_cmds, history=None):
    """Build wrap-around phases: [Failed] -> [Everything After / Unverified] -> [Previously Passed]."""
    if not history:
        return [*native_cmds, ["./task", *invocation(targets)]]
    native_by_name = {cmd[1]: cmd for cmd in native_cmds if len(cmd) >= 2}
    failed_native = [native_by_name[name] for name in history.get("native_failed", []) if name in native_by_name]
    passed_native = [native_by_name[name] for name in history.get("native_passed", [])
                     if name in native_by_name and name not in history.get("native_failed", [])]
    unverified_native = [cmd for cmd in native_cmds if cmd not in failed_native and cmd not in passed_native]
    failed_targets, after_targets, before_targets = rotate_cursor(
        targets, history.get("failed_targets", []), history.get("passed_targets", [])
    )
    if not failed_targets and not failed_native:
        return [*native_cmds, ["./task", *invocation(targets)]]
    phases = []
    if failed_targets:
        phases.append(["./task", *invocation(failed_targets)])
    phases.extend(failed_native)
    if after_targets:
        phases.append(["./task", *invocation(after_targets)])
    phases.extend(unverified_native)
    phases.extend(passed_native)
    if before_targets:
        phases.append(["./task", *invocation(before_targets)])
    return phases


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
    plan = previous_plan(event, os.environ['GITHUB_RUN_ID'], lanes()['ready']['targets'])
    print(json.dumps(plan, separators=(',', ':')) if plan else '')
