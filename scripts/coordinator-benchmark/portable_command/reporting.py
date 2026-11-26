"""Durable per-case evidence archiving, Q-ID qualification, and discrepancy breakdown."""
import hashlib
import pathlib
from portable_command.contracts import bytes_value, digest
from portable_command.provenance import (
    file_sha256,
    inject_drafts,
    inspect_engine_provenance,
    make_app_factory,
    resolve_engine_binary,
    resolve_engine_manifest,
    source_identity,
)
from progress import save

SETUP_GAPS = {'profile_binding_approximate', 'profile_binding_mismatch', 'uninjected_drafts', 'uninjected_host_facts'}
OBSERVATION_GAPS = {'missing_sidecar_log', 'incomplete_sidecar_log', 'missing_program_status_code'}
OPENING_MARKERS = ('server listening', 'listening', 'server started', 'startup')
CLOSING_MARKERS = (
    'shutdown signal received', 'shutdown after spawning process exited',
    'shutdown context canceled', 'engine shutdown', 'server stopped',
    'session aborted', 'session ended', 'shutdown',
)


def scan_sidecar_launches(log_path, session_id, window=None):
    """Verify sidecar log spans the full run window (opening + terminal/flush) and count launches."""
    path = pathlib.Path(log_path)
    if not path.is_file():
        return {'available': False, 'complete': False, 'command_launches': None, 'lines': []}
    lines = path.read_text(errors='replace').splitlines()
    if not lines:
        return {'available': True, 'complete': False, 'command_launches': None, 'lines': []}
    prefix = f'session_id={session_id[:8]}'
    opening_line = next(
        (n for n, line in enumerate(lines, 1)
         if prefix in line or session_id in line or any(m in line.lower() for m in OPENING_MARKERS)),
        None,
    )
    closing_line = None
    if opening_line is not None:
        for n, line in enumerate(lines, 1):
            if n > opening_line and any(m in line.lower() for m in CLOSING_MARKERS):
                closing_line = n
    window_ok = True
    if isinstance(window, dict) and isinstance(window.get('post_tool_lines'), int):
        window_ok = bool(closing_line is not None and closing_line > window['post_tool_lines'] and len(lines) > window['post_tool_lines'])
    complete = bool(opening_line is not None and closing_line is not None and closing_line > opening_line and window_ok)
    matches = [{'line': n, 'excerpt': line[:240]} for n, line in enumerate(lines, 1)
               if 'process boundary selected' in line and 'tool=command' in line and (prefix in line or session_id in line)]
    return {
        'available': True, 'complete': complete, 'scope': prefix, 'line_count': len(lines),
        'opening_line': opening_line, 'closing_line': closing_line, 'window_ok': window_ok,
        'command_launches': len(matches) if complete else None, 'lines': matches,
    }

FACT_OWNERS = {
    'actual_exit': 'jobs_owner', 'all_attempts_retained': 'live_harness', 'alternating_arm_order': 'live_harness',
    'anchored_edit': 'source_owner', 'bounded_control_flow': 'program_runtime', 'bounded_visits': 'filesystem_owner',
    'browser_obligation_open': 'browser_owner', 'build_receipt_not_closeout': 'closeout_owner',
    'captured_environment_cwd': 'jobs_owner', 'captured_source_identity': 'source_owner',
    'catalog_generation_matches_live': 'catalog_owner', 'complete_tree': 'filesystem_owner',
    'confined_archive_entries': 'archive_owner', 'confined_launch': 'process_owner',
    'delivered_tests_separate': 'verification_owner', 'delivery_owner_identity': 'source_owner',
    'disk_provenance': 'process_owner', 'duplicate_aware_scoring': 'artifact_oracle',
    'exact_argv': 'process_owner', 'exact_mapped_ranges': 'source_owner',
    'explicit_history_schema': 'artifact_oracle', 'external_disk_provenance': 'temp_owner',
    'five_attempts_preregistered': 'live_harness', 'foreign_index_preserved': 'git_owner',
    'honest_blocked_closeout': 'closeout_owner', 'immutable_identity': 'live_harness',
    'immutable_ref_evidence': 'git_owner', 'independent_artifact_oracle': 'artifact_oracle',
    'invocation_fsmonitor_policy': 'git_owner', 'owned_descendants_only': 'jobs_owner',
    'real_bzip2_cross_decoder': 'archive_owner', 'recoverable_publication': 'archive_owner',
    'same_physical_identity': 'temp_owner', 'session_authored_paths': 'git_owner',
    'temp_path_owned': 'temp_owner', 'typed_null_sink': 'program_runtime',
    'validated_resubmission_reference': 'progress_owner', 'verification_source_match': 'verification_owner',
    'screened_retained_failure_output': 'security_screen_owner',
    'retained_full_output': 'output_retention_owner',
    'full_stream_digest': 'output_retention_owner',
    'preview_independent': 'output_retention_owner',
    'every_producer_eligible': 'program_runtime',
}


