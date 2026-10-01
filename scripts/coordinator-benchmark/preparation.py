"""Retain preparation inputs for restart before paid calls."""
import json
import pathlib
import shutil
import subprocess
import tempfile
from contextlib import contextmanager
from progress import save, timestamp
from snapshot import freeze, read_source, file_hash, identity
from built_runtime import seal_application, verify_application, require_unadmitted, build_manifest, application_identity
from capture_storage import clone_file
from dispatch_graph import waves, dependency_check_id
from workflow_evidence import validate_receipts


@contextmanager
def stage(run, phase):
    receipt = {'phase': phase, 'state': 'running', 'started_at': timestamp()}
    path = run / 'preparation-status.json'
    save(path, receipt)
    try:
        yield
    except BaseException as exc:
        save(path, {**receipt, 'state': 'failed', 'finished_at': timestamp(),
                    'error_type': type(exc).__name__,
                    'returncode': exc.returncode if isinstance(exc, subprocess.CalledProcessError) else None})
        raise
    else:
        save(path, {**receipt, 'state': 'completed', 'finished_at': timestamp()})


def validate_bank(manifest, suite):
    if type(suite.get('task_allowance')) is not int or suite['task_allowance'] < 1:
        raise ValueError('the benchmark requires a positive coordinator task allowance')
    operations = manifest['operations']
    ids = [op['id'] for op in operations]
    cases = [case['id'] for case in suite['cases']]
    if len(set(ids)) != len(ids) or len(set(cases)) != len(cases) or set(ids) != set(cases):
        raise ValueError('suite and fixture catalog must have the same unique identities')
    by_id={case['id']:case for case in suite['cases']}
    for operation in operations:
        workflow = operation.get('workflow')
        spec = by_id[operation['id']]
        if bool(workflow) != bool(spec.get('workflow_id')) or (workflow and operation.get('tier')!='workflow'):
            raise ValueError('custom workflow contract and fixture must agree')
        if workflow and (workflow['id'] != spec['workflow_id'] or workflow['version'] != spec.get('workflow_version') or not workflow['receipts']):
            raise ValueError('custom workflows require an identity and phase receipts')
        if workflow:
            validate_receipts(workflow)
        if operation.get('tier')=='orchestration':
            if (by_id[operation['id']].get('setup') or {}).get('policy')!='scripted':
                raise ValueError('every tier-two operation must keep workers scripted')
            dispatches = by_id[operation['id']]['setup'].get('dispatches', [])
            waves(dispatches, operation.get('dispatch', {}).get('dependencies', {}))
            decisions = operation.get('decisions', {})
            declared = {d['label']: d for d in dispatches if d['stages'][0]['kind'] == 'needs_decision'}
            if set(decisions) != set(declared):
                raise ValueError('each suspended assignment requires one decision contract')
            if any(value['option'] not in declared[label]['stages'][0]['outcomes'] for label, value in decisions.items()):
                raise ValueError('a decision contract names an unavailable option')
    tiers = manifest.get('tiers') or {}
    scenarios = {}
    for op in operations:
        if op['role'] != 'scored': continue
        tier = op.get('tier', 'gate')
        if tier not in tiers:
            raise ValueError('scored operation names an undeclared tier')
        scenario = scenarios.setdefault((tier, op['scenario']), {'family': op['family'], 'fixtures': []})
        if scenario['family'] != op['family']:
            raise ValueError('a scenario cannot belong to different families')
        scenario['fixtures'].append(op['id'])
    if not scenarios:
        raise ValueError('the benchmark must declare scored scenarios')
    for tier, declared in tiers.items():
        families = {op['family'] for op in operations if op['role'] == 'scored' and op.get('tier', 'gate') == tier}
        if len(families) != declared['families']:
            raise ValueError(f'tier {tier} declares {declared["families"]} families but names {len(families)}')


