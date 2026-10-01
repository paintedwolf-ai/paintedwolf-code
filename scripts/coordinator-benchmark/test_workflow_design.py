"""Exhaustive checks of finite fixture solutions and permitted receipt orders."""
import copy
import itertools
import json
import pathlib
import unittest
from test_workflow_evidence import WorkflowFixtureCase, OPERATIONS
from workflow_evidence import validate_receipts

ROOT = pathlib.Path(__file__).resolve().parents[2]


def operation(name):
    return next(op for op in OPERATIONS if op['id'] == name)


def fixture(name, file):
    return json.loads((ROOT/'lycaon/test/fixtures/eval/coordinator'/name/file).read_text())


def linearizations(receipts, prefix=()):
    if len(prefix) == len(receipts):
        yield prefix
        return
    for record in receipts:
        if record['id'] not in prefix and set(record['after']) <= set(prefix):
            yield from linearizations(receipts, (*prefix, record['id']))


class WorkflowDesignTests(unittest.TestCase):
    def test_approval_catalog_has_one_joint_solution_and_matches_expected_records(self):
        name='workflow-revision-reapproval'
        baseline=fixture(name,'baseline.json')['components']
        rows=[r for r in fixture(name,'approvals.json') if r['release']=='R17'
              and r['environment']=='production' and r['result']=='passed']
        components=list(baseline)
        solutions=[]
        for records in itertools.product(*[[r for r in rows if r['component']==c] for c in components]):
            chosen={r['component']:r for r in records}
            if all(chosen[d]['revision']==revision for r in records for d,revision in r['dependencies'].items()):
                solutions.append(chosen)
        self.assertEqual(len(solutions),1)
        selected=solutions[0]
        expected=operation(name)['workflow']['receipts']
        self.assertEqual(selected['cli']['revision'], baseline['cli']['revision'])
        self.assertNotEqual(selected['cli']['dependencies'], baseline['cli']['dependencies'])
        self.assertEqual({c:r['revision'] for c,r in selected.items()},
                         {c:expected[0]['verdict'][c] for c in components})
        for c,r in selected.items():
            renew=any(r[k]!=baseline[c][k] for k in ['revision','dependencies'])
            self.assertEqual(expected[1]['verdict'][c], 'renew' if renew else 'retain')
            self.assertEqual(expected[2]['verdict'][c], r['approval'] if renew else baseline[c]['approval'])

    def test_recovery_contract_covers_exact_transitive_closure_and_artifacts(self):
        name='workflow-dependent-recovery'
        dependencies=fixture(name,'dependencies.json')
        rows={r['component']:r for r in fixture(name,'deployments.json') if r['release']=='R17'}
        failed={c for c,r in rows.items() if r['result']=='failed'}
        affected=set(failed)
        while True:
            expanded=affected|{c for c,parents in dependencies.items() if set(parents)&affected}
            if expanded==affected: break
            affected=expanded
        receipts=operation(name)['workflow']['receipts']
        for c in rows:
            self.assertEqual(receipts[0]['verdict'][c], 'failed' if c in failed else 'dependent' if c in affected else 'preserved')
            self.assertEqual(receipts[-1]['verdict'][c],rows[c]['previous' if c in affected else 'current'])
        actions=[r['verdict'] for r in receipts if r['phase']=='recovery_action']
        self.assertEqual({(r['component'],r['action']) for r in actions},
                         {(c,'restore') for c in affected}|{(c,'suspend') for c in affected-failed})
        for r in actions:
            self.assertEqual(r['artifact'], rows[r['component']]['current' if r['action']=='suspend' else 'previous'])
        for order in linearizations(receipts):
            positions={name:i for i,name in enumerate(order)}
            for c in affected:
                for parent in dependencies[c]:
                    if parent in affected:
                        self.assertLess(positions['restore-'+parent], positions['restore-'+c])
                    if parent in affected-failed:
                        self.assertLess(positions['suspend-'+c], positions['suspend-'+parent])
            self.assertLess(max(positions['suspend-'+c] for c in affected-failed),
                            min(positions['restore-'+c] for c in affected))

    def test_amendment_requires_exactly_two_corrections_and_preserves_history(self):
        name='workflow-reopen-branch'
        original={r['region']:r for r in fixture(name,'handoffs.json')}
        current=copy.deepcopy(original)
        amendment=fixture(name,'amendment.json');current[amendment['region']]=amendment
        corrections=[amendment]
        for region,record in current.copy().items():
            if all(current[d]['revision']==v for d,v in record['dependencies'].items()): continue
            replacements=[r for r in fixture(name,'replacements.json') if r['release']=='R17'
                          and r['region']==region and r['result']=='passed'
                          and all(current[d]['revision']==v for d,v in r['dependencies'].items())]
            self.assertEqual(len(replacements),1)
            current[region]=replacements[0];corrections.append(replacements[0])
        expected=operation(name)['workflow']['receipts']
        self.assertEqual([r['region'] for r in corrections],['east','west'])
        for r in expected[1:-1]:
            value=r['verdict'];region=value['region']
            self.assertEqual(value['revision'],current[region]['revision'])
            self.assertEqual(value['before_revision'],original[region]['revision'])
            self.assertEqual(value['artifact'],current[region]['artifact'])
        for region in original:
            self.assertEqual(expected[0]['verdict'][region],original[region]['artifact'])
            self.assertEqual(expected[-1]['verdict'][region],current[region]['artifact'])

    def test_receipt_contracts_reject_ambiguous_selectors_and_cycles(self):
        for op in OPERATIONS: validate_receipts(op['workflow'])
        original=operation('workflow-dependent-recovery')['workflow']
        for change in ['overlap','cycle','missing','duplicate']:
            with self.subTest(change=change):
                contract=copy.deepcopy(original)
                if change=='overlap': contract['receipts'][1]['identity']={}
                if change=='cycle': contract['receipts'][0]['after']=['index']
                if change=='missing': contract['receipts'][0]['after']=['absent']
                if change=='duplicate': contract['receipts'][1]['id']='scope'
                with self.assertRaises(ValueError): validate_receipts(contract)


