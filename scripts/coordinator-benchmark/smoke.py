"""Exercise preparation against an owned application, using only manual completions."""
import argparse
import contextlib
import json
import pathlib
import secrets
import sys
import shutil
import socket
import sqlite3
import subprocess
import time
import tempfile
import urllib.error
import urllib.request
import uuid
import unittest
import smoke_capabilities
import smoke_closeout
import smoke_boundaries
import smoke_workers
from runtime_checks import project_scans, project_baseline, requested_full_pass
from progress import save
from snapshot import ROOT
sys.path.append(str(ROOT / 'scripts'))
from artifact_paths import artifact_root, build_dir
from focused_evidence import preparation_checks
from application_ledger import checkpoint_closed_store
from application_http import open_request
from application_environment import application_environment
from dispatch_graph import dependency_check_id


class Application:
    def __init__(self, directory, source_config=None, rate_state=None):
        self.directory=directory; directory.mkdir(parents=True,exist_ok=False)
        self.config=directory/'config'; self.config.mkdir()
        self.manual = source_config is None
        if source_config is not None:
            for name in ['providers.local.yaml','model-policy.yaml','credential-vault.age','.credential-vault-development-identity']:
                if (source_config/name).exists():
                    shutil.copyfile(source_config/name,self.config/name)
                    (self.config/name).chmod(0o600)
            self.config.chmod(0o700)
        self.projects=[]
        self.admissions=[]
        suite=json.loads((ROOT/'lycaon/test/fixtures/eval/coordinator-benchmark.json').read_text())
        self.task_allowance=suite['task_allowance']
        self.approval_guidance=suite['unattended']['approval_guidance']
        self.baseline_checks=[]
        self.flows=[]
        self.token=secrets.token_hex(24)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1',0)); self.port=sock.getsockname()[1]
        self.url=f'http://127.0.0.1:{self.port}'
        self.log=(directory/'sidecar.log').open('w')
        env=application_environment({'LYCAON_CONFIG_DIR':str(self.config),'LYCAON_CONFIG_ROOT':str(ROOT/'lycaon'),
             'LYCAON_ENGINE_ROOT':str(ROOT/'lycaon-den/src-tauri/engine-root'),
             'LYCAON_ADDR':f'127.0.0.1:{self.port}','LYCAON_API_TOKEN':self.token,
             'LYCAON_DEV':'1','LYCAON_HARNESS':'1','LYCAON_LOG_LEVEL':'debug',
             'LYCAON_LLM_MOCK':'1' if self.manual else '0','LYCAON_LLM_MANUAL':'1' if self.manual else '0'})
        if rate_state is not None: env['LYCAON_LLM_RATE_STATE_DIR']=str(rate_state)
        self.process=subprocess.Popen([str(build_dir(ROOT)/'lycaon-dev'),'serve','--db',str(directory/'store.db')],
                                      cwd=ROOT,env=env,stdout=self.log,stderr=subprocess.STDOUT)
        save(directory/'ownership.json',{'pid':self.process.pid,'port':self.port})

    def request(self, path, body=None, missing=False, method=None):
        request=urllib.request.Request(self.url+path,data=json.dumps(body).encode() if body is not None else None,
                    headers={'Authorization':'Bearer '+self.token,'Content-Type':'application/json'}, method=method)
        try:
            with open_request(request) as response:
                return json.load(response)
        except urllib.error.HTTPError as exc:
            if missing and exc.code==404: return None
            raise

    def wait_for(self, observe, phase, session=None, timeout=180):
        """Poll for a value and report the blocked phase on timeout."""
        deadline=time.monotonic()+timeout
        while True:
            if self.process.poll() is not None:
                raise RuntimeError('owned application exited during '+phase)
            if time.monotonic()>deadline:
                waiting=self.request('/harness/llm/pending') if self.manual else {}
                raise RuntimeError(f'{phase}: no progress within {timeout}s; pending model request: '
                                   +json.dumps({k:waiting.get(k) for k in ('pending','id','session_id')}))
            if session:
                with contextlib.closing(sqlite3.connect(self.directory/'store.db')) as db:
                    failed=db.execute("SELECT status, error_code FROM prompt_submissions WHERE session_id=? AND status IN ('failed','interrupted','canceled')",(session,)).fetchone()
                    if failed: raise RuntimeError(f'{phase}: submission {failed[0]} ({failed[1]})')
            result=observe()
            if result: return result
            time.sleep(.25)

    def submission_settled(self, session, submission):
        observation=self.request(f'/harness/execution/{session}/{submission}')
        if observation['session_id']!=session or observation['submission_id']!=submission:
            raise ValueError('execution observation does not match its admission')
        return observation['settled']

    def ready(self):
        def healthy():
            try:
                self.request('/health'); return True
            except urllib.error.HTTPError as error:
                if error.code in {408,429,500,502,503,504}: return False
                raise
            except OSError: return False
        self.wait_for(healthy,'startup')
        self.contract = self.request('/harness/contract')
        expected = json.loads((ROOT/'lycaon/internal/harnessfixture/contract.json').read_text())
        if (self.contract.get('contract') != expected or self.contract.get('profile') != 'development-harness'
            or self.contract.get('application_version') != (ROOT/'VERSION').read_text().strip()):
            raise RuntimeError('application does not implement the selected evaluation contract')
        if self.manual: self.request('/harness/llm/auto',{'enabled':False})

    def close(self, cleanup=True):
        if self.process.poll() is None:
            self.process.terminate()
            try: self.process.wait(timeout=30)
            except subprocess.TimeoutExpired: self.process.kill(); self.process.wait()
        self.log.close()
        if cleanup: self.cleanup()

    def cleanup(self):
        for root in self.projects: shutil.rmtree(root)

    def session(self, spec):
        project=pathlib.Path(tempfile.mkdtemp(prefix='paintedwolf-preparation-'))/spec['id']
        self.projects.append(project.parent)
        shutil.copytree(ROOT/'lycaon/test/fixtures/eval'/spec['project'],project)
        attached=self.request('/v1/projects',{'roots':[{'path':str(project)}]})
        if spec.get('workflow_id'):
            self.request('/v1/projects/'+attached['id']+'/trust',
                         {'enabled':{'project_settings':True,'prompt_overrides':True},'mark_seen':True},method='PATCH')
            available=self.request('/v1/workflows?project_id='+attached['id'])['workflows']
            if not any(w['id']==spec['workflow_id'] for w in available):
                raise RuntimeError('project workflow was not admitted: '+spec['workflow_id'])
        session=self.request('/v1/sessions',{'project_id':attached['id'],'posture':'build'})
        self.admissions.append({'case':spec['id'],'session_id':session['id'],'project_id':attached['id']})
        self.request('/harness/model-limit',{'session_id':session['id'],'limit':self.task_allowance})
        def prepared():
            state=self.request('/v1/sessions/'+session['id'])
            if state['status']=='preparing': return False
            if state['status']!='idle': raise RuntimeError('session preparation ended in '+state['status'])
            return True
        self.wait_for(prepared,'session preparation',session['id'])
        baseline=self.wait_for(lambda:project_baseline(self.request('/v1/projects/'+attached['id']+'/security')),
                               'scanner baseline',session['id'])
        self.baseline_checks.append(baseline)
        return session['id'], project

    def verify_scanner_runtime(self, spec):
        session,_=self.session({**spec,'id':'scanner-runtime'})
        state=self.request('/v1/sessions/'+session)
        response=self.request('/v1/projects/'+state['project_id']+'/scans',{'kind':'full'})
        save(self.directory/'scanner-request.json',response)
        requested=requested_full_pass(response,state['project_id'])
        def observe():
            overview=self.request('/v1/projects/'+state['project_id']+'/security')
            save(self.directory/'scanner-status.json',overview)
            return project_scans(overview,requested)
        return self.wait_for(observe,'scanner runtime health',session,timeout=900)

    def verify_prelude(self,spec):
        session,project=self.session(spec)
        prelude={'operation_id':str(uuid.uuid4()),'steps':spec['prelude']}
        if spec.get('prelude_final'): prelude['final']=spec['prelude_final']
        self.request('/harness/preparation',{'session_id':session,'prelude':prelude})
        accepted=self.request(f'/v1/sessions/{session}/prompts',{'operation_id':str(uuid.uuid4()),'text':spec['prompt']})
        if prelude.get('final'):
            self.wait_for(lambda:self.submission_settled(session,accepted['operation_id']),
                          'initial implement completion',session)
            self.request(f'/v1/sessions/{session}/prompts',{'operation_id':str(uuid.uuid4()),'text':spec['follow_ups'][0]['prompt']})
        receipt=self.wait_for(lambda:self.request('/harness/preparation/'+session,missing=True),
                              'candidate entry',session)
        self.request(f'/v1/sessions/{session}/abort',{})
        return {'id':spec['id'],'entry':receipt}

    def prepare_workers(self,spec):
        session,project=self.session(spec)
        if spec.get('prelude'):
            self.request('/harness/preparation', {'session_id':session,
                'prelude':{'operation_id':str(uuid.uuid4()),'steps':spec['prelude']}})
        result=self.request('/harness/overlays',{'session_id':session,'setup':spec['setup']})
        if len(result.get('overlays') or [])!=len(spec['setup'].get('overlays') or []): raise RuntimeError('prepared overlay count differs')
        for expected,actual in zip(spec['setup'].get('overlays') or [],result.get('overlays') or []):
            validation=actual.get('verification')
            verdict=expected['verification_verdict']
            if not validation or validation['Verdict']!=verdict or not validation['CheckID']:
                raise RuntimeError('worker preparation did not execute the expected verification')
            if actual.get('result_status') != expected.get('result_status','complete'):
                raise RuntimeError('prepared result status differs from its declared boundary')
        return session,project,result

    def verify_workers(self,spec,operation):
        """Exercise successful worker flows and rejected shortcuts."""
        setup=spec['setup']
        scripted=setup.get('dispatches') or any(o.get('script') for o in setup.get('overlays') or [])
        session,project,result=self.prepare_workers(spec)
        observed={'id':spec['id'],'prepared':result}
        flow={'id':spec['id'],'kind':'intended','session_id':session,'project_dir':str(project),'prepared':result}
        if spec.get('prelude') or scripted or (operation.get('tier')=='orchestration' and setup.get('overlays')):
            self.request(f'/v1/sessions/{session}/prompts',{'operation_id':str(uuid.uuid4()),'text':spec['prompt']})
            if spec.get('prelude'):
                observed['entry']=flow['entry']=self.wait_for(lambda:self.request('/harness/preparation/'+session,missing=True),
                                                             'worker handoff entry',session)
            existing=next((step['args']['content'] for step in reversed(spec.get('prelude') or []) if step.get('tool')=='update_progress'),None)
            continuation_progress={o['label']:'done' for o in setup.get('overlays') or [] if o.get('script')}
            if continuation_progress and not existing:
                smoke_workers.reconcile_progress(self,session,{label:'pending' for label in continuation_progress})
            for seed, overlay in zip(result.get('overlays') or [],setup.get('overlays') or []):
                if script := overlay.get('script'):
                    observed['resumed_job']=smoke_workers.continue_worker(self,session,project,seed,script)
                else:
                    smoke_workers.promote_prepared(self,session,seed,overlay,spec,operation)
            if setup.get('dispatches'):
                observed['dispatches'],flow['automatic_responses']=smoke_workers.intended_dispatches(self,session,project,setup['dispatches'],existing,operation)
            if operation.get('verification'):
                smoke_workers.invoke(self,session,'command',{'command':'python3 -B -m unittest discover','verification':True})
            if operation.get('progress'):
                smoke_workers.reconcile_progress(self,session,operation['progress'])
            elif continuation_progress and not existing:
                smoke_workers.reconcile_progress(self,session,continuation_progress)
            findings=[f for leg in observed.get('dispatches') or [] for f in leg.get('findings') or []]
            closeout = operation
            if operation.get('closeout_paths'):
                for path in operation['closeout_paths']:
                    smoke_workers.invoke(self,session,'read',{'path':path})
                findings += [{'path':path,'line':1,'excerpt':(project/path).read_text().splitlines()[0]} for path in operation['closeout_paths']]
                closeout = {**operation,'cited_findings':findings}
            smoke_workers.finish(self,session,smoke_workers.closeout(closeout,'The requested work is integrated.',findings))
            self.flows.append(flow)
        elif setup.get('overlays'):
            self.flows.append({**flow, 'kind': 'shortcut', 'control': 'prepared-workers-unpromoted',
                               'must_fail': ['promoted-prepared-worker-'+seed['label'] for seed in setup['overlays']]})
        if setup.get('dispatches'):
            session,project,result=self.prepare_workers(spec)
            self.request(f'/v1/sessions/{session}/prompts',{'operation_id':str(uuid.uuid4()),'text':spec['prompt']})
            entry=None
            if spec.get('prelude'):
                entry=self.wait_for(lambda:self.request('/harness/preparation/'+session,missing=True),'shortcut handoff entry',session)
            observed['shortcut']=smoke_workers.shortcut_dispatches(self,session,project,setup['dispatches'],operation)
            # The shortcut has no worker findings to cite.
            smoke_workers.finish(self,session,'The requested work is integrated.')
            self.flows.append({'id':spec['id'],'kind':'shortcut','session_id':session,'project_dir':str(project),'prepared':result,'entry':entry,
                               'must_fail':['no-off-plan-dispatch']+['planned-leg-settled-'+d['label'] for d in setup['dispatches']]})
        for label, prerequisites in operation.get('dispatch',{}).get('dependencies', {}).items():
            session,project,result=self.prepare_workers(spec)
            self.request(f'/v1/sessions/{session}/prompts',{'operation_id':str(uuid.uuid4()),'text':spec['prompt']})
            progress='## Progress\n'+''.join(f"- [ ] {d['label']}\n" for d in setup['dispatches'])
            smoke_workers.invoke(self,session,'update_progress',{'content':progress})
            dependent=next(d for d in setup['dispatches'] if d['label']==label)
            job=smoke_workers.job_id(smoke_workers.invoke(self,session,'task',smoke_workers.dispatch_call(dependent)['args']))
            smoke_workers.settled(self,session,job,{'failed'} if dependent['mode']=='write' else {'complete'})
            smoke_workers.invoke(self,session,'update_progress',{'content':progress.replace('- [ ] ','- [~] ')})
            smoke_workers.finish(self,session,'The worker could not complete its assignment.')
            self.flows.append({'id':spec['id'],'kind':'shortcut','control':'dependent-before-prerequisite',
                               'session_id':session,'project_dir':str(project),'prepared':result,
                               'must_fail':[dependency_check_id(p,label) for p in prerequisites]})
        if operation.get('decisions'):
            for dispatch in setup['dispatches']:
                if dispatch['label'] in operation['decisions']:
                    smoke_workers.unresolved_decision(self, spec, dispatch)
            smoke_workers.swapped_decisions(self, spec, operation)
        return observed


