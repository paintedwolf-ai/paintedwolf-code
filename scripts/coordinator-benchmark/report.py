"""Automatically grade a frozen coordinator matrix and export only public facts."""
import argparse
import hashlib
import json
from capture import verify_capture_files
import math
import pathlib
import subprocess
from worker_evidence import worker_checks
import episode_evidence
from focused_evidence import prepared_case, preparation_checks, returned_worker_checks, sandbox_checks
from candidate_tests import changed_test_paths
from verification_evidence import verification_checks
from artifacts import retain_project, retain_outside, InvalidCandidate
from grading_identity import grading_hash, report_id, verify_grader, retain_grader
from grading_state import report_lease
from grade import grade, IMAGE, GradingUnavailable
from measurement_cache import measure
from progress import save
from built_runtime import verify_application
from snapshot import ROOT, identity, file_hash
from local_runtime import runtime_identity
from execution_evidence import verify_execution
from coordinator_evidence import closeout_checks, progress_checks
from orchestration_evidence import planned_dispatch_checks
from decision_evidence import decision_checks, question_checks
from capability_evidence import capability_checks
from closeout_evidence import method_checks, cited_finding_checks
from fixture_contracts import outside_checks
import structure
import cost_evidence
from workflow_evidence import workflow_checks
from allowance_evidence import verify_allowance, allowance_checks
from sampling import estimate, reliability, METHOD

MANIFEST = json.loads((pathlib.Path(__file__).parent / 'benchmark.json').read_text())
CASES = {c['id']: (c['title'], c['grader']) for c in MANIFEST['operations']}
OPERATIONS = {c['id']: c for c in MANIFEST['operations']}
TIERS = MANIFEST['tiers']
SCORED = {c['id'] for c in MANIFEST['operations'] if c['role'] == 'scored'}
SCORED_BY_TIER = {tier: {c['id'] for c in MANIFEST['operations'] if c['role'] == 'scored' and c.get('tier', 'gate') == tier} for tier in TIERS}
GRADING_SHA = grading_hash(pathlib.Path(__file__).parent)


def interval(passed, total):
    if not total:
        return [0.0, 100.0]
    z = 1.959963984540054
    p = passed / total
    center = (p + z*z/(2*total)) / (1 + z*z/total)
    half = z * math.sqrt(p*(1-p)/total + z*z/(4*total*total)) / (1+z*z/total)
    return [max(0, 100*(center-half)), min(100, 100*(center+half))]


def overall_score(cases, repetitions, tier='gate', mode='release'):
    scored = SCORED_BY_TIER[tier]
    selected = [c for c in cases if c['id'] in scored]
    minimum = TIERS[tier]['release_repetitions'] if mode == 'release' else MANIFEST['exploration_repetitions']
    complete = (mode in {'release', 'exploration'} and repetitions >= minimum
                and {c['id'] for c in selected} == scored and len(selected) == len(scored)
                and all(c['attempts'] == repetitions and c['measured'] == repetitions for c in selected))
    if not complete:
        return None
    return estimate(selected, MANIFEST['operations'], tier)['score']


def tier_result(cases, repetitions, mode, tier):
    """A tier's public numbers: mean with its bound, pass^k, consistency, and structural means by family."""
    score = overall_score(cases, repetitions, tier, mode)
    scored = [c for c in cases if c['id'] in SCORED_BY_TIER[tier]]
    families = {}
    for case in scored:
        observations = [t.get('structure') for t in case['trials'] if t.get('structure')]
        families.setdefault(case['family'], []).extend(observations)
    result = {'score': score, 'score_interval_95': estimate(scored, MANIFEST['operations'], tier)['interval_95'] if score is not None else None,
              'pass_k': None, 'consistency': None,
              'structure': {family: structure.summarize(observations) for family, observations in sorted(families.items())}}
    if score is not None:
        strict = reliability(scored, MANIFEST['operations'], tier, TIERS[tier]['release_repetitions'])
        if strict:
            result['pass_k'], result['consistency'], result['k'] = strict['pass_k'], strict['consistency'], strict['k']
    return result


