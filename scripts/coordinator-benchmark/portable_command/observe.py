"""Independent observation and audit builder for native Portable Command runs."""
import contextlib
import hashlib
import json
import pathlib
import platform
import sqlite3
import subprocess
from portable_command.artifacts import project_hashes
from portable_command.contracts import DIRECTORY, ROOT, digest

ALLOWED_STOPS = {None, 'rejected', 'conflict', 'cancelled', 'incomplete', 'budget', 'interrupted'}


def file_sha256(path):
    return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()


def source_identity(root=ROOT, profile=None):
    """Capture immutable source/build/host identity for an attempt."""
    def git(*args):
        proc = subprocess.run(['git', '-C', str(root), *args], capture_output=True, text=True, check=False)
        return proc.stdout.strip() if proc.returncode == 0 else ''
    version_file = root / 'VERSION'
    lang_file = root / 'lycaon/config/packs/painted-wolf/platform/tools/command-language-v1/language.json'
    return {
        'git_head': git('rev-parse', 'HEAD'),
        'git_tree': git('rev-parse', 'HEAD^{tree}'),
        'application_version': version_file.read_text().strip() if version_file.is_file() else '',
        'catalog_sha256': file_sha256(lang_file) if lang_file.is_file() else '',
        'suite_sha256': file_sha256(DIRECTORY / 'suite.json'),
        'platform': f'{platform.system().lower()}-{platform.machine().lower()}',
        'profile': profile or '',
    }


def query_db_audit(db_path, session_id, call_ids):
    """Read authoritative SQLite rows for the tested session and tool calls."""
    uri = pathlib.Path(db_path).resolve().as_uri() + '?mode=ro'
    with contextlib.closing(sqlite3.connect(uri, uri=True)) as db:
        db.row_factory = sqlite3.Row
        placeholders = ','.join('?' for _ in call_ids) or "''"
        params = [session_id, *call_ids] if call_ids else [session_id]
        receipts = [dict(r) for r in db.execute(
            f'SELECT * FROM invocation_receipts WHERE session_id=? AND tool_call_id IN ({placeholders}) ORDER BY rowid',
            params,
        ).fetchall()]
        receipt_ids = [r['id'] for r in receipts]
        r_ph = ','.join('?' for _ in receipt_ids) or "''"
        programs = [dict(r) for r in db.execute(
            f'SELECT * FROM invocation_programs WHERE parent_receipt_id IN ({r_ph}) ORDER BY rowid',
            receipt_ids,
        ).fetchall()] if receipt_ids else []
        prog_ids = [p['id'] for p in programs]
        p_ph = ','.join('?' for _ in prog_ids) or "''"
        executions = [dict(r) for r in db.execute(
            f'SELECT * FROM invocation_program_executions WHERE program_id IN ({p_ph}) ORDER BY program_id, ordinal',
            prog_ids,
        ).fetchall()] if prog_ids else []
        pins = [dict(r) for r in db.execute(
            f'SELECT * FROM invocation_program_revision_pins WHERE program_id IN ({p_ph}) ORDER BY program_id, execution_id, ordinal',
            prog_ids,
        ).fetchall()] if prog_ids else []
        outputs = [dict(r) for r in db.execute(
            f'SELECT id,program_id,execution_id,stream,reference,complete,redacted,preview,preview_truncated,line_count,inline_digest,blob_id FROM invocation_program_outputs WHERE program_id IN ({p_ph}) ORDER BY rowid',
            prog_ids,
        ).fetchall()] if prog_ids else []
        publications = [dict(r) for r in db.execute(
            f'SELECT * FROM invocation_program_publications WHERE program_id IN ({p_ph})',
            prog_ids,
        ).fetchall()] if prog_ids else []
        windows = [dict(r) for r in db.execute(
            f'SELECT id,session_id,tool_call_id,state FROM source_command_windows WHERE session_id=? AND tool_call_id IN ({placeholders})',
            params,
        ).fetchall()] if call_ids else []
        messages = [dict(r) for r in db.execute(
            f'SELECT id,ord,role,tool_result_call_id,tool_result_json FROM messages WHERE session_id=? AND tool_result_call_id IN ({placeholders}) ORDER BY ord',
            params,
        ).fetchall()] if call_ids else []
    return {
        'invocation_receipts': receipts,
        'invocation_programs': programs,
        'invocation_program_executions': executions,
        'invocation_program_revision_pins': pins,
        'invocation_program_outputs': outputs,
        'invocation_program_publications': publications,
        'source_command_windows': windows,
        'tool_result_messages': messages,
    }