def verify_case(application, spec, operation):
    if spec.get('workflow_id'):
        import smoke_workflows
        return smoke_workflows.verify(application, spec, operation)
    if spec['id'] in smoke_closeout.CASES:
        return smoke_closeout.verify(application, spec, operation)
    if spec['id'] in smoke_capabilities.CASES:
        return smoke_capabilities.verify(application, spec)
    if spec.get('sandbox'):
        return smoke_boundaries.verify(application, spec)
    if spec.get('setup'):
        return application.verify_workers(spec, operation)
    return application.verify_prelude(spec)


def run_outcome_controls(requested):
    """Run outcome and fixture-contract controls in the pinned container runtime; return their count."""
    import focused_outcomes
    import test_focused_outcomes
    import test_fixture_contracts
    test_focused_outcomes.local_execute=focused_outcomes.execute
    test_fixture_contracts.local_execute=focused_outcomes.execute
    controls_root=artifact_root(ROOT)/'coordinator-oracle-controls'
    controls_root.mkdir(parents=True,exist_ok=True)
    previous_tempdir=tempfile.tempdir
    tempfile.tempdir=str(controls_root)
    controls=unittest.defaultTestLoader.loadTestsFromTestCase(test_focused_outcomes.FocusedOutcomeControls)
    controls.addTests(test_fixture_contracts.FixtureControls('test_'+case.replace('-','_'))
                      for case in sorted(requested & set(test_fixture_contracts.CONTRACTS)))
    try:
        if not unittest.TextTestRunner(verbosity=2).run(controls).wasSuccessful():
            raise RuntimeError('isolated outcome controls failed')
        with tempfile.TemporaryDirectory(prefix='deadline-') as temporary:
            if focused_outcomes.execute(pathlib.Path(temporary), 'while True: pass') is not None:
                raise RuntimeError('nonterminating candidate produced an observation')
    finally: tempfile.tempdir=previous_tempdir
    return controls.countTestCases()


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out',type=pathlib.Path,required=True)
    parser.add_argument('--oracles',action='store_true',help='also run outcome controls in the pinned Docker runtime')
    parser.add_argument('--cases',help='exact fixture IDs; defaults to every scored prepared boundary')
    args=parser.parse_args()
    suite=json.loads((ROOT/'lycaon/test/fixtures/eval/coordinator-benchmark.json').read_text())
    manifest=json.loads(pathlib.Path(__file__).with_name('benchmark.json').read_text())
    operations={o['id']:o for o in manifest['operations']}
    declared={o['id'] for o in manifest['operations']}
    required={c['id'] for c in suite['cases'] if c['id'] in declared and (c.get('setup') or c.get('prelude') or c.get('sandbox') or c.get('workflow_id'))}
    requested=set(args.cases.split(',')) if args.cases is not None else declared
    if not requested or not requested <= declared: raise ValueError('unknown fixture selection')
    selected=required & requested
    control_count=run_outcome_controls(requested) if args.oracles else 0
    application=Application(args.out.resolve())
    results=[]
    try:
        application.ready()
        scanner_health=application.verify_scanner_runtime(next(c for c in suite['cases'] if c['id']=='current-verification'))
        for spec in suite['cases']:
            if spec['id'] not in selected: continue
            print('Checking '+spec['id'],flush=True)
            result=verify_case(application,spec,operations[spec['id']])
            results.append(result);save(application.directory/'results.json',results)
            print('Passed '+spec['id'],flush=True)
    finally: application.close(cleanup=sys.exc_info()[0] is not None)
    import smoke_grading
    try:
        flows=smoke_grading.retain_flows(application.directory,application.flows)
        save(application.directory/'flows.json',flows)
        save(application.directory/'ledger-checkpoint.json',
             checkpoint_closed_store(application.directory/'store.db', application.process))
        trajectories=smoke_grading.verify_flows(application.directory,flows,suite)
    finally:
        application.cleanup()
    save(application.directory/'trajectories.json',trajectories)
    specs={case['id']:case for case in suite['cases']}
    for result in results:
        if result.get('entry'):
            from episode_evidence import export
            export(application.directory, {'session_id':result['entry']['session_id']},ROOT)
            preparation_checks(application.directory,
                {'session_id':result['entry']['session_id'],'preparation':result['entry']},specs[result['id']])
    save(application.directory/'validation.json', {'contract': application.contract, 'cases': sorted(requested), 'application_boundaries':len(results), 'admissions':application.admissions, 'baseline_checks':application.baseline_checks, 'scanner_health':scanner_health,
         'denied_service_probes':[r['id'] for r in results if r.get('denied_service_probe')],
         'write_root_probes':[r for r in results if r.get('write_root')],
         'candidate_execution_bound':bool(args.oracles),
         'isolated_outcome_controls':control_count, 'fixtures':sorted(selected), 'receipt_checks':'passed',
         'trajectory_controls':'passed','trajectories':trajectories})


if __name__=='__main__': main()