def validate_runtime(run, source, cases=None):
    manifest=json.loads((source/'scripts/coordinator-benchmark/benchmark.json').read_text())
    suite=json.loads((source/manifest['suite']).read_text())
    requested = set(cases) if cases is not None else {o['id'] for o in manifest['operations']}
    if not requested or not requested <= {o['id'] for o in manifest['operations']}:
        raise ValueError('preflight requires a declared operation selection')
    directory=pathlib.Path(tempfile.mkdtemp(prefix='preflight-',dir=run))
    with (directory/'smoke.log').open('w') as log:
        subprocess.run(['./task','eval:tool-usage','BENCHMARK=smoke','--',
                        '--out',str(directory/'application'),'--oracles','--cases',','.join(sorted(requested))],cwd=source,
                       stdout=log,stderr=subprocess.STDOUT,check=True)
    receipt=directory/'application/validation.json'
    validation=json.loads(receipt.read_text())
    manifest={**manifest,'operations':[o for o in manifest['operations'] if o['id'] in requested]}
    suite={**suite,'cases':[c for c in suite['cases'] if c['id'] in requested]}
    expected_contract = {'profile':'development-harness', 'application_version':(source/'VERSION').read_text().strip(),
                         'contract':json.loads((source/'lycaon/internal/harnessfixture/contract.json').read_text())}
    if validation.get('contract') != expected_contract or set(validation.get('cases', [])) != requested:
        raise ValueError('preflight application contract or operation selection differs')
    operations={o['id'] for o in manifest['operations']}
    expected={c['id'] for c in suite['cases'] if c['id'] in operations and (c.get('setup') or c.get('prelude') or c.get('sandbox') or c.get('workflow_id'))}
    variants=json.loads((source/'scripts/coordinator-benchmark/fixture-contracts.json').read_text())
    variants={k:v for k,v in variants.items() if k in requested}
    validate_worker_controls(validation.get('trajectories', []), suite)
    required_trajectories={o['id'] for o in manifest['operations'] if o.get('tier') in {'orchestration','workflow'}}
    intended={t['id'] for t in validation.get('trajectories',[]) if t['kind']=='intended'}
    if not required_trajectories <= intended:
        raise ValueError('application preflight omitted intended trajectories: '+','.join(sorted(required_trajectories-intended)))
    shortcut_required={c['id'] for c in suite['cases'] if (c.get('setup') or {}).get('dispatches')}
    shortcut_required.update(o['id'] for o in manifest['operations'] if o.get('workflow'))
    shortcut_required.update({'capability-background-service','capability-write-root','denied-no-fallback'} & requested)
    shortcuts={t['id'] for t in validation.get('trajectories',[]) if t['kind']=='shortcut' and t['failed']}
    if not shortcut_required <= shortcuts:
        raise ValueError('application preflight omitted rejected shortcuts: '+','.join(sorted(shortcut_required-shortcuts)))
    workflow_controls=json.loads((source/'scripts/coordinator-benchmark/fixture-controls.json').read_text())
    for operation in manifest['operations']:
        if operation.get('workflow'):
            required_controls={c['id'] for c in workflow_controls[operation['id']]['workflow']['negative']}
            observed={t.get('control') for t in validation.get('trajectories', [])
                      if t['id']==operation['id'] and t['kind']=='shortcut' and t['failed']}
            if not required_controls <= observed:
                raise ValueError('application preflight omitted workflow negative controls')
            alternatives = {c['id'] for c in workflow_controls[operation['id']]['workflow']['alternatives']}
            passed = {t.get('control') for t in validation.get('trajectories', [])
                      if t['id']==operation['id'] and t['kind']=='intended' and not t['failed']}
            if not alternatives <= passed:
                raise ValueError('application preflight omitted alternative workflow orderings')
    for operation in manifest['operations']:
        required = {dependency_check_id(p,label)
                    for label, prerequisites in operation.get('dispatch', {}).get('dependencies', {}).items() for p in prerequisites}
        failed = {name for t in validation.get('trajectories', []) if t['id'] == operation['id']
                  and t.get('control') == 'dependent-before-prerequisite' for name in t['failed']}
        if not required <= failed:
            raise ValueError('application preflight omitted dependent dispatch failure controls')
    decisions={o['id'] for o in manifest['operations'] if o.get('decisions')}
    validate_decision_controls(validation.get('trajectories', []), suite, decisions)
    if 'capability-background-service' in requested:
        validate_service_controls(validation.get('trajectories', []))
    write_probes=validation.get('write_root_probes',[])
    if ({probe['id'] for probe in write_probes}!={c['id'] for c in suite['cases'] if c.get('sandbox')=='write_root'}
        or any(probe['write_root'].get('read_inspection')!='passed' or probe['write_root'].get('ungranted_write')!='rejected' for probe in write_probes)):
        raise ValueError('application preflight did not separate output read and write authority')
    validate_baselines(validation, expected)
    if (set(validation['fixtures'])!=expected or validation['application_boundaries']!=len(expected)
        or validation.get('candidate_execution_bound') is not True
        or validation['isolated_outcome_controls']!=6+len(variants) or validation['receipt_checks']!='passed'
        or validation.get('trajectory_controls')!='passed'
        or set(validation['denied_service_probes'])!={c['id'] for c in suite['cases'] if c.get('sandbox')=='deny_loopback'}
        or not validation['scanner_health'].get('assessment_id')):
        raise ValueError('application preflight did not validate every required boundary')
    return {'receipt':str(receipt.relative_to(run)),'sha256':file_hash(receipt)}


