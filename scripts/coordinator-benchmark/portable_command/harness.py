"""Zero-model real-application runner and second-process replay for Portable Command cases."""
import contextlib
import hashlib
import json
import pathlib
import sqlite3
import threading
import urllib.parse
import urllib.request
import uuid
import smoke
import smoke_workers
from bootstrap_policy import open_request
from capture_storage import checkpoint_closed_store
from portable_command.artifacts import project_hashes
from portable_command.case_oracles import materialize_git_setup, materialize_host_facts
from portable_command.contracts import DIRECTORY, ROOT, load_suite, materialize
from portable_command.grading import grade_case
from portable_command.handle_await import await_running_handle
from portable_command.observe import build_observation, file_sha256, source_identity
from portable_command.profile_session import DECLARED_READONLY_PROFILES, create_declared_profile_session
from portable_command.provenance import materialize_denied_progress_scope, materialize_helpers
from portable_command.reporting import inject_drafts, make_app_factory
from progress import save


class SSECollector:
    """Capture live /v1/events SSE frames into a bounded JSONL file."""
    def __init__(self, url, token, out_path):
        self.url = url + '/v1/events'
        self.token = token
        self.out_path = pathlib.Path(out_path)
        self._stop = threading.Event()
        self._response = None
        self._thread = threading.Thread(target=self._run, daemon=True)
        self.count = 0

    def start(self):
        self._thread.start()

    def _run(self):
        req = urllib.request.Request(self.url, headers={'Authorization': 'Bearer ' + self.token, 'Accept': 'text/event-stream'})
        try:
            with open_request(req) as resp:
                self._response = resp
                with self.out_path.open('w') as sink:
                    for raw in resp:
                        if self._stop.is_set():
                            break
                        line = raw.decode('utf-8', errors='replace').rstrip('\r\n')
                        if line.startswith('data:'):
                            sink.write(line[5:].strip() + '\n')
                            sink.flush()
                            self.count += 1
        except (OSError, AttributeError, ValueError):
            pass

    def close(self):
        self._stop.set()
        if self._response is not None:
            with contextlib.suppress(OSError, AttributeError):
                self._response.close()
        self._thread.join(timeout=3)


def wait_tool_result(application, session, call_id):
    """Wait for any terminal tool_result_json row (including typed refusals)."""
    def observe():
        with contextlib.closing(sqlite3.connect(application.directory / 'store.db')) as db:
            row = db.execute(
                'SELECT tool_result_json FROM messages WHERE session_id=? AND tool_result_call_id=?',
                (session, call_id),
            ).fetchone()
        return json.loads(row[0]) if row else None
    return application.wait_for(observe, 'portable-command tool result', session)


def send_tool_calls(application, session, calls, *, surface=None):
    """Answer the next manual model turn with exact scripted calls, allowing refusals."""
    req = application.wait_for(lambda: smoke_workers.pending(application, session), 'portable-command model turn', session)
    names = [c['name'] for c in calls]
    missing = sorted({n for n in names if n not in req['tools']})
    if missing and 'request_tools' in req['tools']:
        load_id = str(uuid.uuid4())
        application.request('/harness/llm/respond', {'id': req['id'], 'tool_calls': [{'id': load_id, 'name': 'request_tools', 'args': {'need': ', '.join(missing)}}]})
        wait_tool_result(application, session, load_id)
        req = application.wait_for(lambda: smoke_workers.pending(application, session), 'loaded portable-command tools', session)
    if isinstance(surface, dict):
        surface['offered_tools'] = sorted(set(surface.get('offered_tools') or ()) | set(req.get('tools') or ()))
    ids = [str(uuid.uuid4()) for _ in calls]
    application.request('/harness/llm/respond', {'id': req['id'], 'tool_calls': [{'id': i, **c} for i, c in zip(ids, calls)]})
    return [(i, wait_tool_result(application, session, i)) for i in ids]


def log_line_count(path):
    p = pathlib.Path(path)
    return len(p.read_text(errors='replace').splitlines()) if p.is_file() else 0


def session_posture(profile):
    return 'build' if profile in {None, '', 'implement'} else 'vet'