def fixture_hash(root):
    h = hashlib.sha256()
    def walk(path):
        for entry in sorted(path.iterdir()):
            if entry.is_symlink():
                raise ValueError('fixture contains a symlink')
            if entry.is_dir():
                walk(entry)
            else:
                body = entry.read_bytes()
                h.update(entry.relative_to(root).as_posix().encode() + b'\0' + str(len(body)).encode() + b'\0' + body)
    walk(root)
    return h.hexdigest()


def route_check(case, config, rows):
    primary = (config['coordinator']['provider'], config['coordinator']['model'])
    pool = {(m['provider'], m['model']) for m in config['workers']}
    own = [r for r in rows if r.get('call') == 'stream' and r.get('session_id') == case['session_id']]
    children = [r for r in rows if r.get('call') == 'stream' and r.get('parent_session_id') == case['session_id']]
    valid = bool(own) and all((r['provider_id'], r['model']) == primary for r in own)
    valid = valid and all((r['provider_id'], r['model']) in pool for r in children)
    return valid, {r['session_id'] for r in children}


def observed_controls(case, rows):
    controls = {}
    for row in rows:
        own = row.get('session_id') == case['session_id']
        child = row.get('parent_session_id') == case['session_id']
        if not own and not child:
            continue
        role = ('coordinator' if own else 'worker') if row.get('call') == 'stream' else 'utility'
        for wire in row.get('request_controls', []):
            observation = {'role': role, 'provider': row['provider_id'], 'model': row['model'], 'controls': wire}
            controls[json.dumps(observation, sort_keys=True)] = observation
    return [controls[key] for key in sorted(controls)]


def orchestration_checks(capture, case, spec, operation, outside=None):
    """Tier-2 obligations, each declared on the manifest operation and read from host facts."""
    checks = []
    if (spec.get('setup') or {}).get('dispatches'):
        checks.extend(planned_dispatch_checks(capture, case, spec, operation.get('dispatch', {})))
    if operation.get('decisions'):
        checks.extend(decision_checks(capture, case, spec, operation['decisions']))
    if operation.get('tier') == 'orchestration':
        checks.extend(question_checks(capture, case))
    if operation.get('capability'):
        checks.extend(capability_checks(capture, case, operation['capability']))
    if operation.get('closeout_method'):
        checks.extend(method_checks(capture, case, operation['closeout_method']))
    if operation.get('cited_findings'):
        checks.extend(cited_finding_checks(capture, case, operation['cited_findings']))
    if spec.get('sandbox') == 'write_root':
        retained = outside or capture / 'graded-outside' / 'files'
        checks.extend(outside_checks(case['id'], retained if retained.is_dir() else None))
    return checks


def unmeasured(case_id, reason):
    return {'id': case_id, 'outcome': 'unmeasured', 'reason': reason}


def evaluate_case(case, config, rows, capture, spec, source):
    route_ok, workers = route_check(case, config, rows)
    if not route_ok or case.get('capture_error'):
        return unmeasured(case['id'], 'Configuration or capture could not be verified.')
    if case['status'] in {'error', 'blocked', 'running', 'awaiting_input', 'awaiting_approval'}:
        return unmeasured(case['id'], 'The application, provider, or harness did not complete this attempt.')
    from ledger import scripted_execution_errors
    if scripted_execution_errors(capture, case, spec):
        return unmeasured(case['id'], 'Scripted worker execution could not be verified.')
    controls = observed_controls(case, rows)
    if not any(c['role'] == 'coordinator' for c in controls):
        return unmeasured(case['id'], 'Application request controls could not be verified.')
    checks = outcome_checks(capture, case, spec, source, workers)
    result = {'id': case['id'], 'request_controls': controls, 'outcome': 'passed' if all(c['passed'] for c in checks) else 'failed',
              'checks_passed': sum(c['passed'] for c in checks), 'checks_total': len(checks),
              'failed_checks': [c['id'] for c in checks if not c['passed']]}
    if OPERATIONS[case['id']].get('tier', 'gate') == 'orchestration':
        result['structure'] = structure.observe(capture, case)
    (capture / f"outcome-{case['id']}-{case['run']}-{GRADING_SHA[:12]}.json").write_text(json.dumps({'result': result, 'checks': checks}, indent=2) + '\n')
    return result