def validate_baselines(validation, expected):
    admissions = validation['admissions']
    projects = [a['project_id'] for a in admissions]
    baselines = [b['project_id'] for b in validation['baseline_checks']]
    if (len(set(projects)) != len(projects) or len(set(baselines)) != len(baselines)
            or set(projects) != set(baselines)
            or {a['case'] for a in admissions} != expected | {'scanner-runtime'}
            or {a['project_id'] for a in admissions if a['case']=='scanner-runtime'} != {validation['scanner_health']['project_id']}):
        raise ValueError('every admitted preflight project requires its own scanner baseline')


def validate_worker_controls(trajectories, suite):
    for case in suite['cases']:
        overlays = (case.get('setup') or {}).get('overlays') or []
        if not overlays:
            continue
        controls = [t for t in trajectories if t['id'] == case['id']]
        intended = any(t['kind'] == 'intended' and not t['failed'] for t in controls)
        failures = {'promoted-prepared-worker-'+seed['label'] for seed in overlays}
        unpromoted = any(t.get('control') == 'prepared-workers-unpromoted' and t['kind'] == 'shortcut'
                         and failures <= set(t['failed']) for t in controls)
        if not intended and not unpromoted:
            raise ValueError('application preflight omitted worker grading controls: ' + case['id'])


def validate_decision_controls(trajectories, suite, required):
    cases = {case['id']:case for case in suite['cases']}
    for case_id in required:
        labels = {d['label'] for d in cases[case_id]['setup']['dispatches'] if d['stages'][0]['kind']=='needs_decision'}
        for label in labels:
            failures = {'planned-leg-settled-'+label, 'decision-answered-'+label}
            if not any(t['id'] == case_id and t.get('control') == 'unanswered-worker-decision' and t.get('worker')==label
                       and t['kind'] == 'shortcut' and failures <= set(t['failed']) for t in trajectories):
                raise ValueError('application preflight omitted unanswered decision control: '+case_id+'/'+label)
        if not any(t['id']==case_id and t.get('control')=='swapped-worker-decisions' and t['kind']=='shortcut'
                   and {'decision-answered-'+label for label in labels} <= set(t['failed']) for t in trajectories):
            raise ValueError('application preflight omitted decision routing control: '+case_id)


def validate_service_controls(trajectories):
    selected = [t for t in trajectories if t['id'] == 'capability-background-service']
    successes = {t.get('control') for t in selected if t['kind'] == 'intended' and not t['failed']}
    forgery = any(t.get('control') == 'fabricated-receipt' and t['kind'] == 'shortcut'
                  and 'supplied-client-succeeded' in t['failed'] for t in selected)
    if not {'intended', 'client-after-delay', 'client-after-timeout'} <= successes or not forgery:
        raise ValueError('application preflight omitted service outcome controls')


