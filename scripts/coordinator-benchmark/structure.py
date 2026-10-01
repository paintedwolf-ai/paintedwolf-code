"""Descriptive episode counts independent of scoring."""
from collections import Counter
import ledger

COUNTED_RESPONSES = {'approval_rejected', 'task_restated', 'unregistered_ask_answered'}
CODE_FAMILIES = ('COMMAND_NOT_ARGV', 'DOOM_LOOP', 'COMMAND_FAILURE_LOOP', 'COMMAND_OUTPUT_NO_LIVE_JOB', 'SANDBOX_TRY', 'SANDBOX_', 'COORDINATOR_', 'PROGRESS_', 'OVERLAY_', 'TASK_', 'WORKER_', 'SYNTH_', 'INVEST_')


def family(code):
    for prefix in CODE_FAMILIES:
        if code.startswith(prefix):
            return prefix.rstrip('_')
    return 'other'


def observe(capture, case):
    db = ledger.read(capture, case['session_id'])
    scope = ledger.session_ids(db)
    calls = ledger.assistant_calls(db, case['session_id'])
    results = ledger.tool_results(db, [case['session_id']])
    cards = ledger.checkpoints(db, scope)
    jobs = ledger.jobs(db, case['session_id'])
    turns = sum(m['role']=='assistant' and m['origin']=='model' for m in ledger.member(db,case['session_id'])['messages'])
    names = Counter(c['name'] for m in calls for c in m['calls'])
    rejected = Counter(family(code) for r in results if r['outcome'] == 'rejected' for code in r['codes'][:1])
    responses = Counter(r.get('kind', '') for r in case.get('automatic_responses') or [])
    denials = sum(1 for kind, count in responses.items() if kind == 'fixture_rule_denied' for _ in range(max(0, count - 1)))
    return {
        'assistant_turns': turns,
        'tool_calls': sum(names.values()),
        'tool_calls_by_name': dict(sorted(names.items())),
        'rejected_calls': sum(rejected.values()),
        'rejected_by_family': dict(sorted(rejected.items())),
        'approval_cards': len(cards),
        'approval_cards_by_subject': dict(sorted(Counter(c['subject'] or 'unknown' for c in cards).items())),
        'interventions': sum(count for kind, count in responses.items() if kind in COUNTED_RESPONSES) + denials,
        'worker_jobs': len(jobs),
        'worker_jobs_by_status': dict(sorted(Counter(j['status'] for j in jobs).items())),
        'promote_attempts': names.get('promote_overlay', 0),
        'wait_calls': names.get('wait', 0),
        'ask_calls': names.get('ask_user', 0),
    }


SUMMARY_KEYS = ('assistant_turns', 'tool_calls', 'rejected_calls', 'approval_cards', 'interventions', 'worker_jobs',
                'promote_attempts', 'wait_calls', 'ask_calls')


def summarize(observations):
    """Average counts over measured trials."""
    measured = [o for o in observations if o]
    if not measured:
        return None
    return {key: sum(o.get(key, 0) for o in measured) / len(measured) for key in SUMMARY_KEYS} | {'trials': len(measured)}