class WorkflowOrderingTests(WorkflowFixtureCase):
    def test_every_legal_recovery_order_passes(self):
        op=operation('workflow-dependent-recovery')
        orders=list(linearizations(op['workflow']['receipts']))
        self.assertEqual(len(orders),4)
        for order in orders:
            with self.subTest(order=order):
                self.seed(op)
                by_id=dict(zip([r['id'] for r in self.contract['receipts']],self.session['verdicts']))
                self.session['verdicts']=[by_id[name] for name in order]
                for index, record in enumerate(self.session['verdicts']): record['source_revision']=index+1
                self.assertTrue(all(self.checks().values()))

    def test_swapped_dependent_actions_fail_only_order(self):
        self.seed(operation('workflow-dependent-recovery'))
        records=self.session['verdicts']
        records[4],records[5]=records[5],records[4]
        for index, record in enumerate(records): record['source_revision']=index+1
        self.assertEqual([k for k,v in self.checks().items() if not v],['workflow-receipt-order'])

    def test_duplicate_action_cannot_replace_an_omitted_action(self):
        self.seed(operation('workflow-dependent-recovery'))
        self.session['verdicts'][2]=copy.deepcopy(self.session['verdicts'][1])
        checks=self.checks()
        self.assertFalse(checks['workflow-receipt-set'])
        self.assertFalse(checks['workflow-receipt-suspend-web'])
        self.assertFalse(checks['workflow-receipt-suspend-api'])

    def test_export_array_order_does_not_replace_host_revisions(self):
        self.seed(operation('workflow-dependent-recovery'))
        self.session['verdicts'].reverse()
        self.assertTrue(all(self.checks().values()))

    def test_reused_source_revision_fails_order(self):
        self.seed(operation('workflow-dependent-recovery'))
        self.session['verdicts'][2]['source_revision']=self.session['verdicts'][1]['source_revision']
        self.assertFalse(self.checks()['workflow-receipt-order'])
