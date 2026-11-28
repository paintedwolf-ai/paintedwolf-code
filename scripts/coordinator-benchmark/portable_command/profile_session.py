"""Declared read-only workflow profile session setup and Q07/Q08/Q09 refusal oracles."""
import json
import pathlib
import urllib.request
import uuid
from bootstrap_policy import open_request
from portable_command.reporting import structured_rejection

DECLARED_READONLY_PROFILES = ('worker_readonly', 'explore_readonly', 'plan_review_readonly')
FORBIDDEN_MUTATION_TOOLS = (
    'write', 'edit', 'delete', 'replace_lines',
    'code_rewrite', 'jq_edit', 'chmod', 'chown',
)


def _workflow_yaml(wf_id, profile):
    return f"""id: {wf_id}
version: 1.0.0
extends: plan@1.0.0
surface_profile: implement
coordinator_profile: {profile}
name: {wf_id} authority witness
initial_posture: build
agents:
  - {{id: coordinator, tools: profile, spawn: false}}
phases:
  - id: research
    activity_label: Exercise {wf_id} under {profile}
    coordinator_surface: implement_investigate
    surface_template: agents/coordinator-surface-build.md
    complete_when: gates_satisfied
    gates: [evidence_passed:verify]
    advance:
      when_gate_met: coordinator
    next: build
  - id: build
    activity_label: Complete {wf_id} witness
    complete_when: gates_satisfied
    gates: [delegation_closeout_complete, evidence_passed:verify]
    advance:
      when_gate_met: coordinator
"""


def validate_workflow_init_artifacts(pre_hashes, post_hashes):
    """Verify workflow init only added .paintedwolf/blueprints/* files and left all fixture files untouched."""
    changed = [p for p, sha in (pre_hashes or {}).items() if (post_hashes or {}).get(p) != sha]
    if changed:
        raise ValueError('workflow init modified baseline fixture files: ' + ', '.join(sorted(changed)))
    added = {p: post_hashes[p] for p in sorted(set(post_hashes or {}) - set(pre_hashes or {}))}
    invalid = [
        p for p in added
        if not p.startswith('.paintedwolf/blueprints/') or '..' in pathlib.PurePosixPath(p).parts
    ]
    if invalid:
        raise ValueError('workflow init created unexpected non-blueprint files: ' + ', '.join(invalid))
    return added


def create_declared_profile_session(application, project_dir, case, *, create_base_session, baseline_hashes=None, hash_fn=None):
    """Compose a session-scoped workflow binding coordinator_profile under build posture and snapshot init blueprints."""
    profile = case.get('profile') or ''
    wf_id = f"pc-{case['id']}"[:48]
    sid, project_id, root_id, base_surface = create_base_session(application, project_dir, {'profile': 'implement', 'turns': case.get('turns') or []})
    req = urllib.request.Request(
        application.url + f'/v1/sessions/{sid}/workflows/compose',
        method='POST',
        data=_workflow_yaml(wf_id, profile).encode('utf-8'),
        headers={'Content-Type': 'application/yaml', 'Authorization': 'Bearer ' + application.token},
    )
    with open_request(req) as resp:
        json.load(resp)
    run = application.request(f'/v1/sessions/{sid}/workflow-runs', {
        'operation_id': str(uuid.uuid4()), 'workflow_id': wf_id, 'workflow_version': '1.0.0',
        'request': f"Execute portable-command fixture {case['id']}.",
    })
    post_init = hash_fn(project_dir) if callable(hash_fn) else None
    init_artifacts = validate_workflow_init_artifacts(baseline_hashes, post_init) if post_init is not None else {}
    surface = {
        **base_surface, 'requested_profile': profile, 'requested_posture': 'build',
        'effective_posture': base_surface.get('effective_posture') or 'build',
        'declared_profile_bound': True, 'workflow_id': wf_id, 'workflow_run_id': run.get('id'),
        'workflow_init_artifacts': init_artifacts, 'post_init_hashes': post_init,
    }
    return sid, project_id, root_id, surface


def has_refusal_source_span(details):
    if not isinstance(details, dict):
        return False
    for key in ('source_line', 'operation_source_line', 'program_line'):
        if isinstance(details.get(key), int) and details[key] >= 1:
            return True
    if isinstance(details.get('source_start_byte'), int) and isinstance(details.get('source_end_byte'), int):
        return True
    span = details.get('span')
    if isinstance(span, dict):
        if isinstance(span.get('line'), int) and span['line'] >= 1:
            return True
        if isinstance(span.get('start'), int) and isinstance(span.get('end'), int):
            return True
    return False


