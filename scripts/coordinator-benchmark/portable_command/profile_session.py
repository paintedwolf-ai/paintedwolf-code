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


def create_declared_profile_session(application, project_dir, case, *, create_base_session):
    """Compose & persist a disposable workflow binding coordinator_profile under build posture."""
    profile = case.get('profile') or ''
    wf_id = f"pc-{case['id']}"[:48]
    author_sid, project_id, root_id, _ = create_base_session(application, project_dir, {'profile': 'implement', 'turns': []})
    req = urllib.request.Request(
        application.url + f'/v1/sessions/{author_sid}/workflows/compose',
        method='POST',
        data=_workflow_yaml(wf_id, profile).encode('utf-8'),
        headers={'Content-Type': 'application/yaml', 'Authorization': 'Bearer ' + application.token},
    )
    with open_request(req) as resp:
        json.load(resp)
    application.request(
        f'/v1/sessions/{author_sid}/workflows/{wf_id}/persist',
        {'version': '1.0.0', 'confirm': True, 'trigger': '/' + wf_id},
    )
    session = application.request('/v1/sessions', {'project_id': project_id, 'posture': 'build'})
    sid = session['id']
    turn_budget = max(20, 4 + len(case.get('turns') or ()) * 16)
    application.request('/harness/model-limit', {'session_id': sid, 'limit': turn_budget})
    observed_state = {}
    def ready():
        state = application.request('/v1/sessions/' + sid)
        if state['status'] == 'preparing':
            return False
        if state['status'] != 'idle':
            raise RuntimeError('declared-profile session preparation ended in ' + state['status'])
        observed_state.update(state)
        return True
    application.wait_for(ready, 'declared-profile session ready', sid)
    run = application.request(f'/v1/sessions/{sid}/workflow-runs', {
        'operation_id': str(uuid.uuid4()), 'workflow_id': wf_id, 'workflow_version': '1.0.0',
        'request': f"Execute portable-command fixture {case['id']}.",
    })
    surface = {
        'requested_profile': profile, 'requested_posture': 'build',
        'effective_posture': observed_state.get('posture') or session.get('posture') or 'build',
        'offered_tools': [], 'session_turn_budget': turn_budget,
        'declared_profile_bound': True, 'workflow_id': wf_id, 'workflow_run_id': run.get('id'),
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
    zero_effects_ok = bool(
        (last_payload or {}).get('completed_effects', 0) == 0
        and (last_payload or {}).get('uncertain_effects', 0) == 0
        and all(e.get('state') in {'refused', 'rejected'} and e.get('effect_certainty') in {None, '', 'none'} for e in execs)
        and (baseline_hashes is None or final_hashes == baseline_hashes)
    )
    facts = {
        'readonly_profile': readonly_ok,
        'offending_source_span': span_ok,
        'no_mutating_remedy': no_mut_remedy,
        'reachable_readonly_remedy': bool(no_mut_remedy and readonly_ok),
        'prior_read_retained': any(e.get('operation_id') == 'read' and e.get('state') == 'completed' for e in execs),
        'embedded_program_validated': bool(code == 'COMMAND_EFFECT_NOT_ALLOWED' and span_ok and not (pathlib.Path(project_dir) / 'forbidden').exists()),
        'secondary_io_disabled': bool(code == 'COMMAND_NATIVE_FORM_UNSUPPORTED' and merged.get('expected') == 'no system()' and span_ok),
        'no_child_launch': bool(no_external_launch),
        'readonly_owner_code': bool(code == 'COMMAND_EFFECT_NOT_ALLOWED' and profile in feedback),
        'capability_limitation': bool(code == 'COMMAND_EFFECT_NOT_ALLOWED' and profile in feedback and no_mut_remedy),
        'honest_blocked_closeout': bool(code == 'COMMAND_EFFECT_NOT_ALLOWED' and zero_effects_ok and no_mut_remedy and readonly_ok),
    }
    if zero_effects_ok:
        facts['zero_program_effects'] = True
    if recovery and code == 'COMMAND_EFFECT_NOT_ALLOWED':
        if case['id'] == 'read-only-dynamic-refusal':
            recovery = {
                **recovery, 'cause': 'readonly_profile', 'remedy': 'readonly_or_closeout' if no_mut_remedy else '',
                'zero_program_effects': False, 'whole_program_resubmit_allowed': False,
                'effect_certainty_required': True, 'source_span_required': span_ok,
            }
        elif case['id'] == 'oar-unavailable-remedy':
            recovery = {
                **recovery, 'cause': 'unavailable_remedy',
                'remedy': 'honest_blocked_closeout' if (no_mut_remedy and profile in feedback) else '',
                'zero_program_effects': zero_effects_ok, 'whole_program_resubmit_allowed': False,
                'effect_certainty_required': zero_effects_ok, 'source_span_required': False,
            }
    return facts, recovery
