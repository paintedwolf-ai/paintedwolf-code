"""Zero-model M06 HTTP & loopback fixture replay through an isolated application."""
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
import uuid

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parents[1]
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

import smoke
from portable_command.contracts import ROOT
from portable_command.harness import create_isolated_session, send_tool_calls
from portable_command.observe import parse_tool_result
from portable_command.provenance import make_app_factory, source_identity
from portable_command.reporting import structured_rejection

OK_BODY = b'm06-fixture-ok-payload\nline-2\n'
PRIOR_BYTES = b'prior-bytes\n'


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def sha256_file(path):
    p = pathlib.Path(path)
    return sha256_bytes(p.read_bytes()) if p.is_file() else None


class LoopbackFixtureServer:
    """Isolated 127.0.0.1:0 TCP/HTTP fixture with an independent request log."""

    def __init__(self):
        self._lock = threading.Lock()
        self.requests = []
        self._server = None
        self._thread = None

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
                method = parts[0] if len(parts) > 0 else ''
                path = parts[1] if len(parts) > 1 else ''
                headers = {}
                for line in lines[1:]:
                    if ':' in line:
                        k, v = line.split(':', 1)
                        headers[k.strip().lower()] = v.strip()
                length = int(headers.get('content-length', '0') or '0')
                body = rest
                while len(body) < length:
                    chunk = self.request.recv(min(4096, length - len(body)))
                    if not chunk:
                        break
                    body += chunk
                body = body[:length]
                with owner._lock:
                    seq = len(owner.requests) + 1
                    owner.requests.append({
                        'seq': seq,
                        'method': method,
                        'path': path,
                        'headers': headers,
                        'body': body.decode('utf-8', errors='replace'),
                    })
                if method == 'GET' and path == '/ok':
                    resp = (
                        f'HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n'
                        f'Content-Length: {len(OK_BODY)}\r\nConnection: close\r\n\r\n'
                    ).encode('ascii') + OK_BODY
                    self.request.sendall(resp)
                elif method == 'GET' and path == '/interrupted':
                    resp = (
                        b'HTTP/1.1 200 OK\r\nContent-Length: 100\r\n'
                        b'Connection: close\r\n\r\npartial-prefix'
                    )
                    self.request.sendall(resp)
                elif method == 'POST' and path == '/commit':
                    # Request already recorded; drop TCP connection with zero response bytes.
                    return
                else:
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
        if self._server is not None:
            self._server.shutdown()
            self._server.server_close()
        if self._thread is not None:
            self._thread.join(timeout=3)


def _choose_approve_option(options):
    enabled = [o for o in (options or []) if o.get('decision_action') == 'approve' and not o.get('disabled')]
    for pref in (
        lambda o: o.get('rung') == 'chat' and o.get('kind') == 'lease',
        lambda o: o.get('kind') == 'current_action',
        lambda _o: True,
    ):
        matches = [o for o in enabled if pref(o)]
        if matches:
            return matches[0]
    return None


class ApprovalPoller:
    """Approve host-authored loopback/HTTP checkpoints during scripted tool turns."""

    def __init__(self, application, session_id):
        self._app = application
        self._sid = session_id
        self._stop = threading.Event()
        self._seen = set()
        self.approved = []
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
                    self.approved.append({
                        'checkpoint_id': cid,
                        'tool_call_id': payload.get('tool_call_id'),
                        'subject_kind': ((payload.get('plan') or {}).get('subject') or {}).get('kind'),
                        'option_id': opt['id'],
                        'rung': opt.get('rung'),
                    })
            self._stop.wait(0.15)

    def close(self):
        self._stop.set()
        self._thread.join(timeout=3)


def _run_turn(application, session_id, surface, cmd, port):
    args = {'command': cmd, 'capability_request': {'loopback_connect': {'ports': [port]}}}
    results = send_tool_calls(application, session_id, [{'name': 'command', 'args': args}], surface=surface)
    call_id, raw_res = results[-1]
    raw_parsed, payload = parse_tool_result({'tool_result_json': json.dumps(raw_res)})
    code, details = structured_rejection(raw_parsed, payload)
    return {
        'tool_call_id': call_id,
        'command': cmd,
        'outcome': raw_parsed.get('outcome'),
        'state': payload.get('state'),
        'status': payload.get('status'),
        'completed_effects': payload.get('completed_effects'),
        'uncertain_effects': payload.get('uncertain_effects'),
        'rejection_code': code,
        'rejection_details': details,
        'program_id': payload.get('program_id') or (raw_parsed.get('command_program') or {}).get('id'),
    }


