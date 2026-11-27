"""Live host-side witness drivers for Q13 (stale-approval), Q14 (publication-barrier), Q15 (partial-write-unknown-http), and Q17 (listener-rebind)."""
import contextlib
import json
import os
import pathlib
import socket
import socketserver
import threading
import time
import urllib.error
import urllib.request
import uuid
from bootstrap_policy import open_request

WITNESS_CASE_IDS = {'stale-approval', 'publication-barrier', 'partial-write-unknown-http', 'listener-rebind'}


def _pick_free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def _http_json(app, method, path, payload=None):
    data = json.dumps(payload).encode('utf-8') if payload is not None else None
    headers = {'Authorization': 'Bearer ' + app.token}
    if data is not None:
        headers['Content-Type'] = 'application/json'
    req = urllib.request.Request(app.url + path, data=data, method=method, headers=headers)
    try:
        with open_request(req) as resp:
            raw = resp.read().decode('utf-8', errors='replace').strip()
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as exc:
        body = exc.read().decode('utf-8', errors='replace')
        raise RuntimeError(f'HTTP {exc.code} {method} {path}: {body}') from exc


def _open_doc(app, pid, root_id, rel_path, client_id='witness-client'):
    return _http_json(app, 'POST', f'/v1/projects/{pid}/editor-documents', {'root_id': root_id, 'path': rel_path, 'client_id': client_id})


def _replace_doc(app, pid, doc_id, expected_rev, content, client_id='witness-client'):
    return _http_json(app, 'PUT', f'/v1/projects/{pid}/editor-documents/{doc_id}', {
        'history_vector': None, 'session_id': None, 'operation_id': str(uuid.uuid4()),
        'client_id': client_id, 'expected_revision': expected_rev, 'content': content, 'eol': 'lf', 'mixed_eol': False,
    })


def _pick_approve_option(cp):
    ta = cp.get('tool_approval') or {}
    plan = (ta.get('plan') if isinstance(ta, dict) else None) or cp.get('approval_plan') or {}
    if isinstance(plan, dict):
        opts = [o for o in (plan.get('options') or ()) if isinstance(o, dict) and not o.get('disabled') and o.get('decision_action') == 'approve' and o.get('id')]
        for pred in (lambda o: o.get('rung') == 'chat' and o.get('kind') == 'lease', lambda o: o.get('kind') == 'current_action', lambda _o: True):
            m = [o for o in opts if pred(o)]
            if m:
                return m[0]['id']
        if plan.get('recommended_option_id'):
            return plan['recommended_option_id']
    return 'allow_once'


class _LoopbackDropServer:
    """127.0.0.1:0 HTTP fixture for Q15 that records POST /commit then drops TCP with zero response bytes."""
    def __init__(self):
        self._lock, self.requests, self._server, self._thread = threading.Lock(), [], None, None

    def start(self):
        owner = self
        class Handler(socketserver.BaseRequestHandler):
            def handle(self):
                self.request.settimeout(5.0)
                buf = b''
                while b'\r\n\r\n' not in buf and len(buf) < 65536:
                    chunk = self.request.recv(4096)
                    if not chunk: break
                    buf += chunk
                if b'\r\n\r\n' not in buf: return
                head, rest = buf.split(b'\r\n\r\n', 1)
                lines = head.decode('latin-1', errors='replace').split('\r\n')
                parts = lines[0].split(' ')
                method, path = (parts[0] if parts else ''), (parts[1] if len(parts) > 1 else '')
                headers = {k.strip().lower(): v.strip() for ln in lines[1:] if ':' in ln for k, v in [ln.split(':', 1)]}
                length, body = int(headers.get('content-length', '0') or '0'), rest
                while len(body) < length:
                    chunk = self.request.recv(min(4096, length - len(body)))
                    if not chunk: break
                    body += chunk
                with owner._lock:
                    owner.requests.append({'seq': len(owner.requests) + 1, 'method': method, 'path': path, 'body': body[:length].decode('utf-8', errors='replace')})
                if method == 'POST' and path == '/commit': return
                self.request.sendall(b'HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')

        class _Srv(socketserver.ThreadingTCPServer):
            allow_reuse_address, daemon_threads = True, True

        self._server = _Srv(('127.0.0.1', 0), Handler)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()
        return self._server.server_address[1]

    def snapshot(self):
        with self._lock: return [dict(r) for r in self.requests]

    def close(self):
        if self._server is not None: self._server.shutdown(); self._server.server_close()
        if self._thread is not None: self._thread.join(timeout=3)