class CheckIdentityError(ValueError):
    def __init__(self, duplicates):
        self.duplicates = duplicates
        super().__init__('duplicate outcome check identities: ' + ', '.join(duplicates))


def outcome_checks(capture, case, spec, source, workers=(), outside=None):
    """Production acceptance checks, shared by paid captures and manual trajectory controls."""
    from ledger import scripted_execution_errors
    if scripted_execution_errors(capture, case, spec):
        raise ValueError('scripted worker execution failed or its receipt is missing')
    preparation_checks(capture, case, spec)
    baseline = source / 'lycaon/test/fixtures/eval' / spec['project']
    if case.get('invalid_artifact'):
        checks = [{'id': 'regular-project-files', 'passed': False}]
    else:
        checks = list(grade(pathlib.Path(case['project_dir']), baseline, CASES[case['id']][1])['checks'])
    completed = case['status'] == 'review_required'
    operation = OPERATIONS[case['id']]
    if operation.get('workflow'):
        checks.append({'id':'application-completed','passed':completed})
        checks.extend(workflow_checks(capture, case, operation['workflow']))
    else:
        checks.append({'id': 'final-handoff', 'passed': completed and bool(case.get('final', '').strip())})
    checks.append({'id': 'unattended-completion', 'passed': case['status'] != 'interaction_exhausted'})
    checks.extend(allowance_checks(case))
    if case['id'] == 'worker-integration':
        checks.extend(worker_checks(capture, case, workers,
                                    changed_test_paths(pathlib.Path(case['project_dir']), baseline)))
    if spec.get('setup'):
        checks.extend(returned_worker_checks(capture, case, spec, OPERATIONS[case['id']].get('overlay_outcomes')))
    if spec.get('sandbox') in {'approve_read', 'deny_read', 'loopback', 'deny_loopback'} and not case.get('invalid_artifact'):
        checks.extend(sandbox_checks(capture, case))
    if not case.get('invalid_artifact') and OPERATIONS[case['id']]['verification']:
        checks.extend(verification_checks(capture, case, source))
    operation = OPERATIONS[case['id']]
    if operation.get('closeout_paths'):
        checks.extend(closeout_checks(capture, case, operation['closeout_paths']))
    if operation.get('progress'):
        checks.extend(progress_checks(capture, case, operation['progress']))
    if not case.get('invalid_artifact'):
        checks.extend(orchestration_checks(capture, case, spec, operation, outside))
    ids = [check['id'] for check in checks]
    duplicates = sorted({name for name in ids if ids.count(name) > 1})
    if duplicates:
        raise CheckIdentityError(duplicates)
    return checks


