"""Zero-model BB05 lifecycle witness: Q11 temp handoff, Q16 owned bg state, Q17 unowned listener refusal."""
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


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def sha256_file(path):
    p = pathlib.Path(path)
    return sha256_bytes(p.read_bytes()) if p.is_file() else None


class UnrelatedListener:
    """Unowned loopback listener used to verify Q17 refusal with zero requests sent."""

    def __init__(self):
        self._lock = threading.Lock()
        self.hits = []
        self._server, self._thread = None, None

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

        self._server = _Server(('127.0.0.1', 0), Handler)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()
        return self._server.server_address[1]

    def hit_count(self):
        with self._lock:
            return len(self.hits)

    def close(self):
        if self._server is not None:
            self._server.shutdown()
            self._server.server_close()
        if self._thread is not None:
            self._thread.join(timeout=3)


class RejectPoller:
    """Reject any pending checkpoint during negative-authority probes so turns never hang."""

    def __init__(self, app, sid):
        self._app, self._sid = app, sid
        self._stop, self._seen = threading.Event(), set()
        self.rejected = []
        self._thread = threading.Thread(target=self._run, daemon=True)

    def start(self):
        self._thread.start()

    def _run(self):
        while not self._stop.is_set():
            with contextlib.suppress(Exception):
                data = self._app.request(f'/v1/sessions/{self._sid}/checkpoints?status=pending') or {}
                for cp in data.get('checkpoints') or []:
                    cid = cp.get('id')
                    if cid and cid not in self._seen:
                        self._seen.add(cid)
                        self._app.request(
                            f'/v1/sessions/{self._sid}/checkpoints/{cid}',
                            {'kind': 'tool_approval', 'action': 'reject', 'guidance': 'unowned listener denied'},
                        )
                        self.rejected.append(cid)
            self._stop.wait(0.15)

    def close(self):
        self._stop.set()
        self._thread.join(timeout=3)


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


def _receipt_state(receipt):
    if not isinstance(receipt, dict):
        return None, None
    prog = receipt.get('program') if isinstance(receipt.get('program'), dict) else receipt
    return prog.get('state'), prog.get('status_code')