class _RebindServer:
    """Unrelated host process binding the exact port previously owned by the session (Q17)."""
    def __init__(self, port):
        self._port, self._lock, self.hits, self._server, self._thread = port, threading.Lock(), [], None, None

    def start(self):
        owner = self
        class Handler(socketserver.BaseRequestHandler):
            def handle(self):
                self.request.settimeout(2.0)
                data = b''
                with contextlib.suppress(OSError): data = self.request.recv(4096)
                with owner._lock: owner.hits.append(data.decode('latin-1', errors='replace'))
                with contextlib.suppress(OSError): self.request.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: 9\r\nConnection: close\r\n\r\nunrelated')

        class _Srv(socketserver.ThreadingTCPServer):
            allow_reuse_address, daemon_threads = True, True

        self._server = _Srv(('127.0.0.1', self._port), Handler)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()

    def hit_count(self):
        with self._lock: return len(self.hits)

    def close(self):
        if self._server is not None: self._server.shutdown(); self._server.server_close()
        if self._thread is not None: self._thread.join(timeout=3)


class _CheckpointPoller:
    def __init__(self, app, sid, *, approve, on_checkpoint=None):
        self._app, self._sid, self._approve, self._on_cp = app, sid, approve, on_checkpoint
        self._stop, self._seen, self.events = threading.Event(), set(), []
        self._thread = threading.Thread(target=self._run, daemon=True)

    def start(self):
        self._thread.start()

    def _run(self):
        while not self._stop.is_set():
            with contextlib.suppress(Exception):
                cps = _http_json(self._app, 'GET', f'/v1/sessions/{self._sid}/checkpoints') or {}
                items = cps.get('checkpoints') if isinstance(cps, dict) else cps
                for cp in (items or ()):
                    if not isinstance(cp, dict) or cp.get('resolved_at') or cp.get('status') not in ('pending', 'open', None):
                        continue
                    cid = cp.get('checkpoint_id') or cp.get('id')
                    if not cid or cid in self._seen:
                        continue
                    self._seen.add(cid)
                    if self._on_cp is not None:
                        self._on_cp(cp)
                    kind = cp.get('kind') or 'tool_approval'
                    if self._approve:
                        body = {'kind': 'content_apply', 'decision': 'approve'} if kind == 'content_apply' else {'kind': kind, 'action': 'approve', 'option_id': _pick_approve_option(cp)}
                    else:
                        body = {'kind': kind, 'action': 'reject', 'guidance': 'rebound listener denied'}
                    _http_json(self._app, 'POST', f'/v1/sessions/{self._sid}/checkpoints/{cid}', body)
                    self.events.append({'checkpoint_id': cid, 'kind': kind, 'approved': self._approve})
            self._stop.wait(0.15)

    def close(self):
        self._stop.set(); self._thread.join(timeout=3)