def captured_trial(record, item, config, suite, seen, source):
    case_id = item['case']
    if record is None or 'report' not in record:
        return unmeasured(case_id, 'The isolated application could not produce an episode report.')
    capture = source.parent / record['capture']
    expected = source.parent / 'captures' / f"episode-{item['index']:03}"
    if not capture.resolve().is_relative_to(expected.resolve()):
        raise ValueError('capture escapes its planned slot')
    path = capture / 'report.json'
    if pathlib.Path(record['report']).resolve() != path.resolve():
        raise ValueError('report does not match its capture')
    raw = json.loads(path.read_text())
    if raw['suite_sha256'] != file_hash(source / MANIFEST['suite']) or (path.parent / 'suite.json').read_bytes() != (source / MANIFEST['suite']).read_bytes():
        raise ValueError('captured suite differs from the frozen benchmark')
    if json.loads((path.parent / 'configuration.json').read_text()) != config:
        raise ValueError('captured configuration differs from the planned application and model')
    verify_capture_files(capture, config)
    runtime = runtime_identity(config)
    if runtime is not None and json.loads((path.parent / 'runtime-verified.json').read_text()) != {'identity': runtime}:
        raise ValueError('local runtime identity was not verified around the episode')
    if len(raw['cases']) != 1 or raw['cases'][0]['id'] != case_id:
        raise ValueError('capture does not match its planned operation')
    case = raw['cases'][0]
    if case.get('workflow_id') != suite[case_id].get('workflow_id'):
        raise ValueError('workflow identity differs from the frozen fixture')
    if case.get('session_id'):
        episode_evidence.export(capture,{'session_id':case['session_id']},source)
    if not verify_execution(capture, case):
        return {**unmeasured(case_id, 'The application could not admit an execution.'),
                'infrastructure_attempts': record['infrastructure_attempts']}
    case = prepared_case(capture, case, suite[case_id])
    verify_allowance(capture, case, json.loads((source / MANIFEST['suite']).read_text()).get('task_allowance', 0))
    if case.get('session_id') in seen:
        raise ValueError('duplicate captured session')
    seen.add(case.get('session_id'))
    if case.get('fixture_sha256') != fixture_hash(source / 'lycaon/test/fixtures/eval' / suite[case_id]['project']):
        raise ValueError('fixture differs from the frozen benchmark')
    available = case.get('session_id') and pathlib.Path(case.get('project_dir', '')).is_dir()
    if available or (path.parent / 'graded-project').exists():
        try:
            case = {**case, 'source_project_dir': case['project_dir'],
                    'project_dir': str(retain_project(path.parent, case))}
        except InvalidCandidate:
            case = {**case, 'invalid_artifact': True}
    if (case.get('sandbox') or {}).get('kind') == 'write_root':
        try:
            retain_outside(path.parent, case)
        except InvalidCandidate:
            pass
    def rows(name):
        return [json.loads(line) for line in (path.parent / name).read_bytes().splitlines()]
    episode_evidence.export(capture, case, source)
    evidence = measurement_inputs(capture)
    result = measure(capture, GRADING_SHA, evidence,
                     lambda: evaluate_case(case, config, rows('llm-requests.jsonl'), capture, suite[case_id], source))
    return {**result, 'infrastructure_attempts': record['infrastructure_attempts']}


def measurement_inputs(capture):
    evidence = [capture / name for name in ['report.json', 'configuration.json', 'suite.json', 'llm-requests.jsonl']]
    evidence.extend(p for p in [capture / 'store.db', capture / 'runtime-verified.json'] if p.exists())
    evidence.extend(sorted((capture / 'episode-evidence').glob('*.json')))
    evidence.extend(sorted((capture / 'conversation-preparation').glob('*.json')))
    evidence.extend(p for p in (capture / 'graded-project').rglob('*') if p.is_file())
    evidence.extend(p for p in (capture / 'graded-outside').rglob('*') if p.is_file())
    evidence.extend(sorted((capture / 'worker-scripts' / 'outcomes').glob('*.json')))
    evidence.extend(sorted((capture / 'projects').glob('*/evidence/*/tests/verify.jsonl')))
    return evidence


class GradingIncomplete(RuntimeError):
    def __init__(self, result):
        super().__init__('independent grading is incomplete; retained measurements can be resumed without model calls')
        self.result = result


