"""Zero-model M11 resource ceilings (at/over) and midstream cancellation replay."""
import argparse
import contextlib
import fcntl
import hashlib
import json
import pathlib
import sqlite3
import sys
import tempfile
import time
import uuid

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parents[1]
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

import smoke
import smoke_workers
from portable_command.contracts import ROOT
from portable_command.harness import create_isolated_session, send_tool_calls
from portable_command.observe import parse_tool_result
from portable_command.provenance import make_app_factory, source_identity
from portable_command.reporting import structured_rejection

RECOVERY_BYTES = b'm11-ceilings-recovered\n'


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def sha256_file(path):
    p = pathlib.Path(path)
    return sha256_bytes(p.read_bytes()) if p.is_file() else None


def _run_cmd(app, sid, surface, cmd):
    results = send_tool_calls(app, sid, [{'name': 'command', 'args': {'command': cmd}}], surface=surface)
    call_id, raw_res = results[-1]
    raw_parsed, payload = parse_tool_result({'tool_result_json': json.dumps(raw_res)})
    code, details = structured_rejection(raw_parsed, payload)
    return {
        'tool_call_id': call_id, 'command': cmd, 'outcome': raw_parsed.get('outcome'),
        'state': payload.get('state'), 'status': payload.get('status'),
        'stdout': payload.get('stdout'), 'stderr': payload.get('stderr'),
        'rejection_code': code, 'rejection_details': details,
        'program_id': payload.get('program_id') or (raw_parsed.get('command_program') or {}).get('id'),
    }