def run_m06_replay(repo_root, out_path, engine_manifest=None, engine_bin=None):
    identity = source_identity(repo_root, profile='implement', engine_bin=engine_bin, engine_manifest=engine_manifest)
    fixture = LoopbackFixtureServer()
    port = fixture.start()
    workdir = pathlib.Path(tempfile.mkdtemp(prefix='m06-http-replay-'))
    app_dir, project_dir = workdir / 'app', workdir / 'project'
    project_dir.mkdir(parents=True, exist_ok=True)
    dest_file = project_dir / 'dest.txt'
    dest_file.write_bytes(PRIOR_BYTES)
    prior_sha = sha256_bytes(PRIOR_BYTES)

    application = make_app_factory(engine_bin, smoke.Application)(app_dir)
    poller = None
    approvals_granted = []
    try:
        application.ready()
        sid, project_id, root_id, surface = create_isolated_session(application, project_dir, {'profile': 'implement'})
        application.request(f'/v1/sessions/{sid}/prompts', {'operation_id': str(uuid.uuid4()), 'text': 'Execute M06 HTTP & loopback fixture.'})
        send_tool_calls(application, sid, [{'name': 'update_progress', 'args': {'content': '- [ ] Run M06 HTTP & loopback fixture'}}], surface=surface)

        poller = ApprovalPoller(application, sid)
        poller.start()

        t1 = _run_turn(application, sid, surface, f'curl -fsS http://127.0.0.1:{port}/ok > ./downloaded.txt', port)
        t2 = _run_turn(application, sid, surface, f'curl -fsS http://127.0.0.1:{port}/interrupted > ./dest.txt', port)
        t3 = _run_turn(application, sid, surface, f"curl -fsS -X POST -d 'payload=1' http://127.0.0.1:{port}/commit", port)

        poller.close()
        approvals_granted = list(poller.approved)
        poller = None
        application.request(f'/v1/sessions/{sid}/abort', {})

        invocations = application.request(f'/v1/sessions/{sid}/invocations')
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
    ok_reqs = [r for r in req_log if r['method'] == 'GET' and r['path'] == '/ok']
    int_reqs = [r for r in req_log if r['method'] == 'GET' and r['path'] == '/interrupted']
    commit_reqs = [r for r in req_log if r['method'] == 'POST' and r['path'] == '/commit']

    downloaded = project_dir / 'downloaded.txt'
    dl_bytes = downloaded.read_bytes() if downloaded.is_file() else None
    dest_sha = sha256_file(dest_file)

    checks = {
        'case1_ok_download': (t1['status'] == 0 and dl_bytes == OK_BODY and len(ok_reqs) == 1),
        'case2_interrupted_preserved': (
            (t2['status'] != 0 or t2['state'] != 'completed') and dest_sha == prior_sha and len(int_reqs) == 1
        ),
        'case3_commit_no_resend': (
            (t3['status'] != 0 or (t3['uncertain_effects'] or 0) >= 1 or t3['state'] != 'completed')
            and len(commit_reqs) == 1
            and commit_reqs[0]['body'] == 'payload=1'
        ),
        'provenance_verified': (identity.get('engine_provenance') or {}).get('provenance_verified') is True,
    }
    report = {
        'case_id': 'M06-http-loopback-fixture',
        'model_calls': 0,
        'passed': all(checks.values()),
        'checks': checks,
        'identity': identity,
        'provenance_verified': checks['provenance_verified'],
        'session': {'id': sid, 'project_id': project_id, 'root_id': root_id, 'surface': surface},
        'fixture_server': {'port': port, 'request_log': req_log, 'commit_count': len(commit_reqs)},
        'approvals_granted': approvals_granted,
        'artifacts': {
            'downloaded_sha256': sha256_file(downloaded),
            'expected_ok_sha256': sha256_bytes(OK_BODY),
            'dest_prior_sha256': prior_sha,
            'dest_final_sha256': dest_sha,
        },
        'turns': {'ok_download': t1, 'interrupted_response': t2, 'commit_outcome_loss': t3},
        'invocations': invocations,
        'program_receipts': program_receipts,
    }
    out_file = pathlib.Path(out_path)
    out_file.parent.mkdir(parents=True, exist_ok=True)
    out_file.write_text(json.dumps(report, indent=2) + '\n')
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description='Zero-model M06 HTTP & loopback fixture replay.')
    parser.add_argument('--engine-manifest', type=pathlib.Path, help='Build-time provenance manifest JSON')
    parser.add_argument('--engine', type=pathlib.Path, help='Explicit engine binary path')
    parser.add_argument('--out', type=pathlib.Path, required=True, help='Output JSON report path')
    parser.add_argument('--repo-root', type=pathlib.Path, default=ROOT, help='Repository root path')
    args = parser.parse_args(argv)
    report = run_m06_replay(args.repo_root, args.out, engine_manifest=args.engine_manifest, engine_bin=args.engine)
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