def create_isolated_session(application, project_dir, case):
    attached = application.request('/v1/projects', {'roots': [{'path': str(project_dir)}]})
    roots = attached.get('roots') or []
    root_id = roots[0]['id'] if roots and isinstance(roots[0], dict) and roots[0].get('id') else ''
    posture = session_posture(case.get('profile'))
    session = application.request('/v1/sessions', {'project_id': attached['id'], 'posture': posture})
    sid = session['id']
    turn_budget = max(20, 4 + len(case.get('turns') or ()) * 16)
    application.request('/harness/model-limit', {'session_id': sid, 'limit': turn_budget})
    observed_state = {}
    def ready():
        state = application.request('/v1/sessions/' + sid)
        if state['status'] == 'preparing':
            return False
        if state['status'] != 'idle':
            raise RuntimeError('session preparation ended in ' + state['status'])
        observed_state.update(state)
        return True
    application.wait_for(ready, 'portable-command session ready', sid)
    surface = {
        'requested_profile': case.get('profile', 'implement'), 'requested_posture': posture,
        'effective_posture': observed_state.get('posture') or session.get('posture') or '', 'offered_tools': [], 'session_turn_budget': turn_budget,
    }
    return sid, attached['id'], root_id, surface


def collect_raw_api(application, session_id, raw_dir, program_ids):
    save(raw_dir / 'invocations.json', application.request(f'/v1/sessions/{session_id}/invocations'))
    save(raw_dir / 'messages.json', application.request(f'/v1/sessions/{session_id}/messages'))
    prog_receipts = {}
    receipt_docs = {}
    for pid in program_ids:
        prog_receipts[pid] = application.request(f'/v1/sessions/{session_id}/program-receipts/{pid}?limit=50', missing=True)
        q = urllib.parse.urlencode({'scopes': pid, 'offset': 0, 'limit': 64})
        receipt_docs[pid] = application.request(f'/v1/sessions/{session_id}/program-receipt-document?{q}', missing=True)
    save(raw_dir / 'program-receipts.json', prog_receipts)
    save(raw_dir / 'receipt-documents.json', receipt_docs)
    return prog_receipts


def case_digest(case):
    return hashlib.sha256(json.dumps(case, sort_keys=True).encode('utf-8')).hexdigest()


def sanitized_fixture(case, baseline_hashes):
    """Build a pinned, sanitized replay fixture free of session IDs, tokens, and host paths."""
    return {
        'schema': 'portable-command-replay-v1', 'version': 1, 'case_id': case['id'],
        'suite_sha256': file_sha256(DIRECTORY / 'suite.json'), 'case_sha256': case_digest(case),
        'profile': case['profile'], 'qualification_cases': list(case['qualification_cases']),
        'setup': case['setup'], 'turns': case['turns'], 'expected': case['expected'],
        'allowed_partial': case['allowed_partial'], 'required_evidence': case['required_evidence'],
        'baseline_hashes': baseline_hashes,
    }


def export_bundle(export_dir, case, observation, grade, audit_bundle, baseline_hashes, raw_dir):
    export_dir = pathlib.Path(export_dir)
    export_dir.mkdir(parents=True, exist_ok=True)
    save(export_dir / 'sanitized-fixture.json', sanitized_fixture(case, baseline_hashes))
    save(export_dir / 'observation.json', observation)
    save(export_dir / 'grade.json', grade)
    save(export_dir / 'audit.json', audit_bundle)
    raw_out = export_dir / 'raw'
    raw_out.mkdir(exist_ok=True)
    for name in ('invocations.json', 'messages.json', 'program-receipts.json', 'receipt-documents.json', 'events.jsonl', 'prelude.json', 'window.json'):
        src = pathlib.Path(raw_dir) / name
        if src.is_file():
            (raw_out / name).write_bytes(src.read_bytes())


def resolve_case(case_or_path):
    """Resolve a frozen case ID or validate a sanitized replay bundle against the pinned catalog."""
    suite = load_suite()
    cases = {c['id']: c for c in suite['cases']}
    candidate = pathlib.Path(case_or_path)
    if candidate.is_file():
        bundle = json.loads(candidate.read_text())
        if bundle.get('schema') != 'portable-command-replay-v1' or bundle.get('version') != 1:
            raise ValueError('unsupported replay bundle schema/version')
        cid = bundle.get('case_id')
        if cid not in cases:
            raise ValueError('replay bundle references unknown case: ' + str(cid))
        pinned = cases[cid]
        if bundle.get('suite_sha256') != file_sha256(DIRECTORY / 'suite.json') or bundle.get('case_sha256') != case_digest(pinned):
            raise ValueError('replay bundle identity does not match pinned catalog')
        for field in ('profile', 'setup', 'turns', 'expected', 'allowed_partial', 'required_evidence'):
            if bundle.get(field) != pinned.get(field):
                raise ValueError('replay bundle modified pinned field: ' + field)
        baseline = bundle.get('baseline_hashes')
        if not isinstance(baseline, dict) or any(not isinstance(k, str) or not isinstance(v, str) for k, v in baseline.items()):
            raise ValueError('replay bundle requires valid baseline_hashes map')
        return {**pinned, '_expected_baseline_hashes': baseline}
    if case_or_path not in cases:
        raise ValueError('unknown portable-command case: ' + str(case_or_path))
    return cases[case_or_path]


