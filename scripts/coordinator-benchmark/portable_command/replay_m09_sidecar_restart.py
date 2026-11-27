"""Zero-model M09 isolated sidecar stop+restart replay across 3 barrier states."""
import argparse
import contextlib
import hashlib
import json
import pathlib
import secrets
import socket
import sqlite3
import subprocess
import sys
import tempfile
import uuid

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parents[1]
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

import smoke
from application_environment import application_environment
from portable_command.contracts import ROOT
from portable_command.harness import create_isolated_session, send_tool_calls
from portable_command.observe import parse_tool_result
from portable_command.provenance import make_app_factory, resolve_engine_binary, source_identity
from portable_command.reporting import structured_rejection

BASELINE_BYTES = b'baseline-m09\n'
S1_BYTES = b'after-restart-1\n'
COMMITTED_BYTES = b'committed-m09\n'
RECOVERED_BYTES = b'recovered-m09\n'


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def sha256_file(path):
    p = pathlib.Path(path)
    return sha256_bytes(p.read_bytes()) if p.is_file() else None


class ReopenedApplication(smoke.Application):
    """Restart lycaon-dev against an existing app_dir/store.db without wiping state."""

    def __init__(self, directory, *, engine_bin=None, generation=1):
        self.directory = pathlib.Path(directory)
        self.config = self.directory / 'config'
        self.manual = True
        self.projects, self.admissions, self.baseline_checks, self.flows = [], [], [], []
        suite = json.loads((ROOT / 'lycaon/test/fixtures/eval/coordinator-benchmark.json').read_text())
        self.task_allowance = suite['task_allowance']
        self.approval_guidance = suite['unattended']['approval_guidance']
        self.token = secrets.token_hex(24)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            self.port = sock.getsockname()[1]
        self.url = f'http://127.0.0.1:{self.port}'
        self.log = (self.directory / f'sidecar-restart-{generation}.log').open('w')
        binary, _ = resolve_engine_binary(ROOT, engine_bin)
        env = application_environment({
            'LYCAON_CONFIG_DIR': str(self.config), 'LYCAON_CONFIG_ROOT': str(ROOT / 'lycaon'),
            'LYCAON_ENGINE_ROOT': str(ROOT / 'lycaon-den/src-tauri/engine-root'),
            'LYCAON_ADDR': f'127.0.0.1:{self.port}', 'LYCAON_API_TOKEN': self.token,
            'LYCAON_DEV': '1', 'LYCAON_HARNESS': '1', 'LYCAON_LOG_LEVEL': 'debug',
            'LYCAON_LLM_MOCK': '1', 'LYCAON_LLM_MANUAL': '1',
        })
        self.process = subprocess.Popen(
            [str(binary), 'serve', '--db', str(self.directory / 'store.db')],
            cwd=ROOT, env=env, stdout=self.log, stderr=subprocess.STDOUT,
        )

    def wait_for(self, observe, phase, session=None, timeout=180):
        return super().wait_for(observe, phase, session=None, timeout=timeout)


def _restart(app, app_dir, *, engine_bin, generation):
    app.close(cleanup=False)
    restarted = ReopenedApplication(app_dir, engine_bin=engine_bin, generation=generation)
    restarted.ready()
    return restarted


def _wait_idle(app, sid):
    def is_idle():
        st = app.request('/v1/sessions/' + sid)['status']
        return st == 'idle'
    app.wait_for(is_idle, 'session idle')


def _start_prompt(app, sid, text):
    app.request('/harness/model-limit', {'session_id': sid, 'limit': 20})
    app.request(f'/v1/sessions/{sid}/prompts', {'operation_id': str(uuid.uuid4()), 'text': text})


def _finish_prompt(app, sid, _text='Done.'):
    app.request(f'/v1/sessions/{sid}/abort', {})
    _wait_idle(app, sid)


def _run_cmd(app, sid, surface, cmd):
    results = send_tool_calls(app, sid, [{'name': 'command', 'args': {'command': cmd}}], surface=surface)
    call_id, raw_res = results[-1]
    raw_parsed, payload = parse_tool_result({'tool_result_json': json.dumps(raw_res)})
    code, details = structured_rejection(raw_parsed, payload)
    return {
        'tool_call_id': call_id, 'command': cmd, 'outcome': raw_parsed.get('outcome'),
        'state': payload.get('state'), 'status': payload.get('status'),
        'stdout': payload.get('stdout'), 'handle': payload.get('handle'),
        'completed_effects': payload.get('completed_effects'),
        'rejection_code': code, 'rejection_details': details,
        'program_id': payload.get('program_id') or (raw_parsed.get('command_program') or {}).get('id'),
    }