def scan_sidecar_launches(log_path, session_id):
    """Count sandbox process-boundary selections emitted for this session's command tool."""
    path = pathlib.Path(log_path)
    if not path.is_file():
        return {'available': False, 'command_launches': 0, 'lines': []}
    matches = []
    for lineno, line in enumerate(path.read_text(errors='replace').splitlines(), 1):
        if 'process boundary selected' in line and session_id in line and 'tool=command' in line:
            matches.append({'line': lineno, 'excerpt': line[:240]})
    return {'available': True, 'command_launches': len(matches), 'lines': matches}


def parse_tool_result(message_row):
    raw = json.loads(message_row['tool_result_json'])
    body = raw.get('content') if raw.get('content') is not None else raw.get('result')
    if isinstance(body, str):
        text = body.strip()
        if text.startswith('[') and '\n' in text:
            text = text.split('\n', 1)[1].strip()
        if text.startswith('{'):
            try:
                return raw, json.loads(text)
            except json.JSONDecodeError:
                return raw, {}
        return raw, {}
    return raw, body if isinstance(body, dict) else {}


def normalize_outcome(raw_result, program_row, payload):
    """Derive terminal outcome and host_stop strictly from host records."""
    raw_outcome = raw_result.get('outcome')
    rejection = raw_result.get('rejection') or {}
    refusal = payload.get('refusal') if isinstance(payload, dict) else None
    code = rejection.get('code') or (refusal.get('code') if isinstance(refusal, dict) else None) or (program_row or {}).get('stop_code') or None
    if raw_outcome == 'rejected' or (program_row and program_row.get('state') == 'rejected') or (isinstance(refusal, dict) and refusal.get('code')):
        return 'rejected', 'rejected', code
    state = (program_row or {}).get('state') or raw_outcome or 'failed'
    stop = None if state == 'completed' else (state if state in ALLOWED_STOPS else 'incomplete')
    return state, stop, code


def _receipt_rows(audit, progress_receipts):
    receipts = []
    effects = []
    for pr in progress_receipts:
        row = {'execution_id': pr['id'], 'operation': 'update_progress', 'outcome': pr['status']}
        receipts.append(row)
        effects.append({'operation': 'update_progress', 'outcome': pr['status']})
    for ex in audit['invocation_program_executions']:
        op = 'write' if ex.get('operation_id') == 'write' else (ex.get('native_command') or ex.get('operation_id'))
        outcome = ex['state']
        row = {'execution_id': ex['id'], 'operation': op, 'outcome': outcome}
        effect = {'operation': op, 'outcome': outcome}
        if ex.get('operation_id') == 'write' or ex.get('accepted_revision') or ex.get('published_revision'):
            pin = next((p for p in audit['invocation_program_revision_pins']
                        if p['execution_id'] == ex['id'] and p['role'] == 'destination'), None)
            if pin:
                row['path'] = pin['path']
                effect['path'] = pin['path']
            row['accepted'] = bool(ex.get('accepted_revision') or outcome == 'completed')
            row['published'] = bool(ex.get('published_revision') or outcome == 'completed')
            effect['accepted'] = row['accepted']
            effect['published'] = row['published']
        receipts.append(row)
        if ex.get('effect_certainty') in {'completed', 'attempted', 'unknown'} or ex.get('operation_id') == 'write':
            effects.append(effect)
    return receipts, effects


def _extract_recovery(case_id, payload, raw_result, audit):
    refusal = payload.get('refusal') if isinstance(payload, dict) else None
    rejection = raw_result.get('rejection') or {}
    code = (refusal or {}).get('code') or rejection.get('code')
    if not code:
        return None
    details = (refusal or {}).get('details') or rejection.get('details') or {}
    feedback = (refusal or {}).get('feedback') or rejection.get('message') or ''
    has_span = any(k in details for k in ('span', 'source_span', 'start_byte', 'word', 'option')) or (
        isinstance(details.get('error'), str) and ':' in details.get('error', '')
    )
    has_help = bool(details.get('expected_form') or details.get('usage') or details.get('help') or ('usage' in feedback.lower()) or ('supported' in feedback.lower()))
    cause = 'option' if code == 'COMMAND_OPTION_UNSUPPORTED' else code.lower()
    return {
        'case_id': case_id,
        'cause': cause,
        'zero_program_effects': (payload.get('completed_effects', 0) == 0 and payload.get('uncertain_effects', 0) == 0 and not audit['invocation_program_executions']),
        'whole_program_resubmit_allowed': bool(payload.get('replay_available', True)),
        'automatic_execution': False,
        'remedy': 'focused_command_help' if has_help else '',
        'effect_certainty_required': True,
        'source_span_required': has_span,
        'owner_code': code,
    }


