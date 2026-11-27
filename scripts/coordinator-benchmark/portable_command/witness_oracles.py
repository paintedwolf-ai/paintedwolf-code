"""Independent SQLite, checkpoint, document, HTTP, and listener-rebind oracles for Q13, Q14, Q15, and Q17."""
import pathlib


def inspect_live_witnesses(case, *, project_dir, audit, receipts, effects, last_payload, recovery, witness_meta, invocations):
    """Extract independent facts and refine recovery/effects for Q13, Q14, Q15, and Q17."""
    cid = case['id']
    if cid not in {'stale-approval', 'publication-barrier', 'partial-write-unknown-http', 'listener-rebind'}:
        return {}, recovery
    wm = witness_meta if isinstance(witness_meta, dict) else {}
    execs = audit.get('invocation_program_executions') or []
    cmd_receipts = [r for r in (audit.get('invocation_receipts') or ()) if r.get('tool_name') == 'command']
    proj = pathlib.Path(project_dir)
    facts = {}

    if cid == 'stale-approval':
        sed_execs = [e for e in execs if e.get('native_command') == 'sed' or e.get('operation_id') == 'sed']
        stop_code = (recovery or {}).get('owner_code') or (last_payload or {}).get('refusal', {}).get('code') or (last_payload or {}).get('stop_code') or ''
        has_digest = any(len(e.get('arguments_digest') or '') == 64 or len(e.get('reviewed_arguments_digest') or '') == 64 for e in execs)
        cp_ok = bool(wm.get('checkpoint_id'))
        human_rev, after_rev = wm.get('human_revision'), wm.get('after_revision')
        doc_before = wm.get('doc_before_rev')
        disk_ok = (proj / 'AGENTS.md').is_file() and (proj / 'AGENTS.md').read_text(errors='replace') == 'alpha\n'
        facts['held_prepared_digest'] = bool(cp_ok and has_digest)
        facts['stale_revision'] = bool(stop_code == 'COMMAND_REVISION_CONFLICT' and human_rev and doc_before and human_rev > doc_before)
        facts['producer_attempts_one'] = len(sed_execs) == 1
        facts['human_edit_preserved'] = bool(human_rev and after_rev == human_rev and disk_ok)
        facts['no_recompute'] = len(cmd_receipts) == 1 and len(sed_execs) == 1
        if recovery is not None and stop_code == 'COMMAND_REVISION_CONFLICT':
            recovery = {
                **recovery, 'cause': 'stale_revision', 'remedy': 'fresh_read_and_preparation',
                'zero_program_effects': False, 'whole_program_resubmit_allowed': False,
                'effect_certainty_required': True, 'source_span_required': False,
            }

    elif cid == 'publication-barrier':
        disk_in = (proj / 'dir' / 'input.txt').read_text(errors='replace') if (proj / 'dir' / 'input.txt').is_file() else ''
        disk_un = (proj / 'unrelated.txt').read_text(errors='replace') if (proj / 'unrelated.txt').is_file() else ''
        marker_absent = not (proj / 'marker.txt').exists()
        ran_marker = any('marker.txt' in (e.get('canonical') or '') for e in execs)
        facts['accepted_document_recoverable'] = bool(
            disk_in == 'old\n' and wm.get('input_doc_dirty') is True
            and bool(wm.get('input_held_agent_version_id'))
            and (wm.get('input_doc_after_rev') or 0) > (wm.get('input_doc_before_rev') or 0)
        )
        facts['dependent_process_not_launched'] = bool(marker_absent and not ran_marker)
        facts['unrelated_draft_unchanged'] = bool(
            disk_un == 'disk\n' and wm.get('unrelated_doc_dirty') is True
            and wm.get('unrelated_doc_after_rev') is not None
            and wm.get('unrelated_doc_after_rev') == wm.get('unrelated_doc_before_rev')
        )

    elif cid == 'partial-write-unknown-http':
        reqs = [r for r in (wm.get('http_requests') or ()) if r.get('method') == 'POST' and r.get('path') == '/commit']
        first_ok = (proj / 'first.txt').is_file() and (proj / 'first.txt').read_text(errors='replace') == 'first\n'
        never_absent = not (proj / 'never').exists()
        touch_started = any(e.get('native_command') == 'touch' or 'never' in (e.get('canonical') or '') for e in execs)
        write_done = any(e.get('operation_id') == 'write' and e.get('effect_certainty') == 'completed' for e in execs)
        http_unknown = any((e.get('operation_id') == 'http_request' or e.get('native_command') == 'curl') and e.get('effect_certainty') == 'unknown' for e in execs)
        comp_eff = (last_payload or {}).get('completed_effects') or 0
        unc_eff = (last_payload or {}).get('uncertain_effects') or 0
        for r in receipts:
            if r.get('operation') == 'http_request' and http_unknown:
                r['effect_certainty'] = 'unknown'
        for e in effects:
            if e.get('operation') == 'http_request' and http_unknown:
                e['effect_certainty'] = 'unknown'
                e['recipient'] = 'fixture-http'
        facts['completed_unknown_not_started'] = bool(first_ok and never_absent and not touch_started and comp_eff >= 1 and unc_eff >= 1)
        facts['owner_journal_reconciliation'] = bool(write_done and http_unknown)
        facts['http_attempts_one'] = len(reqs) == 1 and reqs[0].get('body') == 'payload'
        facts['no_whole_program_retry'] = (last_payload or {}).get('replay_available') is not True and len(cmd_receipts) == 1
        if recovery is not None or (comp_eff >= 1 and unc_eff >= 1):
            base_rec = recovery or {'case_id': cid, 'automatic_execution': False, 'owner_code': (last_payload or {}).get('stop_code') or 'HTTP_TRANSPORT_ERROR'}
            recovery = {
                **base_rec, 'cause': 'partial_unknown', 'remedy': 'specific_remaining_status_action',
                'zero_program_effects': False, 'whole_program_resubmit_allowed': False,
                'effect_certainty_required': True, 'source_span_required': False,
            }

    elif cid == 'listener-rebind':
        inv0 = (invocations or [{}])[-1]
        rebound_refused = bool(inv0.get('outcome') == 'rejected' or (last_payload or {}).get('status') not in (None, 0) or inv0.get('rejection_code'))
        hits = wm.get('unrelated_hits')
        facts['observed_listener_identity'] = wm.get('owned_listener_succeeded') is True
        facts['revoked_on_rebind'] = rebound_refused
        facts['no_request_sent'] = hits == 0

    return facts, recovery