def explicit_effect_certainty(audit, payload, *, program_id=None, expected_ops=None, require_nonempty=False):
    """Verify explicit 'none' effect_certainty across a complete, matching program receipt set."""
    if not isinstance(payload, dict):
        return False
    if payload.get('completed_effects', 0) != 0 or payload.get('uncertain_effects', 0) != 0:
        return False
    progs = audit.get('invocation_programs') or []
    pid = program_id or payload.get('program_id') or (progs[-1]['id'] if progs else None)
    all_execs = [
        e for e in (audit.get('invocation_program_executions') or ())
        if not ((e.get('operation_id') or '').startswith('program ') and not e.get('native_command'))
    ]
    if pid is not None and any(e.get('program_id') != pid for e in all_execs if program_id is None):
        return False
    execs = [e for e in all_execs if pid is None or e.get('program_id') == pid]
    if require_nonempty and not execs:
        return False
    if isinstance(payload.get('executed_children'), int):
        exp_started = payload['executed_children']
        if any('owner_started' in e for e in execs):
            if sum(1 for e in execs if e.get('owner_started') == 1) != exp_started:
                return False
        else:
            non_refused = sum(1 for e in execs if e.get('state') != 'refused')
            if exp_started > len(execs) or exp_started not in {len(execs), non_refused}:
                return False
    if expected_ops is not None and len(execs) != len(expected_ops):
        return False
    return all(e.get('effect_certainty') == 'none' for e in execs)


def has_no_mutating_remedy(feedback):
    text = (feedback or '').lower()
    return bool(text) and not any(bad in text for bad in ('use write', 'use edit', 'sed -i', 'run rm '))


def inspect_readonly_authority(case, *, session_surface, project_dir, audit, recovery, last_payload, last_raw, baseline_hashes, final_hashes, no_external_launch):
    """Extract Q07, Q08, and Q09 authority facts from declared-profile surface and refusal payloads."""
    profile = case.get('profile') or ''
    if profile not in DECLARED_READONLY_PROFILES:
        return {}, recovery
    surf = session_surface or {}
    offered = set(surf.get('offered_tools') or ())
    mutation_tools_absent = 'command' in offered and all(t not in offered for t in FORBIDDEN_MUTATION_TOOLS)
    readonly_ok = bool(
        (surf.get('declared_profile_bound') is True and surf.get('requested_profile') == profile and mutation_tools_absent)
        or surf.get('effective_posture') in {'vet', 'spec'}
    )
    code, details = structured_rejection(last_raw or {}, last_payload or {})
    refusal = (last_payload or {}).get('refusal') if isinstance((last_payload or {}).get('refusal'), dict) else {}
    feedback = str(refusal.get('feedback') or (last_raw or {}).get('message') or '')
    merged = {**(refusal.get('details') if isinstance(refusal.get('details'), dict) else {}), **(details if isinstance(details, dict) else {})}
    span_ok = has_refusal_source_span(merged)
    no_mut_remedy = has_no_mutating_remedy(feedback) and mutation_tools_absent
    execs = audit['invocation_program_executions']
    exp_receipts = (case.get('expected') or {}).get('receipts') or ()
    certainty_ok = explicit_effect_certainty(
        audit, last_payload or {}, expected_ops=exp_receipts if exp_receipts else None,
        require_nonempty=bool(exp_receipts),
    )
    zero_effects_ok = bool(certainty_ok and (baseline_hashes is None or final_hashes == baseline_hashes))
    ro_code = code in {'COMMAND_EFFECT_NOT_ALLOWED', 'COMMAND_OPERATION_NOT_ALLOWED'}
    facts = {
        'readonly_profile': readonly_ok,
        'offending_source_span': span_ok,
        'no_mutating_remedy': no_mut_remedy,
        'reachable_readonly_remedy': bool(no_mut_remedy and readonly_ok),
        'prior_read_retained': any(e.get('operation_id') == 'read' and e.get('state') == 'completed' for e in execs),
        'embedded_program_validated': bool(code == 'COMMAND_EFFECT_NOT_ALLOWED' and span_ok and not (pathlib.Path(project_dir) / 'forbidden').exists()),
        'secondary_io_disabled': bool(code == 'COMMAND_NATIVE_FORM_UNSUPPORTED' and merged.get('expected') == 'no system()' and span_ok),
        'no_child_launch': bool(no_external_launch),
        'readonly_owner_code': bool(ro_code and profile in feedback),
        'capability_limitation': bool(ro_code and profile in feedback and no_mut_remedy),
        'honest_blocked_closeout': bool(ro_code and zero_effects_ok and no_mut_remedy and readonly_ok),
    }
    if zero_effects_ok:
        facts['zero_program_effects'] = True
    if recovery and ro_code:
        if case['id'] == 'read-only-dynamic-refusal':
            recovery = {
                **recovery, 'cause': 'readonly_profile', 'remedy': 'readonly_or_closeout' if no_mut_remedy else '',
                'zero_program_effects': False, 'whole_program_resubmit_allowed': False,
                'effect_certainty_required': certainty_ok, 'source_span_required': span_ok,
            }
        elif case['id'] == 'oar-unavailable-remedy':
            recovery = {
                **recovery, 'cause': 'unavailable_remedy',
                'remedy': 'honest_blocked_closeout' if (no_mut_remedy and profile in feedback) else '',
                'zero_program_effects': zero_effects_ok, 'whole_program_resubmit_allowed': False,
                'effect_certainty_required': zero_effects_ok, 'source_span_required': False,
            }
    return facts, recovery
