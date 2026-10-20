"""Preregister paired runs and retain every slot. No provider invocation lives here."""
import json
import math
import re
from focused_outcomes import read_json
from portable_command_contracts import DIRECTORY, digest

SHA256 = re.compile(r'[0-9a-f]{64}')
SOURCE_FIELDS = ('application_sha256', 'configuration_sha256', 'bialy_sha256',
                 'catalog_sha256', 'fixtures_sha256', 'grader_sha256', 'hardware_sha256')
MODEL_FIELDS = ('backend_kind', 'backend_version', 'backend_artifact_sha256',
                'resolved_model_id', 'model_artifact_sha256', 'request_controls_sha256')


def load_live_manifest():
    manifest = read_json(DIRECTORY / 'live.json')
    if (not isinstance(manifest, dict) or manifest.get('version') != 1
            or manifest.get('attempts_per_task_model_arm') != 5
            or manifest.get('arms') != ['real-shell-reference', 'production-native']):
        raise ValueError('unsupported paired qualification manifest')
    validate_manifest(manifest)
    return manifest


def validate_manifest(manifest):
    from portable_command_contracts import load_suite
    cases = {case['id'] for case in load_suite()['cases']}
    if (manifest.get('version') != 1 or manifest.get('attempts_per_task_model_arm') != 5
            or manifest.get('arms') != ['real-shell-reference', 'production-native']
            or len(set(manifest.get('tasks', []))) != len(manifest.get('tasks', []))
            or not manifest.get('tasks') or not set(manifest['tasks']) <= cases
            or [m['role'] for m in manifest['models']] != ['small', 'medium', 'large']):
        raise ValueError('invalid frozen task/model/arm inventory')
    for key in ('model_responses', 'wall_ms', 'tokens', 'interventions', 'attempts'):
        if type(manifest['budgets'].get(key)) is not int or manifest['budgets'][key] <= 0:
            raise ValueError('missing positive fixed task allowance')
    if manifest['budgets']['attempts'] != 5:
        raise ValueError('qualification requires five attempts')


def freeze_plan(manifest, source, models, authorization):
    """Require privately verified identities before any later P31 runner may execute."""
    validate_manifest(manifest)
    if (authorization.get('allow_live') is not True or not authorization.get('approval_reference')
            or not authorization.get('isolated_host_reference')
            or type(authorization.get('maximum_cost_usd')) not in (int, float)
            or not math.isfinite(authorization['maximum_cost_usd']) or authorization['maximum_cost_usd'] <= 0):
        raise ValueError('explicit isolated-host/model/cost authorization is missing')
    for key in SOURCE_FIELDS:
        if not isinstance(source.get(key), str) or not SHA256.fullmatch(source[key]):
            raise ValueError('missing immutable source/configuration/grader/hardware identity: ' + key)
    selected = {row['role']: row for row in models}
    if len(selected) != len(models):
        raise ValueError('duplicate model role')
    for requirement in manifest['models']:
        binding = selected.get(requirement['role'], {})
        validate_model(requirement, binding)
        if binding['resolved_model_id'] not in authorization.get('allowed_resolved_models', []):
            raise ValueError('model is not covered by this authorization')
    if set(selected) != {row['role'] for row in manifest['models']}:
        raise ValueError('model roster differs from frozen small/medium/large plan')
    episodes = []
    for task in manifest['tasks']:
        for model in manifest['models']:
            for attempt in range(5):
                arms = manifest['arms'] if attempt % 2 == 0 else list(reversed(manifest['arms']))
                for order, arm in enumerate(arms):
                    episodes.append({'slot': len(episodes), 'task': task, 'model_role': model['role'],
                                     'attempt': attempt + 1, 'arm': arm, 'order': order})
    frozen = {'version': 1, 'source': source, 'models': models, 'episodes': episodes,
              'manifest': manifest, 'authorization_reference': authorization['approval_reference'],
              'maximum_cost_usd': authorization['maximum_cost_usd']}
    frozen = json.loads(json.dumps(frozen))
    frozen['plan_sha256'] = digest(json.dumps(frozen, sort_keys=True, separators=(',', ':')).encode())
    return frozen


def validate_model(requirement, binding):
    if binding.get('selection') != requirement['selection']:
        raise ValueError('model selection differs from preregistered contract')
    for key in MODEL_FIELDS:
        value = binding.get(key)
        if not isinstance(value, str) or not value or (key.endswith('sha256') and not SHA256.fullmatch(value)):
            raise ValueError('missing exact backend/model artifact identity: ' + key)
    if binding.get('runtime_identity_verified') is not True:
        raise ValueError('model identity must come from existing runtime/provider preflight')
    if requirement.get('resolved_id') and binding['resolved_model_id'] != requirement['resolved_id']:
        raise ValueError('resolved model differs from fixed task roster')
    if binding.get('alias_dependent') is not False and not binding.get('alias_reproducibility_limit'):
        raise ValueError('an alias requires an explicit reproducibility limitation')


