"""Zero-model live-app replay witness for Q15 (partial-write-unknown-http)."""
import argparse
import contextlib
import hashlib
import json
import pathlib
import socketserver
import sqlite3
import sys
import tempfile
import threading
import time
import uuid


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def sha256_file(path):
    p = pathlib.Path(path)
    return sha256_bytes(p.read_bytes()) if p.is_file() else None


class Q15LoopbackServer:
    """Isolated 127.0.0.1:0 HTTP fixture with /commit (drop) and /hold (block until released)."""

    def __init__(self):
        self._lock = threading.Lock()
        self.requests = []
        self.hold_received = threading.Event()
        self.hold_release = threading.Event()
        self._server, self._thread = None, None

    def start(self):
        owner = self

        class Handler(socketserver.BaseRequestHandler):
            def handle(self):
                self.request.settimeout(5.0)
                buf = b''
                while b'\r\n\r\n' not in buf and len(buf) < 65536:
                    chunk = self.request.recv(4096)
                    if not chunk:
                        break
                    buf += chunk
                if b'\r\n\r\n' not in buf:
                    return
                head, rest = buf.split(b'\r\n\r\n', 1)
                lines = head.decode('latin-1', errors='replace').split('\r\n')
                parts = lines[0].split(' ')
                method, path = (parts[0] if parts else ''), (parts[1] if len(parts) > 1 else '')
                headers = {k.strip().lower(): v.strip() for ln in lines[1:] if ':' in ln for k, v in [ln.split(':', 1)]}
                length = int(headers.get('content-length', '0') or '0')
                body = rest
                while len(body) < length:
                    chunk = self.request.recv(min(4096, length - len(body)))
                    if not chunk:
                        break
                    body += chunk
                body = body[:length]
                with owner._lock:
                    owner.requests.append({
                        'seq': len(owner.requests) + 1, 'method': method, 'path': path,
                        'body': body.decode('utf-8', errors='replace'),
                    })
                if method == 'POST' and path == '/commit':
                    return  # Drop TCP with zero response bytes after recording receipt.
                if method == 'POST' and path == '/hold':
                    owner.hold_received.set()
                    owner.hold_release.wait(timeout=15.0)
                    return
                self.request.sendall(b'HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')

        class _Server(socketserver.ThreadingTCPServer):
            allow_reuse_address = True
            daemon_threads = True

        self._server = _Server(('127.0.0.1', 0), Handler)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()
        return self._server.server_address[1]

    def snapshot(self):
        with self._lock:
            return [dict(r) for r in self.requests]

    def close(self):
        self.hold_release.set()
        if self._server is not None:
            self._server.shutdown()
            self._server.server_close()
        if self._thread is not None:
            self._thread.join(timeout=3)


def _choose_approve_option(options):
    enabled = [o for o in (options or []) if o.get('decision_action') == 'approve' and not o.get('disabled')]
    for pref in (lambda o: o.get('rung') == 'chat' and o.get('kind') == 'lease', lambda o: o.get('kind') == 'current_action', lambda _o: True):
        matches = [o for o in enabled if pref(o)]
        if matches:
            return matches[0]
    return None


class ApprovalPoller:
    """Approve host-authored loopback checkpoints during scripted tool turns."""

    def __init__(self, application, session_id):
        self._app, self._sid = application, session_id
        self._stop, self._seen, self.approved = threading.Event(), set(), []
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
                    opt = _choose_approve_option((payload.get('plan') or {}).get('options'))
                    if opt is None:
                        continue
                    self._seen.add(cid)
                    self._app.request(
                        f'/v1/sessions/{self._sid}/checkpoints/{cid}',
                        {'kind': 'tool_approval', 'action': 'approve', 'option_id': opt['id']},
                    )
                    self.approved.append({'checkpoint_id': cid, 'option_id': opt['id']})
            self._stop.wait(0.15)

    def close(self):
        self._stop.set()
        self._thread.join(timeout=3)


def parse_args():
    p = argparse.ArgumentParser(description='Zero-model Q15 partial-write-unknown-http live replay.')
    p.add_argument('--engine-manifest', required=True)
    p.add_argument('--out', required=True)
    p.add_argument('--repo-root', default=None)
    return p.parse_args()