def run_bb05_lifecycle_witness(repo_root, out_path, engine_manifest=None, engine_bin=None):
    identity = source_identity(repo_root, profile='implement', engine_bin=engine_bin, engine_manifest=engine_manifest)
    listener = UnrelatedListener()
    unrelated_port = listener.start()
    workdir = pathlib.Path(tempfile.mkdtemp(prefix='bb05-lifecycle-witness-'))
    app_dir, project_dir = workdir / 'app', workdir / 'project'
    project_dir.mkdir(parents=True, exist_ok=True)

    app = make_app_factory(engine_bin, smoke.Application)(app_dir)
    reject_poller = None
    try:
        app.ready()
        sid, project_id, root_id, surface = create_isolated_session(app, project_dir, {'profile': 'implement'})
        app.request(f'/v1/sessions/{sid}/prompts', {'operation_id': str(uuid.uuid4()), 'text': 'Execute BB05 lifecycle witnesses (Q11/Q16/Q17).'})
        send_tool_calls(app, sid, [{'name': 'update_progress', 'args': {'content': '- [ ] Run BB05 lifecycle witnesses'}}], surface=surface)

        # 1. Q11 temp-native-process-handoff: native mktemp + write -> external process readback.
        q11_handoff = _run_cmd(app, sid, surface, 'p=$(mktemp); printf \'same\\n\' > "$p"; external -- cat "$p"')

        # 2. Q16 owned-background-state: captured env isolation + live session job inspect & stop.
        q16_env = _run_cmd(
            app, sid, surface,
            "export FIXTURE=before; (printf '%s\\n' \"$FIXTURE\" > ./bg_env.txt) & job=$!; export FIXTURE=after; wait \"$job\"; cat ./bg_env.txt",
        )
        q16_start = _run_cmd(app, sid, surface, 'sleep 60 & jobs')
        q16_stop = _run_cmd(app, sid, surface, 'kill %1; wait %1; jobs')

        # 3. Q17 listener-rebind (unowned loopback listener refused with zero requests sent).
        reject_poller = RejectPoller(app, sid)
        reject_poller.start()
        q17_rebind = _run_cmd(app, sid, surface, f'curl -fsS http://127.0.0.1:{unrelated_port}/rebind')
        reject_poller.close()
        reject_poller = None

        app.request(f'/v1/sessions/{sid}/abort', {})
        invocations = app.request(f'/v1/sessions/{sid}/invocations')
        with contextlib.closing(sqlite3.connect(app_dir / 'store.db')) as db:
            pids = [r[0] for r in db.execute(
                'SELECT p.id FROM invocation_programs p JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE r.session_id=?',
                (sid,),
            ).fetchall()]
        program_receipts = {
            pid: app.request(f'/v1/sessions/{sid}/program-receipts/{pid}?limit=50', missing=True)
            for pid in pids
        }
    finally:
        if reject_poller is not None:
            reject_poller.close()
        app.close(cleanup=False)
        listener.close()

    q11_state, q11_status = _receipt_state(program_receipts.get(q11_handoff['program_id']))
    q16_env_sha = sha256_file(project_dir / 'bg_env.txt')
    q17_hits = listener.hit_count()

    checks = {
        'q11_temp_native_process_handoff': (
            q11_handoff['status'] == 0
            and q11_handoff['stdout'] == 'same\n'
            and q11_state == 'completed' and q11_status == 0
        ),
        'q16_owned_background_env_isolation': (
            q16_env['status'] == 0
            and q16_env['stdout'] == 'before\n'
            and q16_env_sha == sha256_bytes(b'before\n')
        ),
        'q16_owned_background_live_and_stop': (
            q16_start['status'] == 0
            and '[1]' in (q16_start['stdout'] or '')
            and q16_stop['status'] == 0
            and '[1]' not in (q16_stop['stdout'] or '')
        ),
        'q17_unowned_listener_refused_zero_hits': (
            (q17_rebind['outcome'] == 'rejected' or q17_rebind['status'] != 0 or bool(q17_rebind['rejection_code']))
            and q17_hits == 0
        ),
        'provenance_verified': (identity.get('engine_provenance') or {}).get('provenance_verified') is True,
    }
    report = {
        'case_id': 'BB05-lifecycle-q11-q16-q17-witness', 'model_calls': 0,
        'passed': all(checks.values()), 'checks': checks,
        'identity': identity, 'provenance_verified': checks['provenance_verified'],
        'session': {'id': sid, 'project_id': project_id, 'root_id': root_id, 'surface': surface},
        'q11': {'handoff': q11_handoff},
        'q16': {'env_isolation': q16_env, 'bg_env_sha256': q16_env_sha, 'start_and_jobs': q16_start, 'stop_and_jobs': q16_stop},
        'q17': {'unrelated_port': unrelated_port, 'hit_count': q17_hits, 'curl_result': q17_rebind},
        'invocations': invocations, 'program_receipts': program_receipts,
    }
    out_file = pathlib.Path(out_path)
    out_file.parent.mkdir(parents=True, exist_ok=True)
    out_file.write_text(json.dumps(report, indent=2) + '\n')
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description='Zero-model BB05 lifecycle witness (Q11/Q16/Q17).')
    parser.add_argument('--engine-manifest', type=pathlib.Path, help='Build-time provenance manifest JSON')
    parser.add_argument('--engine', type=pathlib.Path, help='Explicit engine binary path')
    parser.add_argument('--out', type=pathlib.Path, required=True, help='Output JSON report path')
    parser.add_argument('--repo-root', type=pathlib.Path, default=ROOT, help='Repository root path')
    args = parser.parse_args(argv)
    report = run_bb05_lifecycle_witness(args.repo_root, args.out, engine_manifest=args.engine_manifest, engine_bin=args.engine)
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