def build_observation(case, *, session_id, identity, project_dir, db_path, sidecar_log, turn_records, prelude_records):
    """Construct a version-1 native-runtime observation from authoritative host records."""
    tested_calls = [cid for rec in turn_records for cid in rec['call_ids']]
    audit = query_db_audit(db_path, session_id, tested_calls)
    sidecar = scan_sidecar_launches(sidecar_log, session_id)
    msgs_by_call = {m['tool_result_call_id']: parse_tool_result(m) for m in audit['tool_result_messages']}
    progs_by_receipt = {p['parent_receipt_id']: p for p in audit['invocation_programs']}
    receipts_by_call = {r['tool_call_id']: r for r in audit['invocation_receipts']}

    progress_receipts = [r for r in audit['invocation_receipts'] if r['tool_name'] == 'update_progress']
    cmd_invocations = []
    last_payload, last_raw, last_prog = {}, {}, None
    for rec in turn_records:
        cmd_call = rec['command_call_id']
        raw_res, payload = msgs_by_call.get(cmd_call, ({}, {}))
        inv_row = receipts_by_call.get(cmd_call, {})
        prog_row = progs_by_receipt.get(inv_row.get('id'))
        outcome, host_stop, code = normalize_outcome(raw_res, prog_row, payload)
        inv_id = (prog_row or {}).get('id') or inv_row.get('id') or cmd_call
        cmd_invocations.append({'id': inv_id, 'program': rec['program'], 'outcome': outcome, 'rejection_code': code})
        last_payload, last_raw, last_prog = payload, raw_res, prog_row

    outcome, host_stop, _ = normalize_outcome(last_raw, last_prog, last_payload)
    status_code = last_payload.get('status') if isinstance(last_payload.get('status'), int) else (
        last_prog['status_code'] if last_prog and last_prog.get('status_code') is not None else (2 if outcome == 'rejected' else 0)
    )
    complete = (outcome == 'completed' and host_stop is None and (last_prog is None or last_prog.get('state') == 'completed'))
    receipts, effects = _receipt_rows(audit, progress_receipts)
    recovery = _extract_recovery(case['id'], last_payload, last_raw, audit)

    external_execs = [e for e in audit['invocation_program_executions']
                      if e.get('owner') == 'external' or e.get('native_gap_json')]
    no_external_launch = (
        sidecar['available']
        and sidecar['command_launches'] == 0
        and len(audit['source_command_windows']) == 0
        and len(external_execs) == 0
        and last_payload.get('source_window') is None
        and last_payload.get('native_gaps', 0) == 0
    )
    child_ids = [r['execution_id'] for r in receipts]
    facts = {
        'progress_and_program_separate': bool(progress_receipts and audit['invocation_programs']
                                              and all(pr['id'] != p['parent_receipt_id'] for pr in progress_receipts for p in audit['invocation_programs'])),
        'absence_pin': any(p['mode'] == 'absent' for p in audit['invocation_program_revision_pins']),
        'child_identities': bool(child_ids) and len(set(child_ids)) == len(child_ids),
        'no_external_launch': no_external_launch,
        'option_source_span': bool(recovery and recovery['source_span_required']),
        'focused_help': bool(recovery and recovery['remedy'] == 'focused_command_help'),
    }
    observation = {
        'version': 1,
        'case_id': case['id'],
        'capture_kind': 'native-runtime',
        'identity': identity,
        'program': {
            'stdout': last_payload.get('stdout', ''),
            'stderr': last_payload.get('stderr', ''),
            'status': status_code,
            'host_stop': host_stop,
            'complete': complete,
        },
        'receipts': receipts,
        'effects': effects,
        'facts': facts,
        'deliveries': [],
        'obligations': [],
        'closeout': 'complete' if outcome == 'completed' else 'blocked',
        'forbidden_replay': False,
        'recovery': recovery,
        'invocations': cmd_invocations,
    }
    audit_bundle = {
        'session_id': session_id,
        'prelude': prelude_records,
        'external_launch_audit': {
            'sidecar': sidecar,
            'source_command_windows': len(audit['source_command_windows']),
            'external_executions': len(external_execs),
            'source_window_field': last_payload.get('source_window'),
            'native_gaps': last_payload.get('native_gaps', 0),
        },
        'filesystem_hashes': project_hashes(project_dir),
        'db': audit,
    }
    return observation, audit_bundle
