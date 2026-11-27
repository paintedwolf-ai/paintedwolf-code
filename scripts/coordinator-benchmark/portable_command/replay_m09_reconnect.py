"""Zero-model M09 live reconnect-without-restart replay through an isolated application."""
import argparse
import contextlib
import hashlib
import json
import pathlib
import sqlite3
import sys
import tempfile
import urllib.error
import urllib.request
import uuid

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parents[1]
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

import smoke
import smoke_workers
from bootstrap_policy import open_request
from portable_command.contracts import ROOT
from portable_command.harness import SSECollector, create_isolated_session, send_tool_calls
from portable_command.observe import parse_tool_result
from portable_command.provenance import make_app_factory, source_identity
from portable_command.reporting import structured_rejection

INIT_BYTES = b'init\n'
APPEND_TOKEN = 'append-once-m09-reconnect'
EXPECTED_LOG = b'init\nappend-once-m09-reconnect\n'
FOLLOWUP_BYTES = b'post-reconnect-ok\n'


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def sha256_file(path):
    p = pathlib.Path(path)
    return sha256_bytes(p.read_bytes()) if p.is_file() else None


def _fresh_http_get(base_url, token, path, *, missing=False):
    """Issue a fresh, non-pooled HTTP GET simulating a reconnected client."""
    req = urllib.request.Request(
        base_url + path,
        headers={'Authorization': 'Bearer ' + token, 'Connection': 'close'},
        method='GET',
    )
    try:
        with open_request(req) as resp:
            return json.load(resp)
    except urllib.error.HTTPError as exc:
        if missing and exc.code == 404:
            return None
        raise


def _receipt_state(receipt):
    if not isinstance(receipt, dict):
        return None, None
    prog = receipt.get('program') if isinstance(receipt.get('program'), dict) else receipt
    return prog.get('state'), prog.get('status_code')


def _find_tool_result_msg(messages_payload, call_id):
    msgs = messages_payload.get('messages') if isinstance(messages_payload, dict) else (messages_payload or [])
    for msg in msgs:
        if not isinstance(msg, dict):
            continue
        tr = msg.get('tool_result') if isinstance(msg.get('tool_result'), dict) else {}
        if tr.get('tool_call_id') == call_id or msg.get('tool_result_call_id') == call_id:
            return msg, tr
    return None, None