def build(run, regrade=False, status_path=None):
    status_path = status_path or run / 'grading-status.json'
    source = run / 'source'
    plan = json.loads((run / 'plan.json').read_text())
    verify_application(source, plan['models'], plan=plan)
    records = json.loads((run / 'results.json').read_text())
    blocked_path = run / 'blocked.json'
    blocked = json.loads(blocked_path.read_text()) if blocked_path.exists() else {}
    if plan['benchmark']['manifest_sha256'] != file_hash(pathlib.Path(__file__).parent / 'benchmark.json'):
        raise ValueError('use the frozen report command from this run to reproduce its results')
    digest = verify_grader(source / 'scripts/coordinator-benchmark', pathlib.Path(__file__).parent,
                           plan['benchmark']['implementation_sha256'], regrade)
    if digest != GRADING_SHA:
        raise ValueError('grader changed during loading')
    if set(records) & set(blocked) or any(not entry.get('blocked') for entry in blocked.values()):
        raise ValueError('execution has inconsistent terminal dispositions')
    if set(records) | set(blocked) != {str(item['index']) for item in plan['episodes']}:
        raise ValueError('execution is incomplete; finish or resume its remaining episodes before publication')
    retain_grader(run, pathlib.Path(__file__).parent, digest)
    benchmark = {**plan['benchmark'], 'maturity': MANIFEST['maturity'], 'acceptance_revision': MANIFEST['acceptance_revision'], 'grading_sha256': digest, 'grading_image': IMAGE}
    common = {k: v for k, v in plan['models'][0]['configuration'].items() if k not in {'coordinator', 'configuration_sha256'}}
    if identity({'benchmark': plan['benchmark'], 'application_configuration': common}) != plan['comparison_id']:
        raise ValueError('execution comparison identity changed')
    suite = {c['id']: c for c in json.loads((source / MANIFEST['suite']).read_text())['cases']}
    benchmark['scoring'] = {'aggregation':'equal-family-scenario-fixture-mean', 'uncertainty':METHOD, 'release_repetitions':MANIFEST['release_repetitions'], 'pilot_repetitions':MANIFEST['exploration_repetitions'],
                            'tiers': {tier: {'release_repetitions': TIERS[tier]['release_repetitions'], 'reliability': 'pass-k-and-outcome-consistency'} for tier in TIERS},
                            'operations':[{key:op.get(key, 'gate') if key == 'tier' else op[key] for key in ['id','scenario','family','role','tier']} for op in MANIFEST['operations']]}
    seen, models, interruptions, unmeasured_count = set(), [], [], 0
    for model in plan['models']:
        cases = []
        for case_id, (title, _) in CASES.items():
            if not any(item['case']==case_id for item in plan['episodes']):
                continue
            trials = []
            for item in plan['episodes']:
                if item['model'] != model['id'] or item['case'] != case_id:
                    continue
                try:
                    trial = captured_trial(records.get(str(item['index'])), item, model['configuration'], suite, seen, source)
                except Exception as exc:
                    (run / f"grading-error-{item['index']:03}-{digest[:12]}.txt").write_text(str(exc) + '\n')
                    error = {'index': item['index'], 'model': item['model'], 'case': case_id,
                             'type': type(exc).__name__, 'detail': str(exc),
                             'retryable': isinstance(exc, (GradingUnavailable, subprocess.TimeoutExpired))}
                    if isinstance(exc, CheckIdentityError):
                        error['duplicate_checks'] = exc.duplicates
                    interruptions.append(error)
                    trial = unmeasured(case_id, 'Capture integrity or independent grading could not be verified.')
                unmeasured_count += trial['outcome'] == 'unmeasured'
                trials.append(trial)
            measured = sum(t['outcome'] in {'passed', 'failed'} for t in trials)
            passed = sum(t['outcome'] == 'passed' for t in trials)
            cases.append({'id': case_id, 'title': title, 'scenario': OPERATIONS[case_id]['scenario'], 'family': OPERATIONS[case_id]['family'], 'role': OPERATIONS[case_id]['role'], 'tier': OPERATIONS[case_id].get('tier', 'gate'), 'passed': passed, 'measured': measured,
                          'cost': cost_evidence.case_cost(run, [item for item in plan['episodes'] if item['model'] == model['id'] and item['case'] == case_id]),
                          'attempts': len(trials), 'interval_95': interval(passed, measured), 'trials': trials})
        score = overall_score(cases, plan['repetitions'], mode=plan['mode'])
        result = {'label': model['label'], 'configuration': model['configuration'], 'score': score, 'score_interval_95': estimate(cases, MANIFEST['operations'])['interval_95'] if score is not None else None, 'cases': cases,
                  'tiers': {tier: tier_result(cases, plan['repetitions'], plan['mode'], tier) for tier in TIERS}}
        preflight = run/'provider-preflight'/model['id']/'result.json'
        if preflight.exists():
            receipt = json.loads(preflight.read_text())
            if receipt['configuration_sha256'] != identity(model['configuration']):
                raise ValueError('provider observation differs from the measured configuration')
            if receipt['accepted']:
                result['provider_observation'] = {'at':receipt['completed_at'], 'source':'application_model_catalog',
                    'context_length':receipt.get('catalog_context_length')}
            elif any(case['measured'] for case in cases):
                raise ValueError('measured execution has a rejected provider admission')
        models.append(result)
    if grading_hash(pathlib.Path(__file__).parent) != digest:
        raise ValueError('grader changed during evaluation; reproduce with its retained source')
    result = {'version': 7, 'preflight_cost': cost_evidence.preflight_cost(run), 'mode':plan['mode'], 'execution_id': plan['run_id'], 'run_id': report_id(plan['run_id'], digest), 'tested_at': plan['started_at'],
              'lineage': plan.get('lineage'),
              'benchmark': benchmark, 'comparison_id': identity({'benchmark': benchmark, 'application_configuration': common}),
              'validation_status': 'calibrating', 'repetitions': plan['repetitions'],
              'ordering_seed': plan['seed'], 'models': models}
    if interruptions:
        retryable = all(error['retryable'] for error in interruptions)
        save(status_path, {'phase': 'blocked', 'errors': interruptions, 'retryable': retryable})
        raise GradingIncomplete(result)
    save(status_path, {'phase': 'completed_with_unmeasured' if unmeasured_count else 'completed',
                                     'unmeasured': unmeasured_count})
    return result


