"""M13 verifier and sanitized synthetic reconstruction helper for dev command diagnostics."""
import json
import pathlib

TERMINAL_STATES = {'completed', 'failed', 'refused', 'rejected', 'interrupted', 'cancelled'}
ALLOWED_DISPOSITIONS = {'contradiction', 'incomplete', 'unsupported', 'overflow'}
FORBIDDEN_RAW_KEYS = {'command', 'argv', 'args', 'env', 'environment', 'stdout', 'stderr', 'file_content', 'body'}


def load_jsonl(path):
    """Load structured diagnostic findings from a command-diagnostics.jsonl file."""
    target = pathlib.Path(path)
    if not target.is_file():
        return []
    records = []
    for raw in target.read_text(encoding='utf-8').splitlines():
        line = raw.strip()
        if line:
            records.append(json.loads(line))
    return records


def validate_redaction(records, forbidden_substrings=()):
    """Verify diagnostic records contain only bounded metadata and no forbidden raw fields or secrets."""
    violations = []
    for idx, rec in enumerate(records):
        if rec.get('schema_version') != 1:
            violations.append(f'record[{idx}]: invalid schema_version')
        if rec.get('disposition') not in ALLOWED_DISPOSITIONS:
            violations.append(f'record[{idx}]: unknown disposition {rec.get("disposition")!r}')
        if not rec.get('rule_id'):
            violations.append(f'record[{idx}]: missing rule_id')
        for key in FORBIDDEN_RAW_KEYS:
            if key in rec:
                violations.append(f'record[{idx}]: forbidden raw key {key}')
        encoded = json.dumps(rec, sort_keys=True)
        for secret in forbidden_substrings:
            if secret and secret in encoded:
                violations.append(f'record[{idx}]: leaked forbidden substring')
    return violations