def summarize_slots(plan, attempts):
    """Missing, interrupted and infrastructure attempts remain in the denominator."""
    unsigned = {key: value for key, value in plan.items() if key != 'plan_sha256'}
    if digest(json.dumps(unsigned, sort_keys=True, separators=(',', ':')).encode()) != plan.get('plan_sha256'):
        raise ValueError('frozen plan changed after preregistration')
    seen = {}
    for attempt in attempts:
        slot = attempt.get('slot')
        if type(slot) is not int or slot in seen or not 0 <= slot < len(plan['episodes']):
            raise ValueError('duplicate or unknown preregistered slot; no outcome-selected replacement')
        if attempt.get('plan_sha256') != plan['plan_sha256']:
            raise ValueError('attempt identity differs from frozen plan')
        if attempt.get('episode') != plan['episodes'][slot]:
            raise ValueError('attempt does not identify its exact task/model/arm')
        seen[slot] = attempt
    trials = []
    for episode in plan['episodes']:
        record = seen.get(episode['slot'])
        state = 'pending' if record is None else record.get('outcome')
        if state not in {'pending', 'passed', 'failed', 'infrastructure', 'interrupted', 'unqualified'}:
            raise ValueError('unknown attempt outcome')
        if state in {'passed', 'failed'}:
            try:
                require_measurement(record, plan, episode)
            except ValueError:
                state = 'unqualified'
        trials.append({'episode': episode, 'outcome': state, 'record': record})
    known_cost = sum(t['record']['cost_usd'] for t in trials if t['record']
                     and type(t['record'].get('cost_usd')) in (int, float)
                     and math.isfinite(t['record']['cost_usd']) and t['record']['cost_usd'] >= 0)
    cost_within_authorization = known_cost <= plan['maximum_cost_usd']
    return {'planned': len(trials), 'trials': trials, 'known_cost_usd': known_cost,
            'cost_within_authorization': cost_within_authorization,
            'qualified': all(t['outcome'] in {'passed', 'failed'} for t in trials),
            'accepted': cost_within_authorization and all(t['outcome'] == 'passed' for t in trials)}


def require_measurement(record, plan, episode):
    model = next(m for m in plan['models'] if m['role'] == episode['model_role'])
    if (record.get('source') != plan['source'] or record.get('model') != model
            or record.get('runtime_before') != model['model_artifact_sha256']
            or record.get('runtime_after') != model['model_artifact_sha256']
            or not SHA256.fullmatch(record.get('raw_capture_sha256', ''))):
        raise ValueError('attempt lacks matching source/model/provider capture identity')
    for key in ('artifact_grade', 'delivered_tests_grade', 'authority_grade', 'recovery_grade', 'closeout_grade'):
        if type(record.get(key)) is not bool:
            raise ValueError('measurement lacks separate independent grades')
    usage = record.get('usage', {})
    for group in ('agent_stream', 'auxiliary'):
        counters = usage.get(group, {})
        for key in ('input_tokens', 'cached_tokens', 'output_tokens', 'requests'):
            if type(counters.get(key)) is not int or counters[key] < 0:
                raise ValueError('stream and auxiliary usage must be retained independently')
    if record.get('outcome') == 'passed' and not all(record[k] for k in
            ('artifact_grade', 'delivered_tests_grade', 'authority_grade', 'recovery_grade', 'closeout_grade')):
        raise ValueError('a pass cannot hide a failed independent grade')

    budgets = plan['manifest']['budgets']
    for key, maximum in [('wall_ms', budgets['wall_ms']), ('interventions', budgets['interventions'])]:
        value = record.get(key)
        if type(value) not in (int, float) or not math.isfinite(value) or not 0 <= value <= maximum:
            raise ValueError('attempt exceeded frozen task allowance')
    if (sum(usage[g]['requests'] for g in ('agent_stream', 'auxiliary')) > budgets['model_responses']
            or sum(usage[g]['input_tokens'] + usage[g]['output_tokens'] for g in ('agent_stream', 'auxiliary')) > budgets['tokens']):
        raise ValueError('attempt exceeded frozen response/token allowance')
    cost = record.get('cost_usd')
    if cost is None:
        if record.get('pricing_state') != 'unpriced':
            raise ValueError('missing price must remain unpriced')
    elif type(cost) not in (int, float) or not math.isfinite(cost) or cost < 0:
        raise ValueError('invalid captured cost')