def verified_source_binding(case, pins, execs, baseline_hashes):
    """Verify source pins match actual fixture paths/digests and belong to owner executions."""
    expected = {k.lstrip('./'): v for k, v in (baseline_hashes or {}).items()}
    if not expected:
        expected = {k.lstrip('./'): digest(bytes_value(v)) for k, v in case.get('setup', {}).get('files', {}).items()}
    draft_digests = {k.lstrip('./'): digest(bytes_value(v['bytes'])) for k, v in case.get('setup', {}).get('drafts', {}).items()}
    exec_ids = {e['id'] for e in execs}
    source_pins = [p for p in pins if p.get('role') == 'source' and p.get('mode') in {'published_disk', 'accepted_document'}]
    if not source_pins:
        return False, []
    matched_paths = set()
    for p in source_pins:
        rel = str(p.get('path') or '').lstrip('./')
        pin_sha = p.get('content_sha256') or p.get('disk_sha256') or ''
        allowed = {s for s in (expected.get(rel), draft_digests.get(rel)) if s}
        if p.get('execution_id') not in exec_ids or rel not in expected or not pin_sha or pin_sha not in allowed:
            return False, source_pins
        matched_paths.add(rel)
    required_paths = {k.lstrip('./') for k in case.get('setup', {}).get('revisions', {})}
    if required_paths and not (required_paths <= matched_paths):
        return False, source_pins
    return True, source_pins


def slice_sidecar_log(log_path, session_id=None, max_edge=40):
    """Retain opening, session/launch-relevant, and closing/shutdown lines for durable audit."""
    path = pathlib.Path(log_path)
    if not path.is_file():
        return None
    lines = path.read_text(errors='replace').splitlines()
    if len(lines) <= max_edge * 2:
        return '\n'.join(lines) + ('\n' if lines else '')
    prefix = f'session_id={session_id[:8]}' if session_id else ''
    keep = set(range(max_edge)) | set(range(len(lines) - max_edge, len(lines)))
    for idx, line in enumerate(lines):
        low = line.lower()
        if (prefix and prefix in line) or 'process boundary selected' in line or 'shutdown' in low:
            keep.add(idx)
    return '\n'.join(f'{i + 1}: {lines[i]}' for i in sorted(keep)) + '\n'


def archive_case_evidence(case_dir, bundle_root, *, case_id, session_id=None):
    """Copy durable per-case raw records and return portable relative locators with SHA-256 digests."""
    case_dir = pathlib.Path(case_dir)
    dest = pathlib.Path(bundle_root).resolve() / case_id
    dest.mkdir(parents=True, exist_ok=True)
    files = {}
    candidates = (
        'attempt.json', 'observation.json', 'grade.json',
        'raw/invocations.json', 'raw/messages.json', 'raw/events.jsonl',
        'raw/program-receipts.json', 'raw/receipt-documents.json',
        'raw/db-audit.json', 'raw/window.json', 'raw/prelude.json', 'raw/filesystem.json',
    )
    for rel in candidates:
        src = case_dir / rel
        if src.is_file():
            target = dest / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            data = src.read_bytes()
            target.write_bytes(data)
            files[rel] = {'path': f'{case_id}/{rel}', 'sha256': hashlib.sha256(data).hexdigest(), 'bytes': len(data)}
    sliced = slice_sidecar_log(case_dir / 'app' / 'sidecar.log', session_id=session_id)
    if sliced is not None:
        target = dest / 'raw' / 'sidecar-slice.log'
        target.parent.mkdir(parents=True, exist_ok=True)
        data = sliced.encode('utf-8')
        target.write_bytes(data)
        files['raw/sidecar-slice.log'] = {'path': f'{case_id}/raw/sidecar-slice.log', 'sha256': hashlib.sha256(data).hexdigest(), 'bytes': len(data)}
    manifest = {'case_id': case_id, 'bundle_dir': case_id, 'files': files}
    save(dest / 'manifest.json', manifest)
    return manifest


