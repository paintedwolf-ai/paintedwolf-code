import json
import pathlib
import tempfile
import unittest
import urllib.error

from provider_checks import check_model, disposition, load_plan, run_checks, select_references, source_fingerprint


def receipt(ok=True, kind='compatibility'):
    result={'ok':ok,'reasoning_policy':{'intent':'medium','style':'effort_levels','always_on':False,'default_on':False},
            'driver_sha256':'d'*64,'request_policy_sha256':'e'*64,'check_revision':1,'application_version':'0.1.0',
            'stages':[{'stage':name,'accepted':ok,'usage':{'prompt_tokens':10,'completion_tokens':5}}
                      for name in ['tool_call','tool_result_replay']]}
    if not ok:result['failure']={'kind':kind,'code':'fixture_failure','retryable':False}
    return result


def reference_plan():
    return {'schema':1,'repetitions':1,'providers':[{'kind':'together','models':[{'id':'reference','covers':['tool replay']}]}]}


def provider():
    return {'id':'provider','kind':'together','base_url':'https://fixture.invalid','configured':True,
            'models':[{'id':'reference'},{'id':'unselected'}]}


class ProviderCheckTests(unittest.TestCase):
    def test_only_reference_models_are_selected(self):
        selected=select_references([provider()],reference_plan(),'')
        self.assertEqual(selected[0][1],[{'id':'reference','covers':['tool replay']}])
        empty=provider();empty['models']=[]
        with self.assertRaises(ValueError):select_references([empty],reference_plan(),'')

    def test_plan_rejects_sweeps_local_runtimes_and_duplicate_models(self):
        for mutate in [lambda p:p['providers'][0].update(kind='ollama'),
                       lambda p:p['providers'][0]['models'].extend([{'id':'other','covers':['x']},{'id':'third','covers':['x']}]),
                       lambda p:p['providers'][0]['models'].append({'id':'reference','covers':['x']})]:
            plan=reference_plan();mutate(plan)
            with tempfile.TemporaryDirectory() as directory:
                path=pathlib.Path(directory)/'plan.json';path.write_text(json.dumps(plan))
                with self.assertRaises(ValueError):load_plan(path)

    def test_checked_in_plan_is_bounded_and_valid(self):
        load_plan(pathlib.Path(__file__).with_name('provider-checks.json'))

    def test_transport_retry_and_resume_preserve_each_attempt(self):
        class Application:
            calls=0
            def request(self,path,body):
                self.calls+=1
                if self.calls==1:return {'ok':False,'failure':{'kind':'provider','code':'provider_rate_limited','retryable':True}}
                return receipt()
        app=Application();waits=[]
        with tempfile.TemporaryDirectory() as directory:
            out=pathlib.Path(directory)
            first=check_model(app,provider(),'reference',out,2,3,wait=waits.append)
            second=check_model(app,provider(),'reference',out,2,3,wait=waits.append)
            self.assertEqual(first,second);self.assertEqual(app.calls,3)
            self.assertEqual(waits,[5]);self.assertEqual(len(list(out.rglob('*attempt-*.json'))),3)

    def test_permanent_http_failure_does_not_retry(self):
        class Application:
            calls=0
            def request(self,path,body):
                self.calls+=1
                raise urllib.error.HTTPError(path,403,'Forbidden',{},None)
        app=Application()
        with tempfile.TemporaryDirectory() as directory:
            samples=check_model(app,provider(),'reference',pathlib.Path(directory),1,3,wait=lambda _:self.fail('permanent error retried'))
        self.assertEqual(app.calls,1)
        self.assertEqual(samples[0]['result']['failure']['code'],'check_http_403')

    def test_config_binding_ignores_application_state_but_binds_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            source=pathlib.Path(directory)
            (source/'credential-vault.age').write_text('first encrypted value')
            before=source_fingerprint(source)
            (source/'store.db').write_text('unrelated changing state')
            self.assertEqual(before,source_fingerprint(source))
            (source/'credential-vault.age').write_text('rotated encrypted value')
            self.assertNotEqual(before,source_fingerprint(source))

    def test_contract_failures_and_infrastructure_failures_remain_distinct(self):
        for result,want in [(receipt(),'passed'),(receipt(False),'failed'),(receipt(False,'provider'),'inconclusive')]:
            report={'models':[{'samples':[{'result':result}]}],'failures':[]}
            self.assertEqual(disposition(report),want)
        self.assertEqual(disposition({'models':[],'failures':[]}),'inconclusive')

    def test_failed_reference_does_not_hide_another_reference(self):
        plan=reference_plan();plan['providers'][0]['models'].append({'id':'unselected','covers':['second contract']})
        class Application:
            def request(self,path,body=None):
                if path=='/v1/providers':return {'providers':[provider()]}
                if body['model']=='reference':raise RuntimeError('controlled fixture failure')
                return receipt()
        with tempfile.TemporaryDirectory() as directory:
            out=pathlib.Path(directory)
            report=run_checks(Application(),plan,'',out,'b'*64)
            self.assertEqual(report['status'],'inconclusive')
            self.assertEqual([row['model'] for row in report['models']],['unselected'])
            self.assertEqual(report['failures'][0]['model'],'reference')
            self.assertTrue((out/'report.html').is_file())


if __name__=='__main__':unittest.main()
