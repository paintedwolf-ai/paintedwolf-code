import unittest
from dispatch_graph import waves, dependency_check_id
from preparation import validate_baselines


class DispatchGraphTests(unittest.TestCase):
    def setUp(self):
        self.dispatches = [{'label':label,'mode':'write'} for label in ['First','Second','Join']]

    def test_composite_check_identity_preserves_label_boundaries(self):
        self.assertNotEqual(dependency_check_id('A-to-B','C'), dependency_check_id('A','B-to-C'))

    def test_join_keeps_both_prerequisites_in_the_first_wave(self):
        result = waves(self.dispatches, {'Join':['First','Second']})
        self.assertEqual([[d['label'] for d in w] for w in result], [['First','Second'],['Join']])

    def test_undeclared_cyclic_self_and_duplicate_dependencies_are_rejected(self):
        for dependencies in [{'Other':['First']}, {'Join':['Other']}, {'Join':['Join']},
                             {'Join':['First','First']}, {'Join':[]}, {'Join':['First'],'First':['Join']}]:
            with self.subTest(dependencies=dependencies), self.assertRaises(ValueError):
                waves(self.dispatches, dependencies)

    def test_read_worker_cannot_supply_an_integration_prerequisite(self):
        self.dispatches[0]['mode'] = 'read'
        with self.assertRaises(ValueError): waves(self.dispatches, {'Join':['First']})


class PreflightAdmissionTests(unittest.TestCase):
    def test_each_admitted_project_has_its_own_baseline(self):
        value = {'admissions':[{'case':'case','project_id':'one'}, {'case':'scanner-runtime','project_id':'scan'}],
                 'baseline_checks':[{'project_id':'one'},{'project_id':'scan'}], 'scanner_health':{'project_id':'scan'}}
        validate_baselines(value, {'case'})
        for baselines in [[{'project_id':'one'}], [{'project_id':'one'},{'project_id':'one'}],
                          [{'project_id':'one'},{'project_id':'other'}]]:
            with self.subTest(baselines=baselines), self.assertRaises(ValueError):
                validate_baselines({**value,'baseline_checks':baselines}, {'case'})
