import json
from ledger import scripted_execution_errors, scripted_outcome
from test_orchestration_evidence import LedgerCase


class ReceiptTests(LedgerCase):
    def test_missing_or_failed_execution_cannot_be_a_model_measurement(self):
        self.job('job','child','write',['report.py'])
        case={'session_id':'root'}
        spec={'setup':{'policy':'scripted'}}
        self.assertEqual(scripted_execution_errors(self.root,case,spec),['job'])
        path=self.root/'worker-scripts/outcomes/job.json'
        path.write_text(json.dumps([{'job_id':'job','kind':'complete'}]))
        self.assertEqual(scripted_execution_errors(self.root,case,spec),[])
        path.write_text(json.dumps([{'job_id':'job','kind':'error','code':'HARNESS_EXECUTION_FAILED'}, {'job_id':'job','kind':'complete'}]))
        self.assertEqual(scripted_execution_errors(self.root,case,spec),['job'])
        self.assertEqual(scripted_outcome(self.root,'job')['kind'],'complete')

    def test_prepared_seed_requires_no_execution_receipt(self):
        self.job('seed','child','write',['report.py'])
        self.assertEqual(scripted_execution_errors(self.root, {'session_id':'root','prepared_overlays':{'overlays':[{'job_id':'seed'}]}}, {'setup':{'policy':'scripted'}}),[])

    def test_cancellation_before_execution_is_a_model_outcome(self):
        self.job('cancelled','child','write',['report.py'],status='cancelled')
        self.assertEqual(scripted_execution_errors(self.root, {'session_id':'root'}, {'setup':{'policy':'scripted'}}),[])