class LiveWitnessContext:
    """Manages per-case host-side fixtures and live observations for Q13, Q14, Q15, and Q17."""
    def __init__(self, case, app, session_id, project_id, root_id, project_dir, out_dir, surface, send_calls, prelude_records):
        self.case, self.cid, self.app, self.sid = case, case['id'], app, session_id
        self.pid, self.rid, self.proj, self.out_dir = project_id, root_id, pathlib.Path(project_dir), pathlib.Path(out_dir)
        self.surface, self.send_calls, self.prelude_records = surface, send_calls, prelude_records
        self.meta, self._cleanups, self._poller, self._chmod_dir = {}, [], None, None
        self.port, self._http_srv, self._rebind_srv = None, None, None

    def prepare(self):
        if self.cid == 'stale-approval':
            doc = _open_doc(self.app, self.pid, self.rid, 'AGENTS.md')
            self.meta.update({'doc_id': doc['id'], 'doc_before_rev': doc['revision']})
            def _race(cp):
                h = _replace_doc(self.app, self.pid, doc['id'], doc['revision'], 'human\n')
                self.meta.update({'checkpoint_id': cp.get('checkpoint_id') or cp.get('id'), 'human_revision': h.get('revision')})
            self._poller = _CheckpointPoller(self.app, self.sid, approve=True, on_checkpoint=_race)
            self._poller.start()
        elif self.cid == 'publication-barrier':
            doc_in = _open_doc(self.app, self.pid, self.rid, 'dir/input.txt')
            doc_un = _open_doc(self.app, self.pid, self.rid, 'unrelated.txt')
            un_draft = _replace_doc(self.app, self.pid, doc_un['id'], doc_un['revision'], 'human draft\n')
            self.meta.update({'input_doc_before_rev': doc_in['revision'], 'unrelated_doc_before_rev': un_draft['revision']})
            sub = self.proj / 'dir'
            os.chmod(sub, 0o555)
            self._chmod_dir = sub
        elif self.cid == 'partial-write-unknown-http':
            self._http_srv = _LoopbackDropServer()
            self.port = self._http_srv.start()
            self._poller = _CheckpointPoller(self.app, self.sid, approve=True)
            self._poller.start()
        elif self.cid == 'listener-rebind':
            self.port = _pick_free_port()
            ready, srv_py = self.proj / 'owned.ready', self.proj / 'owned_srv.py'
            srv_py.write_text(
                'import http.server, pathlib\nclass H(http.server.BaseHTTPRequestHandler):\n'
                ' def do_GET(self): b=b"owned-listener\\n"; self.send_response(200); self.send_header("Content-Length",str(len(b))); self.end_headers(); self.wfile.write(b)\n'
                ' def log_message(self,*a): pass\n'
                f's=http.server.HTTPServer(("127.0.0.1",{self.port}),H); pathlib.Path("owned.ready").write_text("ok"); s.serve_forever()\n'
            )
            pol = _CheckpointPoller(self.app, self.sid, approve=True)
            pol.start()
            try:
                c_start = self.send_calls(self.app, self.sid, [{'name': 'command', 'args': {'command': 'external -- python3 ./owned_srv.py &', 'capability_request': {'local_listen': {'ports': [self.port]}}}}], surface=self.surface)
                self.prelude_records.append({'kind': 'q17_start', 'call_id': c_start[0][0], 'outcome': c_start[0][1].get('outcome')})
            finally:
                pol.close()
            self.app.wait_for(lambda: ready.is_file(), 'owned listener ready', timeout=30)
            owned_ok = False
            for _ in range(10):
                c_cur = self.send_calls(self.app, self.sid, [{'name': 'command', 'args': {'command': f'curl -fsS http://127.0.0.1:{self.port}/owned'}}], surface=self.surface)
                self.prelude_records.append({'kind': 'q17_owned_curl', 'call_id': c_cur[0][0], 'outcome': c_cur[0][1].get('outcome')})
                if 'owned-listener' in str(c_cur[0][1].get('content') or ''):
                    owned_ok = True
                    break
                time.sleep(0.5)
            c_stop = self.send_calls(self.app, self.sid, [{'name': 'command', 'args': {'command': 'kill %1; wait %1'}}], surface=self.surface)
            self.prelude_records.append({'kind': 'q17_stop', 'call_id': c_stop[0][0], 'outcome': c_stop[0][1].get('outcome')})
            for tmpf in (ready, srv_py):
                with contextlib.suppress(OSError): tmpf.unlink()
            self._rebind_srv = _RebindServer(self.port)
            for attempt in range(30):
                try:
                    self._rebind_srv.start(); break
                except OSError:
                    if attempt == 29:
                        raise
                    time.sleep(0.2)
            self.meta.update({'port': self.port, 'owned_listener_succeeded': owned_ok})
            self._poller = _CheckpointPoller(self.app, self.sid, approve=False)
            self._poller.start()

    def rewrite_batch(self, batch):
        if self.cid == 'partial-write-unknown-http' and self.port:
            cmd = batch[0]['args']['command'].replace('"$FIXTURE_HTTP"', f'http://127.0.0.1:{self.port}/commit')
            return [{'name': 'command', 'args': {'command': cmd, 'capability_request': {'loopback_connect': {'ports': [self.port]}}}}]
        if self.cid == 'listener-rebind' and self.port:
            cmd = batch[0]['args']['command'].replace('"$FIXTURE_HTTP"', f'http://127.0.0.1:{self.port}/owned')
            return [{'name': 'command', 'args': {'command': cmd}}]
        return batch

    def finish(self):
        if self._poller is not None:
            self._poller.close()
            self.meta['checkpoints'] = list(self._poller.events)
            self._poller = None
        if self.cid == 'stale-approval':
            after = _open_doc(self.app, self.pid, self.rid, 'AGENTS.md', client_id='verify-client')
            self.meta.update({'after_revision': after.get('revision'), 'after_dirty': after.get('dirty'), 'after_content': after.get('content')})
        elif self.cid == 'publication-barrier':
            after_in = _open_doc(self.app, self.pid, self.rid, 'dir/input.txt', client_id='verify-client')
            after_un = _open_doc(self.app, self.pid, self.rid, 'unrelated.txt', client_id='verify-client')
            self.meta.update({
                'input_doc_after_rev': after_in.get('revision'), 'input_doc_dirty': after_in.get('dirty'),
                'input_held_agent_version_id': after_in.get('held_agent_version_id'),
                'unrelated_doc_after_rev': after_un.get('revision'), 'unrelated_doc_dirty': after_un.get('dirty'),
            })
        elif self.cid == 'partial-write-unknown-http' and self._http_srv is not None:
            self.meta['http_requests'] = self._http_srv.snapshot()
        elif self.cid == 'listener-rebind' and self._rebind_srv is not None:
            self.meta['unrelated_hits'] = self._rebind_srv.hit_count()
        self.cleanup()
        return self.meta

    def cleanup(self):
        if self._poller is not None:
            with contextlib.suppress(Exception):
                self._poller.close()
            self._poller = None
        if self._chmod_dir is not None:
            with contextlib.suppress(OSError):
                os.chmod(self._chmod_dir, 0o755)
            self._chmod_dir = None
        if self._http_srv is not None:
            with contextlib.suppress(Exception):
                self._http_srv.close()
            self._http_srv = None
        if self._rebind_srv is not None:
            with contextlib.suppress(Exception):
                self._rebind_srv.close()
            self._rebind_srv = None