def _is_observation_gap(gap):
    return gap in OBSERVATION_GAPS or gap.startswith('missing_observation:')


def classify_case_disposition(record, *, engine_provenance=None):
    """Assign explicit qualified/unqualified disposition and discrepancy category."""
    gaps = list(record.get('observation_gaps') or [])
    reasons = list(gaps)
    if record.get('has_progress_update') and record.get('native_envelope_qualified') is not True:
        if 'sibling_progress_substitute' not in reasons:
            reasons.append('sibling_progress_substitute')
    if record.get('attempt') != 'measured' and 'unmeasured_attempt' not in reasons:
        reasons.append('unmeasured_attempt')
    if record.get('qualification') != 'native-runtime' and 'non_native_qualification' not in reasons:
        reasons.append('non_native_qualification')
    if isinstance(engine_provenance, dict) and engine_provenance.get('provenance_verified') is False:
        if 'unverified_engine_provenance' not in reasons:
            reasons.append('unverified_engine_provenance')

    disposition = 'qualified' if not reasons else 'unqualified'
    qualified_pass = bool(disposition == 'qualified' and record.get('passed') is True)
    categories = []
    if record.get('has_progress_update') and record.get('native_envelope_qualified') is not True:
        categories.append('proven_contract_mismatch')
    if any(g in SETUP_GAPS for g in gaps) or record.get('attempt') != 'measured':
        categories.append('missing_setup')
    if any(_is_observation_gap(g) for g in gaps) or 'unverified_engine_provenance' in reasons:
        categories.append('missing_observation')
    if not qualified_pass and not categories:
        categories.append('runtime_issue')
    return {
        'disposition': disposition,
        'unqualified_reasons': reasons,
        'qualified_pass': qualified_pass,
        'discrepancy_category': None if qualified_pass else categories[0],
        'discrepancy_categories': [] if qualified_pass else categories,
    }


def aggregate_qualification(all_cases, records):
    """Aggregate Q01-Q22 strictly: a Q ID passes only when all required cases have qualified native passes."""
    by_id = {r['case_id']: r for r in records}
    planned_by_q = {}
    for c in all_cases:
        for q in c['qualification_cases']:
            planned_by_q.setdefault(q, []).append(c['id'])
    per_q, passed_selected, passed_full = {}, [], []
    for q in sorted(planned_by_q):
        planned = planned_by_q[q]
        selected = [cid for cid in planned if cid in by_id]
        qual_passed = [cid for cid in selected if by_id[cid]['qualified_pass']]
        diag_passed = [cid for cid in selected if by_id[cid]['passed']]
        unqualified = [cid for cid in selected if by_id[cid]['disposition'] != 'qualified']
        sel_ok = bool(selected) and len(qual_passed) == len(selected)
        full_ok = len(selected) == len(planned) and len(qual_passed) == len(planned)
        if sel_ok:
            passed_selected.append(q)
        if full_ok:
            passed_full.append(q)
        per_q[q] = {
            'planned_cases': planned, 'selected_cases': selected,
            'qualified_passed_cases': qual_passed, 'diagnostic_passed_cases': diag_passed,
            'unqualified_cases': unqualified, 'selected_complete': sel_ok, 'full_complete': full_ok,
            'status': 'passed_full' if full_ok else ('passed_selected_partial' if sel_ok else 'not_passed'),
        }
    return per_q, passed_selected, passed_full


def build_discrepancy_breakdown(records):
    """Break all non-qualified-passing cases down by missing_setup, missing_observation, proven_contract_mismatch, runtime_issue."""
    buckets = {k: [] for k in ('missing_setup', 'missing_observation', 'proven_contract_mismatch', 'runtime_issue')}
    for r in records:
        cat = r.get('discrepancy_category')
        if cat in buckets:
            buckets[cat].append(r['case_id'])
    return {
        'counts': {k: len(v) for k, v in buckets.items()},
        'cases': buckets,
        'total_discrepancies': sum(len(v) for v in buckets.values()),
    }