def inputs(args, implementation):
    path = args.out / 'preparation.json'
    if path.exists():
        saved = json.loads(path.read_text())
        if saved['implementation'] != implementation:
            raise ValueError('preparation requires its original runner implementation')
        return saved
    roster = json.loads(args.roster.read_text())
    manifest = json.loads(pathlib.Path(__file__).with_name('benchmark.json').read_text())
    mode = getattr(args, 'mode', None) or 'exploration'
    roster['repetitions'] = manifest['release_repetitions'] if mode == 'release' else manifest['exploration_repetitions']
    if args.repetitions is not None:
        roster['repetitions'] = args.repetitions
    if args.models:
        requested = set(args.models.split(','))
        if not requested <= {m['id'] for m in roster['models']}:
            raise ValueError('unknown model selection')
        roster['models'] = [m for m in roster['models'] if m['id'] in requested]
    saved = {'roster': roster, 'cases': args.cases, 'cadence': args.cadence, 'implementation': implementation,
             'concurrency': args.concurrency or 5, 'cloud_concurrency': args.cloud_concurrency or 2,
             'provider_limit': args.provider_limit, 'mode': mode, 'tier': getattr(args, 'tier', None) or 'all'}
    save(path, saved)
    return saved


def source_snapshot(run, selection=None):
    source = run / 'source'
    path = run / 'selection.json'
    if path.exists():
        saved = json.loads(path.read_text())
        if selection is not None and saved != selection:
            raise ValueError('application or benchmark selection changed during preparation')
        selection = saved
    elif selection is not None:
        save(path, selection)
    else:
        raise ValueError('preparation requires an explicit application selection')
    if not (source / 'source.json').exists():
        require_unadmitted(source)
        if source.exists():
            shutil.rmtree(source)
        return freeze(source, selection)
    return read_source(source)


def build_application(run, environment):
    source = run / 'source'
    if (source / 'application.json').exists():
        return verify_application(source)
    require_unadmitted(source)
    selection = json.loads((run / 'selection.json').read_text())
    retained = selection.get('retained_source')
    targets = ['gitengine:fetch', 'build:lycaon-dev', 'eval:tool-usage']
    if retained:
        retained = pathlib.Path(retained)
        previous = verify_application(retained)
        current = read_source(source)
        if any(previous[key] != current[key] for key in ('source_sha256', 'revision', 'version', 'target')):
            raise ValueError('repair changed the selected application')
        runtime = pathlib.Path('lycaon-den/src-tauri/engine-root')
        if (source / runtime).exists():
            shutil.rmtree(source / runtime)
        shutil.copytree(retained / runtime, source / runtime, symlinks=True, copy_function=clone_file)
        prior_evaluation = json.loads((retained / 'evaluation-source.json').read_text())
        next_evaluation = json.loads((source / 'evaluation-source.json').read_text())
        if prior_evaluation['harness_sha256'] == next_evaluation['harness_sha256']:
            (source / '.bin').mkdir(exist_ok=True)
            for name in ('lycaon-dev', 'pw-logs'):
                destination = source / '.bin' / name
                destination.unlink(missing_ok=True)
                clone_file(retained / '.bin' / name, destination)
            targets = ['eval:tool-usage']
        else:
            targets = ['build:lycaon-dev', 'eval:tool-usage']
    # The sealed application keeps its executables inside the prepared source.
    build_environment = {**environment, 'GOWORK': 'off', 'GOFLAGS': '-buildvcs=false',
                         'PW_BUILD_DIR': str(source / '.bin')}
    with stage(run, 'build'), (run / 'build.log').open('w') as log:
        subprocess.run(['./task', *targets, 'BUILD_ONLY=true'],
                       cwd=source, env=build_environment, stdout=log, stderr=subprocess.STDOUT, check=True)
        if not retained:
            schemas = source / 'lycaon-den/src-tauri/engine-root/schemas'
            if schemas.exists():
                shutil.rmtree(schemas)
            shutil.copytree(source / 'schemas', schemas)
        if retained and application_identity(read_source(source), build_manifest(source)) != previous:
            raise ValueError('repair changed the retained application runtime')
        return seal_application(source)