def write_report(run, regrade=False, destination=None, status_path=None):
    private_status = status_path is not None
    status_path = status_path or run / 'grading-status.json'
    with report_lease(run):
        def retain(result):
            save(destination, result)
            status = json.loads(status_path.read_text())
            save(status_path, {**status, 'report_id': result['run_id'],
                                              'report_sha256': file_hash(destination), 'report_path': str(destination.resolve())})

        try:
            mode = json.loads((run / 'plan.json').read_text())['mode']
            original = run / ('public.json' if mode == 'release' else 'preview.json')
            if regrade and (destination is None or destination.resolve() == original.resolve()):
                raise ValueError('regrading requires a separate output to preserve the original report')
            if destination is None:
                destination = (status_path.parent / 'report.json' if private_status else
                               run / ('public.json' if mode == 'release' else 'preview.json'))
            destination.unlink(missing_ok=True)
            save(status_path, {'phase': 'running', 'retryable': False})
            try:
                result = build(run, regrade, status_path)
            except GradingIncomplete as error:
                retain(error.result)
                raise
            retain(result)
            return result
        except GradingIncomplete:
            raise
        except BaseException as error:
            interrupted = isinstance(error, (KeyboardInterrupt, InterruptedError))
            save(status_path, {'phase': 'blocked', 'retryable': interrupted,
                              'failure': {'kind': 'harness',
                                          'code': 'grading_interrupted' if interrupted else 'grading_failed',
                                          'type': type(error).__name__, 'retryable': interrupted}})
            raise


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', required=True, type=pathlib.Path)
    parser.add_argument('--regrade', action='store_true', help='apply only the selected grader revision to a completed frozen execution')
    parser.add_argument('--out', type=pathlib.Path, help='destination for the public report')
    parser.add_argument('--status-out', type=pathlib.Path, help='receipt for this grading invocation')
    args = parser.parse_args()
    result = write_report(args.run.resolve(), args.regrade, args.out, args.status_out)
    print(('Publication' if result['mode']=='release' else 'Private preview')+' report written; no manual verdicts are required.')
