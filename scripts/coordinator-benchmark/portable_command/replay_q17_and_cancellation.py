"""Zero-model Q17 (listener-rebind) & Q15/Q16 (command abort/cancellation) live replay."""
import argparse
import contextlib
import hashlib
import json
import pathlib
import socket
import socketserver
import sqlite3
import sys
import tempfile
import threading
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


def _pick_free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]

def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()

def sha256_file(path):
    p = pathlib.Path(path)
    return sha256_bytes(p.read_bytes()) if p.is_file() else None


class UnrelatedRebindServer:
    """Unrelated host process binding the exact port previously owned by the session."""

    def __init__(self, port):
        self._port, self._lock, self.hits, self._server, self._thread = port, threading.Lock(), [], None, None

    def start(self):
        owner = self

        class Handler(socketserver.BaseRequestHandler):
            def handle(self):
                self.request.settimeout(2.0)
                data = b''
                with contextlib.suppress(OSError):
                    data = self.request.recv(4096)
                with owner._lock:
                    owner.hits.append(data.decode('latin-1', errors='replace'))
                with contextlib.suppress(OSError):
                    self.request.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: 9\r\nConnection: close\r\n\r\nunrelated')

        class _Server(socketserver.ThreadingTCPServer):
            allow_reuse_address = True
            daemon_threads = True

        self._server = _Server(('127.0.0.1', self._port), Handler)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()

    def hit_count(self):
        with self._lock:
            return len(self.hits)

    def close(self):
        if self._server is not None:
            self._server.shutdown()
            self._server.server_close()
        if self._thread is not None:
            self._thread.join(timeout=3)


class CheckpointPoller:
    """Approve or reject pending tool_approval checkpoints during scripted turns."""

    def __init__(self, app, sid, *, approve):
        self._app, self._sid, self._approve = app, sid, approve
        self._stop, self._seen, self.decisions = threading.Event(), set(), []
        self._thread = threading.Thread(target=self._run, daemon=True)

    def start(self):
        self._thread.start()

    def _run(self):
        while not self._stop.is_set():
            with contextlib.suppress(Exception):
                data = self._app.request(f'/v1/sessions/{self._sid}/checkpoints?status=pending') or {}
                for cp in data.get('checkpoints') or []:
                    cid = cp.get('id')
                    if not cid or cid in self._seen:
                        continue
                    payload = cp.get('tool_approval') or {}
                    self._seen.add(cid)
                    if self._approve:
                        opts = [o for o in ((payload.get('plan') or {}).get('options') or []) if o.get('decision_action') == 'approve' and not o.get('disabled')]
                        if not opts:
                            continue
                        choice = next((o for o in opts if o.get('rung') == 'chat' and o.get('kind') == 'lease'), opts[0])
                        body = {'kind': 'tool_approval', 'action': 'approve', 'option_id': choice['id']}
                    else:
                        body = {'kind': 'tool_approval', 'action': 'reject', 'guidance': 'rebound listener denied'}
                    self._app.request(f'/v1/sessions/{self._sid}/checkpoints/{cid}', body)
                    self.decisions.append({'checkpoint_id': cid, 'action': body['action']})
            self._stop.wait(0.15)

    def close(self):
        self._stop.set()
        self._thread.join(timeout=3)


def _run_cmd(app, sid, surface, cmd, *, cap_req=None):
    args = {'command': cmd} if not cap_req else {'command': cmd, 'capability_request': cap_req}
    call_id, raw_res = send_tool_calls(app, sid, [{'name': 'command', 'args': args}], surface=surface)[-1]
    raw_parsed, payload = parse_tool_result({'tool_result_json': json.dumps(raw_res)})
    code, details = structured_rejection(raw_parsed, payload)
    return {
        'tool_call_id': call_id, 'command': cmd, 'outcome': raw_parsed.get('outcome'),
        'state': payload.get('state'), 'status': payload.get('status'),
        'stdout': payload.get('stdout'), 'stderr': payload.get('stderr'),
        'rejection_code': code, 'rejection_details': details,
        'program_id': payload.get('program_id') or (raw_parsed.get('command_program') or {}).get('id'),
    }


def _wait_idle(app, sid):
    app.wait_for(lambda: app.request('/v1/sessions/' + sid)['status'] == 'idle', 'session idle')

def _open_session(app, project_id):
    sid = app.request('/v1/sessions', {'project_id': project_id, 'posture': 'build'})['id']
    app.request('/harness/model-limit', {'session_id': sid, 'limit': 20})
    _wait_idle(app, sid)
    return sid


