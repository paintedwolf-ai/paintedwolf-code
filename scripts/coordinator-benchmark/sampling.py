"""Fixed-bank estimates and descriptive episode counts."""
from collections import Counter
from fractions import Fraction
import math
from math import comb
import ledger

METHOD = 'weighted-hoeffding-fixed-bank'
COUNTED_RESPONSES = {'approval_rejected', 'task_restated', 'unregistered_ask_answered'}
CODE_FAMILIES = ('COMMAND_NOT_ARGV', 'DOOM_LOOP', 'COMMAND_FAILURE_LOOP', 'COMMAND_OUTPUT_NO_LIVE_JOB', 'SANDBOX_TRY', 'SANDBOX_', 'COORDINATOR_', 'PROGRESS_', 'OVERLAY_', 'TASK_', 'WORKER_', 'SYNTH_', 'INVEST_')
SUMMARY_KEYS = ('assistant_turns', 'tool_calls', 'rejected_calls', 'approval_cards', 'interventions', 'worker_jobs',
                'promote_attempts', 'wait_calls', 'ask_calls')


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


def summarize(observations):
    """Average counts over measured trials."""
    measured = [o for o in observations if o]
    if not measured:
        return None
    return {key: sum(o.get(key, 0) for o in measured) / len(measured) for key in SUMMARY_KEYS} | {'trials': len(measured)}


def weights(operations, tier='gate'):
    families = {}
    for operation in operations:
        if operation['role'] == 'scored' and operation.get('tier', 'gate') == tier:
            families.setdefault(operation['family'], {}).setdefault(operation['scenario'], []).append(operation['id'])
    return {fixture: Fraction(1, len(families) * len(scenarios) * len(fixtures))
            for scenarios in families.values() for fixtures in scenarios.values() for fixture in fixtures}


def estimate(cases, operations, tier='gate'):
    weight = weights(operations, tier)
    selected = {case['id']: case for case in cases if case['id'] in weight}
    if selected.keys() != weight.keys() or any(c['measured'] <= 0 for c in selected.values()):
        raise ValueError('fixed-bank estimates require every scored fixture')
    score = float(sum(100 * w * Fraction(selected[key]['passed'], selected[key]['measured']) for key, w in weight.items()))
    squared = sum(w*w / selected[key]['measured'] for key, w in weight.items())
    # Two-sided Hoeffding bound for independent, bounded trial outcomes with
    # predetermined weights. It remains nonzero for an all-pass sample.
    radius = 100 * math.sqrt(math.log(40) * squared / 2)
    return {'score': score, 'interval_95': [max(0, score-radius), min(100, score+radius)], 'method': METHOD}


def pass_k(passed, measured, k):
    """Unbiased probability that k independent draws without replacement all pass (tau-bench pass^k)."""
    if measured < k or k < 1:
        return None
    return Fraction(comb(passed, k), comb(measured, k))


def reliability(cases, operations, tier, k):
    """Weighted pass^k and outcome consistency ((2p-1)^2) over the same fixed bank."""
    weight = weights(operations, tier)
    selected = {case['id']: case for case in cases if case['id'] in weight}
    if selected.keys() != weight.keys() or any(c['measured'] < k for c in selected.values()):
        return None
    strict = sum(100 * w * pass_k(selected[key]['passed'], selected[key]['measured'], k) for key, w in weight.items())
    consistency = sum(w * (2 * Fraction(selected[key]['passed'], selected[key]['measured']) - 1) ** 2 for key, w in weight.items())
    return {'k': k, 'pass_k': float(strict), 'consistency': float(100 * consistency)}