def _lock_is_free(lock_path):
    p = pathlib.Path(lock_path)
    if not p.is_file():
        return False
    with p.open('r+') as fh:
        try:
            fcntl.flock(fh.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            fcntl.flock(fh.fileno(), fcntl.LOCK_UN)
            return True
        except BlockingIOError:
            return False


def _wait_idle(app, sid):
    app.wait_for(lambda: app.request('/v1/sessions/' + sid)['status'] == 'idle', 'session idle')


def _open_session(app, project_id):
    session = app.request('/v1/sessions', {'project_id': project_id, 'posture': 'build'})
    sid = session['id']
    app.request('/harness/model-limit', {'session_id': sid, 'limit': 20})
    _wait_idle(app, sid)
    return sid


def run_m11_ceilings_and_cancel(repo_root, out_path, engine_manifest=None, engine_bin=None):
    identity = source_identity(repo_root, profile='implement', engine_bin=engine_bin, engine_manifest=engine_manifest)
    workdir = pathlib.Path(tempfile.mkdtemp(prefix='m11-ceilings-cancel-'))
    app_dir, project_dir = workdir / 'app', workdir / 'project'
    project_dir.mkdir(parents=True, exist_ok=True)

    app = make_app_factory(engine_bin, smoke.Application)(app_dir)
    try:
        app.ready()
        sid, project_id, root_id, surface = create_isolated_session(app, project_dir, {'profile': 'implement'})
        app.request(f'/v1/sessions/{sid}/prompts', {'operation_id': str(uuid.uuid4()), 'text': 'Execute M11 resource ceilings and midstream cancellation.'})
        send_tool_calls(app, sid, [{'name': 'update_progress', 'args': {'content': '- [ ] Run M11 resource ceilings and midstream cancellation'}}], surface=surface)

        # 1a. pipeline_stages ceiling: at limit (32 stages) succeeds; over limit (33 stages) refuses.
        pipe_at_cmd = "printf 'pipe-ok\\n'" + (' | cat' * 31)
        pipe_over_cmd = "printf 'pipe-over\\n'" + (' | cat' * 32)
        pipe_at = _run_cmd(app, sid, surface, pipe_at_cmd)
        pipe_over = _run_cmd(app, sid, surface, pipe_over_cmd)

        # 1b. expanded_fields ceiling (max 16384): brace expansion of 20000 refuses with COMMAND_LIMIT_EXCEEDED.
        expand_over = _run_cmd(app, sid, surface, 'echo {1..20000}')

        # 1c. function_depth ceiling (max 32): infinite recursion refuses with COMMAND_LIMIT_EXCEEDED.
        func_over = _run_cmd(app, sid, surface, 'f() { f; }; f')

        # 1d. Subsequent ordinary command in the same session succeeds cleanly after ceiling refusals.
        after_ceilings = _run_cmd(app, sid, surface, "printf 'after-ceilings-ok\\n' > ./m11_ceilings.txt && cat ./m11_ceilings.txt")

        # 2. Midstream cancellation via /v1/sessions/{sid}/abort while an external child holds an flock.
        lock_file = project_dir / 'm11_cancel.lock'
        pid_file = project_dir / 'm11_cancel.pid'
        cancel_cmd = (
            'external -- python3 -c "'
            'import fcntl, os, pathlib, time; '
            "f = open('m11_cancel.lock', 'w'); "
            'fcntl.flock(f, fcntl.LOCK_EX); '
            "pathlib.Path('m11_cancel.pid').write_text(str(os.getpid())); "
            'time.sleep(60)"'
        )
        req = app.wait_for(lambda: smoke_workers.pending(app, sid), 'midstream cancel turn', sid)
        cancel_call_id = str(uuid.uuid4())
        app.request('/harness/llm/respond', {'id': req['id'], 'tool_calls': [{'id': cancel_call_id, 'name': 'command', 'args': {'command': cancel_cmd}}]})

        app.wait_for(lambda: pid_file.is_file() and not _lock_is_free(lock_file), 'midstream child active', sid, timeout=30)
        child_pid = int(pid_file.read_text().strip())
        app.request(f'/v1/sessions/{sid}/abort', {'reason': 'm11_midstream_cancel'})
        _wait_idle(app, sid)

        deadline = time.monotonic() + 15
        lock_released = False
        while time.monotonic() < deadline:
            if _lock_is_free(lock_file):
                lock_released = True
                break
            time.sleep(0.1)

        with contextlib.closing(sqlite3.connect(app_dir / 'store.db')) as db:
            cancel_row = db.execute(
                'SELECT p.id, p.state, p.status_code, p.stop_code FROM invocation_programs p '
                'JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE r.tool_call_id=?',
                (cancel_call_id,),
            ).fetchone()
            pids = [r[0] for r in db.execute(
                'SELECT p.id FROM invocation_programs p JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE r.session_id=?',
                (sid,),
            ).fetchall()]
        cancel_db = {'id': cancel_row[0], 'state': cancel_row[1], 'status_code': cancel_row[2], 'stop_code': cancel_row[3]} if cancel_row else None
        invocations = app.request(f'/v1/sessions/{sid}/invocations')
        program_receipts = {
            pid: app.request(f'/v1/sessions/{sid}/program-receipts/{pid}?limit=50', missing=True)
            for pid in pids
        }

        # 3. Clean recovery in a fresh session on the same project after midstream abort.
        rec_sid = _open_session(app, project_id)
        app.request(f'/v1/sessions/{rec_sid}/prompts', {'operation_id': str(uuid.uuid4()), 'text': 'M11 post-cancel recovery.'})
        send_tool_calls(app, rec_sid, [{'name': 'update_progress', 'args': {'content': '- [ ] Recover after M11 cancel'}}], surface=surface)
        post_cancel = _run_cmd(app, rec_sid, surface, "printf 'm11-ceilings-recovered\\n' > ./m11_rec.txt && cat ./m11_rec.txt")
        app.request(f'/v1/sessions/{rec_sid}/abort', {})
    finally:
        app.close(cleanup=False)

    rec_sha = sha256_file(project_dir / 'm11_rec.txt')
    checks = {
        'pipeline_stages_at_32_succeeds': (pipe_at['status'] == 0 and pipe_at['stdout'] == 'pipe-ok\n'),
        'pipeline_stages_over_32_refused': (
            pipe_over['rejection_code'] == 'COMMAND_LIMIT_EXCEEDED' and pipe_over['status'] != 0
        ),
        'expanded_fields_over_16384_refused': (
            expand_over['rejection_code'] == 'COMMAND_LIMIT_EXCEEDED' and expand_over['status'] != 0
        ),
        'function_depth_over_32_refused': (
            func_over['rejection_code'] == 'COMMAND_LIMIT_EXCEEDED' and func_over['status'] != 0
        ),
        'session_recovers_after_ceilings': (
            after_ceilings['status'] == 0 and after_ceilings['stdout'] == 'after-ceilings-ok\n'
        ),
        'midstream_cancel_settled_no_lingering_child': (
            child_pid > 0
            and lock_released
            and cancel_db is not None
            and cancel_db['state'] in {'interrupted', 'canceled'}
            and cancel_db['status_code'] != 0
        ),
        'post_cancel_recovery_succeeds': (
            post_cancel['status'] == 0
            and post_cancel['stdout'] == RECOVERY_BYTES.decode('utf-8')
            and rec_sha == sha256_bytes(RECOVERY_BYTES)
        ),
        'provenance_verified': (identity.get('engine_provenance') or {}).get('provenance_verified') is True,
    }
    report = {
        'case_id': 'M11-resource-ceilings-and-midstream-cancel', 'model_calls': 0,
        'passed': all(checks.values()), 'checks': checks,
        'identity': identity, 'provenance_verified': checks['provenance_verified'],
        'session': {'id': sid, 'recovery_session_id': rec_sid, 'project_id': project_id, 'root_id': root_id},
        'ceilings': {
            'pipeline_at_32': pipe_at, 'pipeline_over_32': pipe_over,
            'expanded_fields_over': expand_over, 'function_depth_over': func_over,
            'after_ceilings': after_ceilings,
        },
        'midstream_cancel': {
            'route': '/v1/sessions/{id}/abort', 'tool_call_id': cancel_call_id,
            'child_pid': child_pid, 'lock_released': lock_released, 'db_row': cancel_db,
            'post_cancel_recovery': post_cancel, 'recovery_sha256': rec_sha,
        },
        'invocations': invocations, 'program_receipts': program_receipts,
    }
    out_file = pathlib.Path(out_path)
    out_file.parent.mkdir(parents=True, exist_ok=True)
    out_file.write_text(json.dumps(report, indent=2) + '\n')
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description='Zero-model M11 resource ceilings and midstream cancellation replay.')
    parser.add_argument('--engine-manifest', type=pathlib.Path, help='Build-time provenance manifest JSON')
    parser.add_argument('--engine', type=pathlib.Path, help='Explicit engine binary path')
    parser.add_argument('--out', type=pathlib.Path, required=True, help='Output JSON report path')
    parser.add_argument('--repo-root', type=pathlib.Path, default=ROOT, help='Repository root path')
    args = parser.parse_args(argv)
    report = run_m11_ceilings_and_cancel(args.repo_root, args.out, engine_manifest=args.engine_manifest, engine_bin=args.engine)
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