def run_case(case, out_dir, *, export_dir=None, engine_bin=None, engine_manifest=None, app_factory=smoke.Application, sse_factory=SSECollector):
    """Execute one Portable Command case through an isolated zero-model application."""
    out_dir = pathlib.Path(out_dir).resolve()
    app_dir, project_dir, raw_dir = out_dir / 'app', out_dir / 'project', out_dir / 'raw'
    raw_dir.mkdir(parents=True, exist_ok=True)
    baseline_hashes = materialize(case, project_dir)
    host_facts_ok, _ = materialize_host_facts(case['setup'].get('host_facts'), project_dir)
    git_setup = materialize_git_setup(case['setup'].get('git'), project_dir, case['setup'].get('files'))
    expected_baseline = case.get('_expected_baseline_hashes')
    if expected_baseline is not None and expected_baseline != baseline_hashes:
        raise ValueError('replay bundle baseline_hashes do not match materialized fixture')
    revision_aliases = {alias: baseline_hashes.get(path) for path, alias in case['setup'].get('revisions', {}).items()}
    identity = source_identity(ROOT, profile=case['profile'], engine_bin=engine_bin, engine_manifest=engine_manifest)
    save(out_dir / 'attempt.json', {'status': 'running', 'case_id': case['id'], 'identity': identity, 'revision_aliases': revision_aliases})
    helpers_dir = materialize_helpers(case['setup'].get('helpers'), out_dir / 'helpers', case_id=case['id'])
    config_root = out_dir / 'config-root'
    host_binding = materialize_denied_progress_scope(case, config_root) if app_factory is smoke.Application else None
    application = make_app_factory(engine_bin, app_factory, helpers_dir=helpers_dir, config_root=config_root if host_binding else None)(app_dir)
    sidecar_path = app_dir / 'sidecar.log'
    sse, prelude_records, turn_records, draft_bindings = None, [], [], {}
    drafts_ok, session_id, pre_prompt_lines, post_tool_lines = True, None, None, None
    try:
        application.ready()
        sse = sse_factory(application.url, application.token, raw_dir / 'events.jsonl')
        sse.start()
        session_id, project_id, root_id, session_surface = (
            create_declared_profile_session(application, project_dir, case, create_base_session=create_isolated_session)
            if case.get('profile') in DECLARED_READONLY_PROFILES and app_factory is smoke.Application
            else create_isolated_session(application, project_dir, case)
        )
        if case['setup'].get('drafts'):
            drafts_ok, draft_bindings = inject_drafts(application, project_id, root_id, case['setup']['drafts'])
        pre_prompt_lines = log_line_count(sidecar_path)
        if not session_surface.get('declared_profile_bound'):
            application.request(f'/v1/sessions/{session_id}/prompts', {'operation_id': str(uuid.uuid4()), 'text': f"Execute portable-command fixture {case['id']}."})
            if case['setup'].get('progress') == 'valid':
                p_calls = send_tool_calls(application, session_id, [{'name': 'update_progress', 'args': {'content': f"- [ ] Run fixture {case['id']}"}}], surface=session_surface)
                prelude_records.append({'kind': 'valid_progress', 'call_id': p_calls[0][0], 'outcome': p_calls[0][1].get('outcome')})
        for turn in case['turns']:
            if 'structured_tool' in turn:
                batch, envelope_mode = [{'name': turn['structured_tool'], 'args': dict(turn.get('arguments') or {})}], 'structured_tool'
            elif 'progress_update' in turn:
                batch = [{'name': 'command', 'args': {'command': turn['program'], 'progress_update': turn['progress_update']}}]
                envelope_mode = 'native_command_envelope'
            else:
                batch, envelope_mode = [{'name': 'command', 'args': {'command': turn['program']}}], 'command_only'
            results = send_tool_calls(application, session_id, batch, surface=session_surface)
            call_ids = [cid for cid, _ in results]
            await_meta = await_running_handle(application, session_id, results[-1][1], send_calls=lambda a, s, c: send_tool_calls(a, s, c, surface=session_surface))
            await_ids = (await_meta['wait_call_ids'] + await_meta['output_call_ids']) if await_meta else []
            turn_records.append({
                'program': turn['program'], 'command_call_id': call_ids[-1], 'call_ids': call_ids, 'await_call_ids': await_ids,
                'handle_await': await_meta, 'batch_plan': [c['name'] for c in batch], 'envelope_mode': envelope_mode,
                'batch_outcomes': [{'call_id': cid, 'tool': c['name'], 'outcome': res.get('outcome')} for c, (cid, res) in zip(batch, results)],
            })
        save(raw_dir / 'prelude.json', {'records': prelude_records, 'revision_aliases': revision_aliases, 'draft_bindings': draft_bindings, 'session_surface': session_surface, 'host_scope_binding': host_binding, 'git_setup': git_setup})
        application.request(f'/v1/sessions/{session_id}/abort', {})
        with contextlib.closing(sqlite3.connect(app_dir / 'store.db')) as db:
            pids = [r[0] for r in db.execute('SELECT p.id FROM invocation_programs p JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE r.session_id=?', (session_id,)).fetchall()]
        program_receipt_pages = collect_raw_api(application, session_id, raw_dir, pids)
        post_tool_lines = log_line_count(sidecar_path)
    except Exception as exc:
        save(out_dir / 'attempt.json', {'status': 'unmeasured', 'phase': 'execution', 'case_id': case['id'], 'session_id': session_id, 'error': str(exc), 'identity': identity})
        raise
    finally:
        if sse is not None:
            sse.close()
        application.close(cleanup=False)
    window = {'pre_prompt_lines': pre_prompt_lines, 'post_tool_lines': post_tool_lines, 'post_close_lines': log_line_count(sidecar_path)}
    save(raw_dir / 'window.json', window)
    eff_posture = session_surface.get('effective_posture', '')
    extra_gaps = [g for g, cond in (
        ('profile_binding_approximate', case.get('profile', 'implement') != 'implement' and not session_surface.get('declared_profile_bound')),
        ('profile_binding_mismatch', bool(eff_posture) and eff_posture != session_surface['requested_posture']),
        ('uninjected_drafts', bool(case['setup'].get('drafts')) and not drafts_ok),
        ('uninjected_host_facts', bool(case['setup'].get('host_facts')) and not (host_binding or host_facts_ok)),
    ) if cond] + [g for rec in turn_records for g in ((rec.get('handle_await') or {}).get('gaps') or ())]
    try:
        checkpoint_closed_store(app_dir / 'store.db', application.process)
        observation, audit_bundle = build_observation(
            case, session_id=session_id, identity=identity, project_dir=project_dir, db_path=app_dir / 'store.db',
            sidecar_log=sidecar_path, turn_records=turn_records, prelude_records=prelude_records, sidecar_window=window,
            extra_gaps=extra_gaps, baseline_hashes=baseline_hashes, session_surface=session_surface,
            draft_bindings=draft_bindings, host_scope_binding=host_binding, program_receipt_pages=program_receipt_pages, git_setup=git_setup,
        )
        grade = grade_case(case, observation, project_dir, identity)
    except Exception as exc:
        save(out_dir / 'attempt.json', {'status': 'unmeasured', 'phase': 'observation', 'case_id': case['id'], 'session_id': session_id, 'error': str(exc), 'identity': identity})
        raise
    save(out_dir / 'observation.json', observation)
    save(out_dir / 'grade.json', grade)
    save(raw_dir / 'db-audit.json', audit_bundle)
    save(raw_dir / 'filesystem.json', {'baseline': baseline_hashes, 'final': project_hashes(project_dir), 'revision_aliases': revision_aliases})
    save(out_dir / 'attempt.json', {'status': 'measured', 'case_id': case['id'], 'session_id': session_id, 'passed': grade['passed'], 'identity': identity})
    if export_dir is not None:
        export_bundle(export_dir, case, observation, grade, audit_bundle, baseline_hashes, raw_dir)
    return {'case_id': case['id'], 'session_id': session_id, 'observation': observation, 'grade': grade, 'audit': audit_bundle}