def main():
    args = parse_args()
    if args.repo_root:
        root = pathlib.Path(args.repo_root).resolve()
        for rel in ('scripts', 'scripts/coordinator-benchmark'):
            cand = str(root / rel)
            if cand not in sys.path:
                sys.path.insert(0, cand)

    import smoke
    from portable_command.harness import create_isolated_session, send_tool_calls
    from portable_command.observe import parse_tool_result
    from portable_command.provenance import make_app_factory, source_identity
    from portable_command.reporting import structured_rejection

    manifest = pathlib.Path(args.engine_manifest).resolve()
    engine = pathlib.Path(json.loads(manifest.read_text())['executable_path'])
    repo_root = pathlib.Path(args.repo_root).resolve() if args.repo_root else pathlib.Path(__file__).resolve().parents[3]
    identity = source_identity(repo_root, profile='implement', engine_bin=engine, engine_manifest=manifest)
    if not (identity.get('engine_provenance') or {}).get('provenance_verified'):
        raise RuntimeError('engine manifest provenance_verified is false')

    fixture = Q15LoopbackServer()
    port = fixture.start()
    workdir = pathlib.Path(tempfile.mkdtemp(prefix='q15-unknown-http-'))
    app_dir, project_dir = workdir / 'app', workdir / 'project'
    project_dir.mkdir(parents=True, exist_ok=True)
    application = make_app_factory(engine, smoke.Application)(app_dir)
    poller, program_receipts = None, {}
    t1_info, t2_info = {}, {}

    try:
        application.ready()
        sid, project_id, root_id, surface = create_isolated_session(application, project_dir, {'profile': 'implement'})
        application.request(f'/v1/sessions/{sid}/prompts', {'operation_id': str(uuid.uuid4()), 'text': 'Execute Q15 partial-write-unknown-http.'})
        send_tool_calls(application, sid, [{'name': 'update_progress', 'args': {'content': '- [ ] Run Q15 partial-write-unknown-http'}}], surface=surface)
        poller = ApprovalPoller(application, sid)
        poller.start()

        # Case 1: transport drop after POST receipt (&& touch ./never)
        cmd1 = f"printf 'first\\n' > ./first.txt && curl -fsS -d payload http://127.0.0.1:{port}/commit && touch ./never"
        [(c1_id, r1_raw)] = send_tool_calls(
            application, sid,
            [{'name': 'command', 'args': {'command': cmd1, 'capability_request': {'loopback_connect': {'ports': [port]}}}}],
            surface=surface,
        )
        r1_parsed, p1 = parse_tool_result({'tool_result_json': json.dumps(r1_raw)})
        code1, det1 = structured_rejection(r1_parsed, p1)
        t1_info = {
            'tool_call_id': c1_id, 'command': cmd1, 'outcome': r1_parsed.get('outcome'),
            'status': p1.get('status'), 'state': p1.get('state'),
            'completed_effects': p1.get('completed_effects'), 'uncertain_effects': p1.get('uncertain_effects'),
            'replay_available': p1.get('replay_available'), 'rejection_code': code1, 'rejection_details': det1,
            'program_id': p1.get('program_id') or (r1_parsed.get('command_program') or {}).get('id'),
        }

        # Case 2: supervisor loss (abort) after POST receipt on exact semicolon form (; touch ./never2)
        cmd2 = f"printf 'first\\n' > ./first2.txt; curl -d payload http://127.0.0.1:{port}/hold; touch ./never2"
        t2_res = {}

        def run_case2():
            try:
                [(c2_id, r2_raw)] = send_tool_calls(
                    application, sid,
                    [{'name': 'command', 'args': {'command': cmd2, 'capability_request': {'loopback_connect': {'ports': [port]}}}}],
                    surface=surface,
                )
                t2_res['call_id'], t2_res['raw'] = c2_id, r2_raw
            except Exception as exc:
                t2_res['error'] = repr(exc)

        worker = threading.Thread(target=run_case2, daemon=True)
        worker.start()
        got_hold = fixture.hold_received.wait(timeout=15.0)
        if got_hold:
            application.request(f'/v1/sessions/{sid}/abort', {})
            time.sleep(0.3)
            fixture.hold_release.set()
        worker.join(timeout=15.0)
        application.wait_for(
            lambda: application.request(f'/v1/sessions/{sid}')['status'] in ('idle', 'interrupted', 'error'),
            'session settled after abort',
        )
        t2_info = {'command': cmd2, 'hold_received': got_hold, 'worker_error': t2_res.get('error')}

        poller.close()
        poller = None
        with contextlib.closing(sqlite3.connect(app_dir / 'store.db')) as db:
            pids = [r[0] for r in db.execute(
                'SELECT p.id FROM invocation_programs p JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE r.session_id=?',
                (sid,),
            ).fetchall()]
        program_receipts = {
            pid: application.request(f'/v1/sessions/{sid}/program-receipts/{pid}?limit=50', missing=True)
            for pid in pids
        }
    finally:
        if poller is not None:
            poller.close()
        application.close(cleanup=False)
        fixture.close()

    req_log = fixture.snapshot()
    commit_reqs = [r for r in req_log if r['method'] == 'POST' and r['path'] == '/commit']
    hold_reqs = [r for r in req_log if r['method'] == 'POST' and r['path'] == '/hold']
    f1, f2 = project_dir / 'first.txt', project_dir / 'first2.txt'
    n1, n2 = project_dir / 'never', project_dir / 'never2'
    c1_pass = (
        f1.is_file() and f1.read_text(encoding='utf-8') == 'first\n'
        and not n1.exists()
        and len(commit_reqs) == 1 and commit_reqs[0]['body'] == 'payload'
        and (t1_info.get('completed_effects') or 0) >= 1
        and (t1_info.get('uncertain_effects') or 0) >= 1
        and t1_info.get('replay_available') is not True
    )
    c2_pass = (
        bool(t2_info.get('hold_received'))
        and f2.is_file() and f2.read_text(encoding='utf-8') == 'first\n'
        and not n2.exists()
        and len(hold_reqs) == 1 and hold_reqs[0]['body'] == 'payload'
    )
    report = {
        'suite': 'q15-partial-write-unknown-http',
        'model_calls': 0,
        'provenance_verified': True,
        'identity': identity,
        'passed': bool(c1_pass and c2_pass),
        'cases': {
            'transport_drop_after_write': {
                'passed': c1_pass, 'turn': t1_info, 'first_sha256': sha256_file(f1),
                'never_absent': not n1.exists(), 'commit_requests': commit_reqs,
            },
            'supervisor_loss_semicolon': {
                'passed': c2_pass, 'turn': t2_info, 'first2_sha256': sha256_file(f2),
                'never2_absent': not n2.exists(), 'hold_requests': hold_reqs,
            },
        },
        'program_receipts': program_receipts,
    }
    out_file = pathlib.Path(args.out).resolve()
    out_file.parent.mkdir(parents=True, exist_ok=True)
    out_file.write_text(json.dumps(report, indent=2) + '\n', encoding='utf-8')
    if not report['passed']:
        raise SystemExit(1)


if __name__ == '__main__':
    main()