def run_m09_reconnect_replay(repo_root, out_path, engine_manifest=None, engine_bin=None):
    identity = source_identity(repo_root, profile='implement', engine_bin=engine_bin, engine_manifest=engine_manifest)
    workdir = pathlib.Path(tempfile.mkdtemp(prefix='m09-reconnect-replay-'))
    app_dir, project_dir = workdir / 'app', workdir / 'project'
    project_dir.mkdir(parents=True, exist_ok=True)
    counter_file = project_dir / 'counter.log'
    counter_file.write_bytes(INIT_BYTES)

    app = make_app_factory(engine_bin, smoke.Application)(app_dir)
    sse_before = sse_after = None
    try:
        app.ready()
        initial_pid = app.process.pid
        sid, project_id, root_id, surface = create_isolated_session(app, project_dir, {'profile': 'implement'})
        app.request(f'/v1/sessions/{sid}/prompts', {'operation_id': str(uuid.uuid4()), 'text': 'Execute M09 live reconnect without restart.'})
        send_tool_calls(app, sid, [{'name': 'update_progress', 'args': {'content': '- [ ] Run M09 live reconnect without restart'}}], surface=surface)

        # Open an SSE client stream before the mutating command, then drop it mid-flight.
        sse_before = SSECollector(app.url, app.token, workdir / 'sse-before.jsonl')
        sse_before.start()

        mutate_cmd = f"printf '{APPEND_TOKEN}\\n' >> ./counter.log && cat ./counter.log"
        req = app.wait_for(lambda: smoke_workers.pending(app, sid), 'm09 reconnect mutate turn', sid)
        mutate_call_id = str(uuid.uuid4())
        app.request('/harness/llm/respond', {'id': req['id'], 'tool_calls': [{'id': mutate_call_id, 'name': 'command', 'args': {'command': mutate_cmd}}]})

        # Simulate real client disconnect without restarting lycaon-dev.
        sse_before.close()
        sse_before_events = sse_before.count
        sse_before = None

        # Reconnect with a fresh SSE stream and fresh HTTP client against the SAME running engine.
        sse_after = SSECollector(app.url, app.token, workdir / 'sse-after.jsonl')
        sse_after.start()

        def recovered_message():
            payload = _fresh_http_get(app.url, app.token, f'/v1/sessions/{sid}/messages')
            msg, tr = _find_tool_result_msg(payload, mutate_call_id)
            return (msg, tr, payload) if msg is not None else None

        rec_msg, rec_tr, messages_after = app.wait_for(recovered_message, 'reconnected tool result message', sid)
        raw_parsed, parsed_payload = parse_tool_result({'tool_result_json': json.dumps(rec_tr)})
        rej_code, _ = structured_rejection(raw_parsed, parsed_payload)
        mutate_pid = parsed_payload.get('program_id') or (rec_tr.get('command_program') or {}).get('id')

        # Exercise repeated API recovery reads via fresh HTTP client (no effect rerun on reload).
        invocations = _fresh_http_get(app.url, app.token, f'/v1/sessions/{sid}/invocations')
        receipt_1 = _fresh_http_get(app.url, app.token, f'/v1/sessions/{sid}/program-receipts/{mutate_pid}?limit=50', missing=True) if mutate_pid else None
        receipt_2 = _fresh_http_get(app.url, app.token, f'/v1/sessions/{sid}/program-receipts/{mutate_pid}?limit=50', missing=True) if mutate_pid else None

        with contextlib.closing(sqlite3.connect(app_dir / 'store.db')) as db:
            prog_rows = db.execute(
                'SELECT p.id, p.state, p.status_code FROM invocation_programs p '
                'JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE r.session_id=?',
                (sid,),
            ).fetchall()
            exec_rows = db.execute(
                'SELECT operation_id, state FROM invocation_program_executions WHERE program_id=?',
                (mutate_pid,),
            ).fetchall() if mutate_pid else []

        counter_after_reconnect = counter_file.read_bytes()
        counter_sha_after_reconnect = sha256_bytes(counter_after_reconnect)
        append_occurrences = counter_after_reconnect.decode('utf-8', errors='replace').count(APPEND_TOKEN)

        # Run a subsequent ordinary command in the same session after reconnect.
        followup_cmd = "printf 'post-reconnect-ok\\n' > ./after_reconnect.txt && cat ./after_reconnect.txt"
        followup_res = send_tool_calls(app, sid, [{'name': 'command', 'args': {'command': followup_cmd}}], surface=surface)
        followup_call_id, followup_raw = followup_res[-1]
        _, followup_payload = parse_tool_result({'tool_result_json': json.dumps(followup_raw)})

        same_pid_no_restart = (app.process.pid == initial_pid and app.process.poll() is None)
        app.request(f'/v1/sessions/{sid}/abort', {})
        sse_after.close()
        sse_after_events = sse_after.count
        sse_after = None
    finally:
        if sse_before is not None:
            sse_before.close()
        if sse_after is not None:
            sse_after.close()
        app.close(cleanup=False)

    r1_state, r1_status = _receipt_state(receipt_1)
    r2_state, r2_status = _receipt_state(receipt_2)
    followup_sha = sha256_file(project_dir / 'after_reconnect.txt')
    final_counter_sha = sha256_file(counter_file)

    checks = {
        'no_restart_same_engine_pid': same_pid_no_restart,
        'api_recovered_completed_state': (
            bool(mutate_pid)
            and parsed_payload.get('status') == 0
            and parsed_payload.get('state') == 'completed'
            and rej_code is None
            and r1_state == 'completed' and r1_status == 0
            and r2_state == 'completed' and r2_status == 0
        ),
        'original_effect_count_once_no_duplicate': (
            append_occurrences == 1
            and counter_sha_after_reconnect == sha256_bytes(EXPECTED_LOG)
            and final_counter_sha == sha256_bytes(EXPECTED_LOG)
        ),
        'sqlite_program_and_executions_match': (
            len(prog_rows) == 1
            and prog_rows[0][0] == mutate_pid
            and prog_rows[0][1] == 'completed'
            and prog_rows[0][2] == 0
            and len(exec_rows) >= 1
            and all(row[1] == 'completed' for row in exec_rows)
        ),
        'subsequent_command_same_session_succeeds': (
            followup_payload.get('status') == 0
            and followup_payload.get('stdout') == FOLLOWUP_BYTES.decode('utf-8')
            and followup_sha == sha256_bytes(FOLLOWUP_BYTES)
        ),
        'provenance_verified': (identity.get('engine_provenance') or {}).get('provenance_verified') is True,
    }
    report = {
        'case_id': 'M09-live-reconnect-without-restart', 'model_calls': 0,
        'passed': all(checks.values()), 'checks': checks,
        'identity': identity, 'provenance_verified': checks['provenance_verified'],
        'engine': {'pid': initial_pid, 'restarted': False, 'sse_before_events': sse_before_events, 'sse_after_events': sse_after_events},
        'session': {'id': sid, 'project_id': project_id, 'root_id': root_id, 'surface': surface},
        'mutate': {
            'tool_call_id': mutate_call_id, 'program_id': mutate_pid, 'payload': parsed_payload,
            'append_occurrences': append_occurrences, 'counter_sha256': final_counter_sha,
            'expected_counter_sha256': sha256_bytes(EXPECTED_LOG),
            'sqlite_programs': [{'id': r[0], 'state': r[1], 'status_code': r[2]} for r in prog_rows],
            'sqlite_executions': [{'operation_id': r[0], 'state': r[1]} for r in exec_rows],
        },
        'followup': {'tool_call_id': followup_call_id, 'payload': followup_payload, 'sha256': followup_sha},
        'invocations': invocations, 'program_receipt_reload_1': receipt_1, 'program_receipt_reload_2': receipt_2,
        'recovered_message_id': rec_msg.get('id') if isinstance(rec_msg, dict) else None,
        'messages_count': len(messages_after.get('messages') or []) if isinstance(messages_after, dict) else 0,
    }
    out_file = pathlib.Path(out_path)
    out_file.parent.mkdir(parents=True, exist_ok=True)
    out_file.write_text(json.dumps(report, indent=2) + '\n')
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description='Zero-model M09 live reconnect-without-restart replay.')
    parser.add_argument('--engine-manifest', type=pathlib.Path, help='Build-time provenance manifest JSON')
    parser.add_argument('--engine', type=pathlib.Path, help='Explicit engine binary path')
    parser.add_argument('--out', type=pathlib.Path, required=True, help='Output JSON report path')
    parser.add_argument('--repo-root', type=pathlib.Path, default=ROOT, help='Repository root path')
    args = parser.parse_args(argv)
    report = run_m09_reconnect_replay(args.repo_root, args.out, engine_manifest=args.engine_manifest, engine_bin=args.engine)
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
