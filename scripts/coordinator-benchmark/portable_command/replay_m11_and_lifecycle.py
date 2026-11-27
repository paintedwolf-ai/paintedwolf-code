"""Zero-model M11 (spill/limits/recovery) & M02 (malformed quote -> corrected call) replay."""
import argparse
import contextlib
import hashlib
import json
import pathlib
import sqlite3
import sys
import tempfile
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

M11_AFTER_BYTES = b'm11-recovered-ok\n'
M02_OK_BYTES = b'recovered\n'
M11_SPILL_CMD = (
    "awk 'BEGIN { for (i = 1; i <= 3000; i++) "
    'printf "m11-row-%05d-0123456789abcdefghijklmnopqrstuvwxyz\\n", i }\''
)


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
    outputs = payload.get('outputs') if isinstance(payload.get('outputs'), list) else []
    stdout_win = next((w for w in outputs if isinstance(w, dict) and w.get('stream') == 'stdout'), None)
    return {
        'tool_call_id': call_id, 'command': cmd, 'outcome': raw_parsed.get('outcome'),
        'state': payload.get('state'), 'status': payload.get('status'),
        'stdout': payload.get('stdout'), 'stdout_ref': payload.get('stdout_ref'),
        'truncated': payload.get('truncated'), 'outputs': outputs, 'stdout_window': stdout_win,
        'completed_effects': payload.get('completed_effects'),
        'rejection_code': code, 'rejection_details': details,
        'program_id': payload.get('program_id') or (raw_parsed.get('command_program') or {}).get('id'),
    }


def _receipt_state(receipt):
    if not isinstance(receipt, dict):
        return None, None
    prog = receipt.get('program') if isinstance(receipt.get('program'), dict) else receipt
    return prog.get('state'), prog.get('status_code')


def run_m11_and_m02_replay(repo_root, out_path, engine_manifest=None, engine_bin=None):
    identity = source_identity(repo_root, profile='implement', engine_bin=engine_bin, engine_manifest=engine_manifest)
    workdir = pathlib.Path(tempfile.mkdtemp(prefix='m11-m02-replay-'))
    app_dir, project_dir = workdir / 'app', workdir / 'project'
    project_dir.mkdir(parents=True, exist_ok=True)

    app = make_app_factory(engine_bin, smoke.Application)(app_dir)
    try:
        app.ready()
        sid, project_id, root_id, surface = create_isolated_session(app, project_dir, {'profile': 'implement'})
        app.request(f'/v1/sessions/{sid}/prompts', {'operation_id': str(uuid.uuid4()), 'text': 'Execute M11 spill/recovery and M02 syntax-refusal/recovery.'})
        send_tool_calls(app, sid, [{'name': 'update_progress', 'args': {'content': '- [ ] Run M11 spill and M02 malformed-quote recovery'}}], surface=surface)

        # M11: large stdout (~150 KiB > 64 KiB inline budget) spills to @output/<program_id>.stdout.
        m11_spill = _run_cmd(app, sid, surface, M11_SPILL_CMD)
        win = m11_spill['stdout_window'] or {}
        spill_path = win.get('output_path') or ''
        m11_read_spill = _run_cmd(app, sid, surface, f"head -n 2 '{spill_path}'") if spill_path else None
        m11_after = _run_cmd(app, sid, surface, "printf 'm11-recovered-ok\\n' > ./m11_after.txt && cat ./m11_after.txt")

        # M02: malformed-quote refusal (zero side effects) -> immediate corrected call in same session.
        m02_bad = _run_cmd(app, sid, surface, "printf 'unterminated")
        m02_pre_exists = (project_dir / 'm02_ok.txt').exists()
        m02_good = _run_cmd(app, sid, surface, "printf 'recovered\\n' > ./m02_ok.txt")

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
        app.close(cleanup=False)

    m11_win = m11_spill['stdout_window'] or {}
    m11_after_sha = sha256_file(project_dir / 'm11_after.txt')
    m02_ok_sha = sha256_file(project_dir / 'm02_ok.txt')
    m02_r_state, m02_r_status = _receipt_state(program_receipts.get(m02_good['program_id']))

    checks = {
        'm11_spill_recorded': (
            m11_spill['status'] == 0
            and m11_spill['truncated'] is True
            and bool(m11_spill['stdout_ref'])
            and bool(m11_win.get('output_path'))
            and (m11_win.get('omitted_bytes') or 0) > 0
            and (m11_win.get('total_bytes') or 0) > 65536
        ),
        'm11_spill_readable': (
            m11_read_spill is not None
            and m11_read_spill['status'] == 0
            and 'm11-row-00001-' in (m11_read_spill.get('stdout') or '')
        ),
        'm11_subsequent_run_recovers': (
            m11_after['status'] == 0
            and not m11_after['truncated']
            and m11_after['stdout'] == M11_AFTER_BYTES.decode('utf-8')
            and m11_after_sha == sha256_bytes(M11_AFTER_BYTES)
        ),
        'm02_malformed_quote_refused': (
            m02_bad['outcome'] == 'rejected'
            and m02_bad['rejection_code'] == 'COMMAND_SYNTAX_INVALID'
            and isinstance(m02_bad['rejection_details'], dict)
            and bool(m02_bad['rejection_details'])
            and not m02_pre_exists
        ),
        'm02_corrected_call_succeeds': (
            m02_good['status'] == 0
            and bool(m02_good['program_id'])
            and m02_ok_sha == sha256_bytes(M02_OK_BYTES)
            and m02_r_state == 'completed' and m02_r_status == 0
        ),
        'provenance_verified': (identity.get('engine_provenance') or {}).get('provenance_verified') is True,
    }
    report = {
        'case_id': 'M11-spill-recovery-and-M02-quote-recovery', 'model_calls': 0,
        'passed': all(checks.values()), 'checks': checks,
        'identity': identity, 'provenance_verified': checks['provenance_verified'],
        'session': {'id': sid, 'project_id': project_id, 'root_id': root_id, 'surface': surface},
        'm11': {
            'spill': m11_spill, 'read_spill': m11_read_spill, 'after': m11_after,
            'after_sha256': m11_after_sha,
        },
        'm02': {
            'malformed': m02_bad, 'corrected': m02_good,
            'ok_sha256': m02_ok_sha, 'expected_ok_sha256': sha256_bytes(M02_OK_BYTES),
        },
        'invocations': invocations, 'program_receipts': program_receipts,
    }
    out_file = pathlib.Path(out_path)
    out_file.parent.mkdir(parents=True, exist_ok=True)
    out_file.write_text(json.dumps(report, indent=2) + '\n')
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description='Zero-model M11 spill/recovery and M02 quote-refusal/recovery replay.')
    parser.add_argument('--engine-manifest', type=pathlib.Path, help='Build-time provenance manifest JSON')
    parser.add_argument('--engine', type=pathlib.Path, help='Explicit engine binary path')
    parser.add_argument('--out', type=pathlib.Path, required=True, help='Output JSON report path')
    parser.add_argument('--repo-root', type=pathlib.Path, default=ROOT, help='Repository root path')
    args = parser.parse_args(argv)
    report = run_m11_and_m02_replay(args.repo_root, args.out, engine_manifest=args.engine_manifest, engine_bin=args.engine)
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