def _open_session(app, project_id):
    session = app.request('/v1/sessions', {'project_id': project_id, 'posture': 'build'})
    sid = session['id']
    app.request('/harness/model-limit', {'session_id': sid, 'limit': 20})
    _wait_idle(app, sid)
    return sid


def _receipt_state(receipt):
    if not isinstance(receipt, dict):
        return None, None
    prog = receipt.get('program') if isinstance(receipt.get('program'), dict) else receipt
    return prog.get('state'), prog.get('status_code')


def _program_row(db_path, pid):
    with contextlib.closing(sqlite3.connect(db_path)) as db:
        row = db.execute('SELECT state, status_code, stop_code FROM invocation_programs WHERE id=?', (pid,)).fetchone()
    return {'state': row[0], 'status_code': row[1], 'stop_code': row[2]} if row else None


def run_m09_replay(repo_root, out_path, engine_manifest=None, engine_bin=None):
    identity = source_identity(repo_root, profile='implement', engine_bin=engine_bin, engine_manifest=engine_manifest)
    workdir = pathlib.Path(tempfile.mkdtemp(prefix='m09-sidecar-restart-'))
    app_dir, project_dir = workdir / 'app', workdir / 'project'
    project_dir.mkdir(parents=True, exist_ok=True)
    baseline_file = project_dir / 'baseline.txt'
    baseline_file.write_bytes(BASELINE_BYTES)
    baseline_sha = sha256_bytes(BASELINE_BYTES)

    app = make_app_factory(engine_bin, smoke.Application)(app_dir)
    try:
        app.ready()
        sid, project_id, root_id, surface = create_isolated_session(app, project_dir, {'profile': 'implement'})

        # State 1: precommit refusal -> sidecar restart -> prior receipt retained + fresh write succeeds.
        _start_prompt(app, sid, 'M09 State 1: precommit refusal before sidecar restart.')
        send_tool_calls(app, sid, [{'name': 'update_progress', 'args': {'content': '- [ ] Run M09 sidecar restart'}}], surface=surface)
        s1_refused = _run_cmd(app, sid, surface, 'grep --bogus-flag x ./baseline.txt')
        _finish_prompt(app, sid, 'State 1 pre-restart done.')

        app = _restart(app, app_dir, engine_bin=engine_bin, generation=1)
        s1_invocations = app.request(f'/v1/sessions/{sid}/invocations')
        s1_inv_list = s1_invocations.get('invocations') if isinstance(s1_invocations, dict) else (s1_invocations or [])
        s1_retained = any(i.get('tool_call_id') == s1_refused['tool_call_id'] for i in s1_inv_list if isinstance(i, dict))
        s1_baseline_sha = sha256_file(baseline_file)

        _start_prompt(app, sid, 'M09 State 1 post-restart fresh command.')
        s1_fresh = _run_cmd(app, sid, surface, "printf 'after-restart-1\\n' > ./s1.txt")
        _finish_prompt(app, sid, 'State 1 post-restart done.')
        s1_file_sha = sha256_file(project_dir / 's1.txt')

        # State 2: committed effect -> sidecar restart -> receipt reload + follow-up read.
        _start_prompt(app, sid, 'M09 State 2: committed effect before sidecar restart.')
        s2_commit = _run_cmd(app, sid, surface, "printf 'committed-m09\\n' > ./committed.txt")
        _finish_prompt(app, sid, 'State 2 pre-restart done.')

        app = _restart(app, app_dir, engine_bin=engine_bin, generation=2)
        s2_pid = s2_commit['program_id']
        s2_receipt = app.request(f'/v1/sessions/{sid}/program-receipts/{s2_pid}?limit=50', missing=True) if s2_pid else None
        s2_committed_sha = sha256_file(project_dir / 'committed.txt')

        _start_prompt(app, sid, 'M09 State 2 post-restart follow-up read.')
        s2_read = _run_cmd(app, sid, surface, 'cat ./committed.txt')
        _finish_prompt(app, sid, 'State 2 post-restart done.')

        # State 3: active running/parked work -> immediate sidecar restart -> non-synthetic outcome + recovery.
        _start_prompt(app, sid, 'M09 State 3: active work interrupted by sidecar restart.')
        s3_active = _run_cmd(app, sid, surface, "printf 'started\\n' && sleep 60")

        app = _restart(app, app_dir, engine_bin=engine_bin, generation=3)
        s3_pid = s3_active['program_id']
        s3_invocations = app.request(f'/v1/sessions/{sid}/invocations')
        s3_receipt = app.request(f'/v1/sessions/{sid}/program-receipts/{s3_pid}?limit=50', missing=True) if s3_pid else None
        s3_db_row = _program_row(app_dir / 'store.db', s3_pid) if s3_pid else None
        _finish_prompt(app, sid)

        rec_sid = _open_session(app, project_id)
        _start_prompt(app, rec_sid, 'M09 State 3 post-restart recovery command.')
        send_tool_calls(app, rec_sid, [{'name': 'update_progress', 'args': {'content': '- [ ] Recover after M09 restart'}}], surface=surface)
        s3_recovery = _run_cmd(app, rec_sid, surface, "printf 'recovered-m09\\n' > ./recovered.txt")
        _finish_prompt(app, rec_sid, 'State 3 recovery done.')
        s3_recovered_sha = sha256_file(project_dir / 'recovered.txt')
    finally:
        app.close(cleanup=False)

    s2_r_state, s2_r_status = _receipt_state(s2_receipt)
    s3_r_state, s3_r_status = _receipt_state(s3_receipt)
    s3_non_synthetic = (
        s3_active['state'] in {'running', 'parked'}
        and not (s3_r_state == 'completed' and s3_r_status == 0)
        and not (s3_db_row and s3_db_row['state'] == 'completed' and s3_db_row['status_code'] == 0)
    )
    checks = {
        'state1_precommit_restart': (
            (s1_refused['outcome'] == 'rejected' or s1_refused['status'] != 0 or bool(s1_refused['rejection_code']))
            and s1_baseline_sha == baseline_sha
            and s1_retained
            and s1_fresh['status'] == 0 and s1_file_sha == sha256_bytes(S1_BYTES)
        ),
        'state2_committed_restart': (
            s2_commit['status'] == 0 and bool(s2_pid)
            and s2_committed_sha == sha256_bytes(COMMITTED_BYTES)
            and s2_r_state == 'completed' and s2_r_status == 0
            and s2_read['status'] == 0 and s2_read['stdout'] == COMMITTED_BYTES.decode('utf-8')
        ),
        'state3_active_restart_and_recovery': (
            bool(s3_pid) and s3_non_synthetic
            and s3_recovery['status'] == 0 and s3_recovered_sha == sha256_bytes(RECOVERED_BYTES)
        ),
        'provenance_verified': (identity.get('engine_provenance') or {}).get('provenance_verified') is True,
    }
    report = {
        'case_id': 'M09-sidecar-restart', 'model_calls': 0,
        'passed': all(checks.values()), 'checks': checks,
        'identity': identity, 'provenance_verified': checks['provenance_verified'],
        'session': {'id': sid, 'recovery_session_id': rec_sid, 'project_id': project_id, 'root_id': root_id},
        'state1': {'refused': s1_refused, 'retained_after_restart': s1_retained, 'baseline_sha256': s1_baseline_sha, 'fresh': s1_fresh, 's1_sha256': s1_file_sha},
        'state2': {'commit': s2_commit, 'receipt': s2_receipt, 'committed_sha256': s2_committed_sha, 'read': s2_read},
        'state3': {'active': s3_active, 'receipt': s3_receipt, 'db_row': s3_db_row, 'invocations': s3_invocations, 'recovery': s3_recovery, 'recovered_sha256': s3_recovered_sha},
    }
    out_file = pathlib.Path(out_path)
    out_file.parent.mkdir(parents=True, exist_ok=True)
    out_file.write_text(json.dumps(report, indent=2) + '\n')
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description='Zero-model M09 isolated sidecar stop+restart replay.')
    parser.add_argument('--engine-manifest', type=pathlib.Path, help='Build-time provenance manifest JSON')
    parser.add_argument('--engine', type=pathlib.Path, help='Explicit engine binary path')
    parser.add_argument('--out', type=pathlib.Path, required=True, help='Output JSON report path')
    parser.add_argument('--repo-root', type=pathlib.Path, default=ROOT, help='Repository root path')
    args = parser.parse_args(argv)
    report = run_m09_replay(args.repo_root, args.out, engine_manifest=args.engine_manifest, engine_bin=args.engine)
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