def run_q17_and_cancellation(repo_root, out_path, engine_manifest=None, engine_bin=None):
    identity = source_identity(repo_root, profile='implement', engine_bin=engine_bin, engine_manifest=engine_manifest)
    workdir = pathlib.Path(tempfile.mkdtemp(prefix='q17-cancel-replay-'))
    app_dir, project_dir = workdir / 'app', workdir / 'project'
    project_dir.mkdir(parents=True, exist_ok=True)
    port = _pick_free_port()

    app = make_app_factory(engine_bin, smoke.Application)(app_dir)
    rebind_srv, poller = None, None
    try:
        app.ready()
        sid, project_id, root_id, surface = create_isolated_session(app, project_dir, {'profile': 'implement'})
        app.request(f'/v1/sessions/{sid}/prompts', {'operation_id': str(uuid.uuid4()), 'text': 'Execute Q17 listener-rebind transition.'})
        send_tool_calls(app, sid, [{'name': 'update_progress', 'args': {'content': '- [ ] Run Q17 listener-rebind witness'}}], surface=surface)

        # Phase 1: Session launches its own loopback HTTP listener on port P and curls it without loopback_connect.
        ready_file = project_dir / 'owned.ready'
        (project_dir / 'owned_srv.py').write_text(
            'import http.server, pathlib\nclass H(http.server.BaseHTTPRequestHandler):\n'
            ' def do_GET(self): b=b"owned-listener\\n"; self.send_response(200); self.send_header("Content-Length",str(len(b))); self.end_headers(); self.wfile.write(b)\n'
            ' def log_message(self,*a): pass\n'
            f's=http.server.HTTPServer(("127.0.0.1",{port}),H); pathlib.Path("owned.ready").write_text("ok"); s.serve_forever()\n'
        )
        poller = CheckpointPoller(app, sid, approve=True)
        poller.start()
        q17_start = _run_cmd(app, sid, surface, 'external -- python3 ./owned_srv.py &', cap_req={'local_listen': {'ports': [port]}})
        poller.close()
        poller = None
        app.wait_for(lambda: ready_file.is_file(), 'owned listener ready', sid, timeout=30)

        q17_owned_curl = None
        for _ in range(10):
            q17_owned_curl = _run_cmd(app, sid, surface, f'curl -fsS http://127.0.0.1:{port}/owned')
            if q17_owned_curl['status'] == 0 and q17_owned_curl['stdout'] == 'owned-listener\n':
                break
            time.sleep(0.5)

        # Phase 2: Stop owned listener and rebind the exact same port with an unrelated host process.
        q17_stop_owned = _run_cmd(app, sid, surface, 'kill %1; wait %1')
        rebind_srv = UnrelatedRebindServer(port)
        for attempt in range(30):
            try:
                rebind_srv.start()
                break
            except OSError:
                if attempt == 29:
                    raise
                time.sleep(0.2)

        # Phase 3: Curl exact same URL; rebind must revoke access with zero hits to unrelated server.
        poller = CheckpointPoller(app, sid, approve=False)
        poller.start()
        q17_rebound_curl = _run_cmd(app, sid, surface, f'curl -fsS http://127.0.0.1:{port}/owned')
        poller.close()
        poller = None
        app.request(f'/v1/sessions/{sid}/abort', {})
        _wait_idle(app, sid)

        # Case B: Q15/Q16 cancellation via POST /v1/sessions/{id}/abort (distinct from ST01-ST03 Stop).
        cancel_sid = _open_session(app, project_id)
        app.request(f'/v1/sessions/{cancel_sid}/prompts', {'operation_id': str(uuid.uuid4()), 'text': 'Execute Q15/Q16 cancellation witness.'})
        send_tool_calls(app, cancel_sid, [{'name': 'update_progress', 'args': {'content': '- [ ] Run cancellation witness'}}], surface=surface)
        req = app.wait_for(lambda: smoke_workers.pending(app, cancel_sid), 'cancellation turn', cancel_sid)
        cancel_call_id = str(uuid.uuid4())
        # Native `sleep 0.25` (< 10s park threshold) keeps the `:` loop live for mid-loop /abort.
        app.request('/harness/llm/respond', {'id': req['id'], 'tool_calls': [{'id': cancel_call_id, 'name': 'command', 'args': {'command': 'while true; do :; sleep 0.25; done'}}]})

        def loop_started():
            with contextlib.closing(sqlite3.connect(app_dir / 'store.db')) as db:
                row = db.execute(
                    'SELECT p.id, (SELECT count(*) FROM invocation_program_executions e WHERE e.program_id=p.id) '
                    'FROM invocation_programs p JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE r.tool_call_id=?',
                    (cancel_call_id,),
                ).fetchone()
            return row if (row and row[1] >= 3) else None

        cancel_pid, _ = app.wait_for(loop_started, 'loop statements admitted', cancel_sid, timeout=30)
        app.request(f'/v1/sessions/{cancel_sid}/abort', {'reason': 'q15_command_abort_cancellation'})
        _wait_idle(app, cancel_sid)

        with contextlib.closing(sqlite3.connect(app_dir / 'store.db')) as db:
            p_row = db.execute('SELECT state, status_code, stop_code FROM invocation_programs WHERE id=?', (cancel_pid,)).fetchone()
            count_at_idle = db.execute('SELECT count(*) FROM invocation_program_executions WHERE program_id=?', (cancel_pid,)).fetchone()[0]
            unsettled = db.execute("SELECT count(*) FROM invocation_program_executions WHERE program_id=? AND state IN ('prepared','waiting','running')", (cancel_pid,)).fetchone()[0]
        time.sleep(0.5)
        with contextlib.closing(sqlite3.connect(app_dir / 'store.db')) as db:
            count_after_wait = db.execute('SELECT count(*) FROM invocation_program_executions WHERE program_id=?', (cancel_pid,)).fetchone()[0]

        rec_sid = _open_session(app, project_id)
        app.request(f'/v1/sessions/{rec_sid}/prompts', {'operation_id': str(uuid.uuid4()), 'text': 'Post-cancellation recovery.'})
        send_tool_calls(app, rec_sid, [{'name': 'update_progress', 'args': {'content': '- [ ] Post-cancel recovery'}}], surface=surface)
        post_cancel = _run_cmd(app, rec_sid, surface, "printf 'post-cancel-ok\\n' > ./cancel_ok.txt && cat ./cancel_ok.txt")
        app.request(f'/v1/sessions/{rec_sid}/abort', {})
        cancel_receipt = app.request(f'/v1/sessions/{cancel_sid}/program-receipts/{cancel_pid}?limit=50', missing=True)
    finally:
        if poller is not None:
            poller.close()
        if rebind_srv is not None:
            rebind_srv.close()
        app.close(cleanup=False)

    rebound_hits = rebind_srv.hit_count() if rebind_srv is not None else -1
    checks = {
        'q17_observed_owned_listener_succeeded': (
            q17_start['status'] == 0 and q17_owned_curl is not None
            and q17_owned_curl['status'] == 0 and q17_owned_curl['stdout'] == 'owned-listener\n'
        ),
        'q17_revoked_on_rebind_zero_hits': (
            (q17_rebound_curl['outcome'] == 'rejected' or q17_rebound_curl['status'] != 0 or bool(q17_rebound_curl['rejection_code']))
            and rebound_hits == 0
        ),
        'cancel_admission_closed_and_no_later_child': (
            p_row is not None and p_row[0] == 'interrupted' and p_row[1] != 0
            and count_at_idle >= 3 and count_after_wait == count_at_idle
        ),
        'cancel_resources_reclaimed_and_recovery_ok': (
            unsettled == 0 and post_cancel['status'] == 0 and post_cancel['stdout'] == 'post-cancel-ok\n'
            and sha256_file(project_dir / 'cancel_ok.txt') == sha256_bytes(b'post-cancel-ok\n')
        ),
        'provenance_verified': (identity.get('engine_provenance') or {}).get('provenance_verified') is True,
    }
    report = {
        'case_id': 'Q17-listener-rebind-and-Q15-cancellation', 'model_calls': 0,
        'passed': all(checks.values()), 'checks': checks, 'identity': identity,
        'provenance_verified': checks['provenance_verified'], 'st01_st03_stop_fence_separate': True,
        'q17': {
            'port': port, 'start': q17_start, 'owned_curl': q17_owned_curl,
            'stop_owned': q17_stop_owned, 'rebound_curl': q17_rebound_curl, 'unrelated_hits': rebound_hits,
        },
        'cancellation': {
            'control_surface': 'POST /v1/sessions/{id}/abort (command abort/cancellation; distinct from ST01-ST03 Stop)',
            'session_id': cancel_sid, 'program_id': cancel_pid,
            'db_program': {'state': p_row[0], 'status_code': p_row[1], 'stop_code': p_row[2]} if p_row else None,
            'exec_count_at_idle': count_at_idle, 'exec_count_after_wait': count_after_wait, 'unsettled_children': unsettled,
            'receipt': cancel_receipt, 'post_cancel_recovery': post_cancel,
        },
    }
    out_file = pathlib.Path(out_path)
    out_file.parent.mkdir(parents=True, exist_ok=True)
    out_file.write_text(json.dumps(report, indent=2) + '\n')
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description='Zero-model Q17 listener-rebind and Q15/Q16 cancellation replay.')
    parser.add_argument('--engine-manifest', type=pathlib.Path, help='Build-time provenance manifest JSON')
    parser.add_argument('--engine', type=pathlib.Path, help='Explicit engine binary path')
    parser.add_argument('--out', type=pathlib.Path, required=True, help='Output JSON report path')
    parser.add_argument('--repo-root', type=pathlib.Path, default=ROOT, help='Repository root path')
    args = parser.parse_args(argv)
    report = run_q17_and_cancellation(args.repo_root, args.out, engine_manifest=args.engine_manifest, engine_bin=args.engine)
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
