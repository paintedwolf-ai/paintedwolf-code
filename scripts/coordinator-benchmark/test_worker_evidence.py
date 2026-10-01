from test_orchestration_evidence import LedgerCase
from worker_evidence import worker_checks


class WorkerEvidenceTests(LedgerCase):
    def checks(self):
        self.save()
        return {c['id']:c['passed'] for c in worker_checks(self.root,{'id':'worker-integration','session_id':'root'},
                                                        {'writer','reader'},{'test_regression.py'})}

    def effect(self,path,branch=''):
        self.facts['effects'].append({'job_id':'implement','path':path,'branch_id':branch,'origin':'agent','entry_kind':'file','op':'write'})

    def test_integration_requires_distinct_completed_review_and_landed_code_and_test(self):
        self.job('implement','writer','write',['ranking.py','test_regression.py'])
        self.job('review','reader','read',['ranking.py'],status='running',merge='')
        self.effect('ranking.py')
        self.assertFalse(self.checks()['worker-review'])
        self.job_by_id('review')['status']='complete'
        self.assertTrue(self.checks()['worker-review'])
        self.assertFalse(self.checks()['worker-contribution'])
        self.effect('test_regression.py',branch='overlay')
        self.assertFalse(self.checks()['worker-contribution'])
        self.facts['effects'][-1]['branch_id']=''
        self.assertTrue(all(self.checks().values()))
        self.job_by_id('review')['child_session_id']='writer'
        self.assertFalse(self.checks()['independent-workers'])
        self.job_by_id('implement')['merge_status']='rejected'
        self.assertFalse(self.checks()['worker-contribution'])

    def test_read_scope_and_complete_return_are_required(self):
        self.job('review','reader','read',['ranking.py'],status='failed',merge='')
        self.assertFalse(self.checks()['worker-review'])
        self.job_by_id('review')['status']='complete'
        self.assertTrue(self.checks()['worker-review'])
        self.job_by_id('review')['scope']['mode']='write'
        self.assertFalse(self.checks()['worker-review'])
