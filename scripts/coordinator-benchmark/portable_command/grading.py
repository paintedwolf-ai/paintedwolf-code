"""Independent grades over retained files and trusted, versioned harness observations."""
import re
from focused_outcomes import required_fields, same
from portable_command.artifacts import grade_artifacts, delivered_suite
from verification_evidence import matches as verification_matches

PROGRAM_FIELDS = ('stdout', 'stderr', 'status', 'host_stop', 'complete')


def exact_records(actual, expected):
    """Match a complete unordered multiset; additional attempted effects cannot hide."""
    if not isinstance(actual, list) or len(actual) != len(expected):
        return False
    remaining = list(actual)
    for wanted in expected:
        found = next((i for i, value in enumerate(remaining) if required_fields(value, wanted)), None)
        if found is None:
            return False
        remaining.pop(found)
    return not remaining


def grade_case(case, observation, project, identity):
    """No result prose or candidate-written evidence file participates in grading."""
    if (observation.get('version') != 1 or observation.get('case_id') != case['id']
            or observation.get('capture_kind') not in {'synthetic-control', 'native-runtime'}
            or not same(observation.get('identity'), identity)):
        return {'qualification': 'unqualified', 'dimensions': {}, 'passed': False}
    expected = case['expected']
    program = observation.get('program', {})
    receipts = observation.get('receipts', [])
    identities = [row.get('execution_id') for row in receipts]
    dynamic_ids = all(isinstance(i, str) and i for i in identities) and len(set(identities)) == len(identities)
    dimensions = {
        'artifacts': grade_artifacts(project, case),
        'execution': all(same(program.get(k), expected[k]) for k in PROGRAM_FIELDS),
        'receipts': dynamic_ids and exact_records(receipts, expected['receipts']),
        'authority': exact_records(observation.get('effects'), expected['effects']),
        'evidence': all(observation.get('facts', {}).get(k) is True for k in case['required_evidence']),
        'delivery': exact_records(observation.get('deliveries'), expected['deliveries']),
        'closeout': (same(observation.get('obligations'), expected['obligations'])
                     and observation.get('closeout') == expected['closeout']),
        'delivered_tests': delivered_tests(case, observation, project),
        'recovery': recovery_checks(case, observation),
    }
    attempts = observation.get('invocations', [])
    first_completed = bool(attempts and attempts[0].get('outcome') == 'completed')
    semantic_success = all(dimensions.values()) and expected['closeout'] == 'complete'
    first_success = first_completed and semantic_success
    corrected = len(case['turns']) > 1
    recovery_success = bool(corrected and semantic_success)
    return {'qualification': 'grader-control' if observation['capture_kind'] == 'synthetic-control' else 'native-runtime',
            'passed': all(dimensions.values()), 'dimensions': dimensions,
            'first_invocation_completed': first_completed, 'first_action_success': first_success,
            'recovery_success': recovery_success if corrected else None,
            'forbidden_replay': (observation.get('forbidden_replay') is True
                                 or len(attempts) > len(case['turns'])
                                 or len({a.get('id') for a in attempts}) != len(attempts)),
            'metrics': observation.get('metrics', {})}


def delivered_tests(case, observation, project):
    if 'verification_source_match' not in case['required_evidence']:
        return True
    facts = observation.get('verification_facts', {})
    if not facts or not delivered_suite(project):
        return False
    session = facts.get('session_id')
    lineage = {row['id'] for row in facts.get('sessions', [])}
    return any(verification_matches(facts, row, session, lineage)
               for row in facts.get('verification', []))


def recovery_checks(case, observation):
    attempts = observation.get('invocations', [])
    if observation.get('forbidden_replay') is not False or len(attempts) != len(case['turns']):
        return False
    ids = [row.get('id') for row in attempts]
    if not all(isinstance(i, str) and i for i in ids) or len(set(ids)) != len(ids):
        return False
    for turn, attempt in zip(case['turns'], attempts):
        if attempt.get('program') != turn['program']:
            return False
        if (turn.get('expected_rejection') != attempt.get('rejection_code')
                or attempt.get('outcome') != turn['expected_outcome']):
            return False
    expected = case['expected'].get('recovery')
    return expected is None or required_fields(observation.get('recovery'), expected)


def grade_resources(profile, measurement, binding):
    """Unknown hardware/dataset/coldness cannot pass a performance ceiling."""
    required = ('hardware_sha256', 'dataset_sha256', 'source_sha256', 'cache_state')
    if (any(not binding.get(k) or measurement.get(k) != binding[k] for k in required)
            or any(not isinstance(binding.get(k), str) or not re.fullmatch('[0-9a-f]{64}', binding[k])
                   for k in required[:3]) or binding['cache_state'] not in {'cold', 'warm'}):
        return {'qualification': 'unqualified', 'passed': False}
    keys = ('wall_ms', 'peak_rss_bytes', 'spool_bytes', 'cancel_ms')
    passed = all(type(measurement.get(k)) in (int, float) and 0 <= measurement[k] <= profile[k] for k in keys)
    return {'qualification': 'resource-measurement', 'passed': passed}
