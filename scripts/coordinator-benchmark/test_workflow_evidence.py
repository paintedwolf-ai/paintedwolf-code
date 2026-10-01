import copy
import json
import pathlib
from test_episode_support import EvidenceCase
from workflow_evidence import workflow_checks

OPERATIONS = [op for op in json.loads(pathlib.Path(__file__).with_name('benchmark.json').read_text())['operations'] if op.get('workflow')]


class WorkflowFixtureCase(EvidenceCase):
    def seed(self, operation):
        self.contract = operation['workflow']
        self.facts['delivered_paths']=list(self.contract['source_paths'])
        self.session['workflows'] = [{'id':'flow','workflow_id':self.contract['id'],
            'workflow_version':self.contract['version'],'status':'complete','current_phase':'done'}]
        self.session['verdicts'] = [{
            'tool_call_id':str(i), 'run_id':'flow','phase':r['phase'],'status':'committed',
            'source_revision':i+1,
            'submission':{'session_id':'root','verdict':copy.deepcopy(r['verdict'])},
            'outcome':{'Applied':True,'Valid':True,'Terminal':r.get('terminal',True),
                       'Grounding':{'traced':True,'cited_evidence':[dict(c,verdict='matched') for c in r['citations']]} if r.get('terminal',True) else None}}
            for i,r in enumerate(self.contract['receipts'])]

    def checks(self):
        self.save()
        return {c['id']:c['passed'] for c in workflow_checks(self.root,{'session_id':'root','workflow_run_id':'flow'},self.contract)}


class WorkflowEvidenceTests(WorkflowFixtureCase):
    def test_every_intended_record_sequence_passes_and_missing_records_fail(self):
        for operation in OPERATIONS:
            with self.subTest(case=operation['id']):
                self.seed(operation)
                self.assertTrue(all(self.checks().values()))
                self.session['verdicts'].pop(0)
                self.assertFalse(self.checks()['workflow-receipt-set'])

    def test_completed_workflow_does_not_replace_wrong_phase_value_or_ungrounded_receipt(self):
        for change in ['phase','value','audit','release','status','repeat']:
            with self.subTest(change=change):
                self.seed(OPERATIONS[0]);receipt=self.session['verdicts'][0]
                if change=='phase': receipt['phase']='other'
                if change=='value': receipt['submission']['verdict']['artifact']='wrong'
                if change=='audit': receipt['outcome']['Grounding']['cited_evidence']=[]
                if change=='release': receipt['submission']['verdict']['release']='R16'
                if change=='status': self.session['workflows'][0]['status']='failed'
                if change=='repeat': self.session['verdicts'].append(copy.deepcopy(receipt))
                self.assertFalse(all(self.checks().values()))

    def test_rejected_submissions_do_not_count_as_accepted_records(self):
        self.seed(OPERATIONS[0])
        rejected=copy.deepcopy(self.session['verdicts'][0]);rejected['outcome']['Valid']=False
        self.session['verdicts'].insert(0,rejected)
        self.assertTrue(all(self.checks().values()))

    def test_file_handles_and_line_citations_cover_the_same_source(self):
        for whole_file in [True, False]:
            with self.subTest(whole_file=whole_file):
                self.seed(OPERATIONS[0])
                for receipt in self.session['verdicts']:
                    for citation in receipt['outcome']['Grounding']['cited_evidence']:
                        citation['handle']='read#2'
                        if whole_file: citation.pop('line',None)
                self.assertTrue(all(self.checks().values()))

    def test_wrong_source_or_host_assembled_citations_fail(self):
        for change in ['path', 'line', 'verdict', 'host_assembled']:
            with self.subTest(change=change):
                self.seed(OPERATIONS[0])
                audit=self.session['verdicts'][0]['outcome']['Grounding']
                if change=='path': audit['cited_evidence'][0]['path']='README.md'
                if change=='line':
                    self.contract=copy.deepcopy(self.contract)
                    self.contract['receipts'][0]['citations'][0]['line']=2
                    audit['cited_evidence'][0]['line']=1
                if change=='verdict': audit['cited_evidence'][0]['verdict']='unmatched'
                if change=='host_assembled': audit['host_assembled']=True
                self.assertFalse(self.checks()['workflow-receipt-'+self.contract['receipts'][0]['id']])

    def test_undeclared_added_files_fail_the_closed_delivery_contract(self):
        self.seed(OPERATIONS[0])
        self.facts['delivered_paths'].append('extra.json')
        self.assertFalse(self.checks()['workflow-delivery-paths'])

    def test_every_record_field_is_required_and_exact(self):
        for operation in OPERATIONS:
            for index, required in enumerate(operation['workflow']['receipts']):
                for field in required['verdict']:
                    for mutation in ['missing', 'wrong', 'type']:
                        with self.subTest(case=operation['id'], receipt=index, field=field, mutation=mutation):
                            self.seed(operation)
                            record=self.session['verdicts'][index]['submission']['verdict']
                            if mutation=='missing': del record[field]
                            elif mutation=='wrong': record[field]='other'
                            else: record[field]=False
                            self.assertFalse(self.checks()['workflow-receipt-'+required['id']])

    def test_declared_negative_controls_fail_their_named_checks(self):
        from workflow_controls import plan
        controls=json.loads(pathlib.Path(__file__).with_name('fixture-controls.json').read_text())
        for operation in OPERATIONS:
            control=controls[operation['id']]['workflow']
            for negative in control['negative']:
                with self.subTest(case=operation['id'], negative=negative['id']):
                    self.seed(operation)
                    receipts, steps, failed=plan(self.contract, control, negative)
                    original=self.session['verdicts']
                    actual=[]
                    for step in steps:
                        if type(step) is not int: continue
                        record=copy.deepcopy(original[step])
                        record['phase']=receipts[step]['phase']
                        record['submission']['verdict']=receipts[step]['verdict']
                        record['source_revision']=len(actual)+1
                        actual.append(record)
                    self.session['verdicts']=actual
                    checks=self.checks()
                    self.assertTrue(failed)
                    self.assertEqual(set(failed), {name for name, passed in checks.items() if not passed})