def evaluate_sanitized_observation(obs):
    """Reference evaluator for sanitized synthetic reconstruction replay (M13)."""
    findings = []
    prog_id = obs.get('program_id', '')
    settled = bool(obs.get('program_settled')) and obs.get('program_state') in TERMINAL_STATES

    def make(rule_id, disposition, reason, expected, observed, *, exec_ids=(), op_ids=(), src_ids=()):
        rec = {
            'schema_version': 1,
            'rule_id': rule_id,
            'disposition': disposition,
            'program_id': prog_id,
            'reason': reason,
            'expected': expected,
            'observed': observed,
        }
        if exec_ids:
            rec['execution_ids'] = sorted(set(exec_ids))
        if op_ids:
            rec['operation_ids'] = sorted(set(op_ids))
        if src_ids:
            rec['source_ids'] = sorted(set(src_ids))
        return rec

    # 1. dup_or_conflicting_terminal
    by_exec = {}
    for tf in obs.get('terminal_records') or [e for e in obs.get('executions', []) if e.get('state') in TERMINAL_STATES]:
        eid = tf.get('execution_id', '')
        if eid and tf.get('state') in TERMINAL_STATES:
            by_exec.setdefault(eid, []).append(tf)
    dups = [eid for eid, items in by_exec.items() if len(items) > 1]
    if dups:
        findings.append(make(
            'dup_or_conflicting_terminal', 'contradiction', 'duplicate_or_conflicting_terminal_facts',
            {'terminal_records_per_execution': 1}, {'duplicate_execution_count': len(dups)}, exec_ids=dups,
        ))

    # 2. missing_child_terminal
    if not settled:
        findings.append(make(
            'missing_child_terminal', 'incomplete', 'program_not_declared_complete',
            {'program_settled': True}, {'program_settled': bool(obs.get('program_settled')), 'program_state': obs.get('program_state')},
        ))
    elif obs.get('truncated') or obs.get('executions_omitted', 0) > 0:
        findings.append(make(
            'missing_child_terminal', 'incomplete', 'truncated_execution_observation',
            {'executions_omitted': 0, 'truncated': False},
            {'executions_omitted': obs.get('executions_omitted', 0), 'truncated': bool(obs.get('truncated'))},
        ))
    else:
        missing = [e for e in obs.get('executions', []) if e.get('admitted') and e.get('state') not in TERMINAL_STATES]
        if missing:
            findings.append(make(
                'missing_child_terminal', 'contradiction', 'admitted_child_missing_terminal_record',
                {'admitted_children_terminal': True}, {'missing_terminal_count': len(missing)},
                exec_ids=[e['execution_id'] for e in missing if e.get('execution_id')],
                op_ids=[e['operation_id'] for e in missing if e.get('operation_id')],
            ))

    # 3. stale_terminal_presentation
    if settled:
        pres = obs.get('presentation')
        if not obs.get('consumer_caught_up') or pres is None:
            findings.append(make(
                'stale_terminal_presentation', 'incomplete',
                'presentation_unavailable' if pres is None else 'consumer_not_caught_up',
                {'consumer_caught_up': True, 'presentation_present': True},
                {'consumer_caught_up': bool(obs.get('consumer_caught_up')), 'presentation_present': pres is not None},
            ))
        else:
            stale = (
                pres.get('program_state') != obs.get('program_state')
                or pres.get('program_status_code') != obs.get('program_status_code')
                or pres.get('program_stop_code', '') != obs.get('program_stop_code', '')
                or (obs.get('activity_revision', 0) > 0 and pres.get('activity_revision', 0) < obs.get('activity_revision', 0))
                or any(a.get('state') not in TERMINAL_STATES for a in pres.get('activities', []))
            )
            if stale:
                findings.append(make(
                    'stale_terminal_presentation', 'contradiction', 'program_state_mismatch',
                    {'program_state': obs.get('program_state'), 'activity_revision': obs.get('activity_revision', 0)},
                    {'program_state': pres.get('program_state'), 'activity_revision': pres.get('activity_revision', 0)},
                ))

    # 4. operation_source_contradiction
    bad_execs, bad_ops, bad_srcs, inc_execs = [], [], [], []
    for link in obs.get('linked_operations', []):
        if not link.get('completed'):
            continue
        if not link.get('complete_evidence'):
            inc_execs.append(link.get('execution_id', ''))
            continue
        if ((link.get('expected_operation_id') and link.get('operation_id') and link['expected_operation_id'] != link['operation_id'])
                or (link.get('expected_source_id') and link.get('source_id') and link['expected_source_id'] != link['source_id'])
                or (link.get('expected_revision') and link.get('observed_revision') and link['expected_revision'] != link['observed_revision'])):
            bad_execs.append(link.get('execution_id', ''))
            bad_ops.extend([x for x in (link.get('expected_operation_id'), link.get('operation_id')) if x])
            bad_srcs.extend([x for x in (link.get('expected_source_id'), link.get('source_id')) if x])
    if bad_execs:
        findings.append(make(
            'operation_source_contradiction', 'contradiction', 'operation_source_mismatch',
            {'linked_operation_source_consistent': True}, {'contradiction_count': len(bad_execs)},
            exec_ids=bad_execs, op_ids=bad_ops, src_ids=bad_srcs,
        ))
    elif inc_execs:
        findings.append(make(
            'operation_source_contradiction', 'incomplete', 'incomplete_operation_source_evidence',
            {'complete_linked_evidence': True}, {'incomplete_links': len(inc_execs)}, exec_ids=inc_execs,
        ))

    # 5. gated stop_admission_after_ack
    stop_ack = obs.get('stop_ack')
    if stop_ack is not None:
        if not (stop_ack.get('qualified') and stop_ack.get('admission_witness_comparable') and stop_ack.get('ack_ordinal', 0) > 0):
            findings.append(make(
                'stop_admission_after_ack', 'unsupported', 'missing_qualified_stop_ack_or_comparable_admission_witness',
                {'qualified_stop_ack': True, 'comparable_admission_witness': True},
                {'qualified_stop_ack': bool(stop_ack.get('qualified')), 'comparable_admission_witness': bool(stop_ack.get('admission_witness_comparable'))},
            ))
        else:
            post = [e['execution_id'] for e in obs.get('executions', [])
                    if e.get('admitted') and e.get('admission_ordinal', 0) > stop_ack['ack_ordinal']]
            if post:
                findings.append(make(
                    'stop_admission_after_ack', 'contradiction', 'admission_observed_after_qualified_stop_ack',
                    {'max_admission_ordinal': stop_ack['ack_ordinal']}, {'post_ack_admission_count': len(post)}, exec_ids=post,
                ))
    return findings
