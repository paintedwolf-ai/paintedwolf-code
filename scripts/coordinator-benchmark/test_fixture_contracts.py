import json
import pathlib
import shutil
import tempfile
import unittest
from unittest.mock import patch
import focused_outcomes as oracle
from fixture_contracts import CONTRACTS, receipt_checks
from test_focused_outcomes import local_execute

ROOT = pathlib.Path(__file__).resolve().parents[2]
HERE = pathlib.Path(__file__).parent
CONTROLS = json.loads((HERE/'fixture-controls.json').read_text())
SUITE = {c['id']:c for c in json.loads((ROOT/'lycaon/test/fixtures/eval/coordinator-benchmark.json').read_text())['cases']}


class FixtureControls(unittest.TestCase):
    def exercise(self, case):
        baseline = ROOT/'lycaon/test/fixtures/eval'/SUITE[case]['project']
        controls = CONTROLS[case]
        with tempfile.TemporaryDirectory() as tmp, patch.object(oracle, 'execute', side_effect=local_execute):
            project = pathlib.Path(tmp)/'candidate'
            def result(changes):
                if project.exists(): shutil.rmtree(project)
                shutil.copytree(baseline,project)
                for name, body in {**controls['positive'], **changes}.items(): (project/name).write_text(body)
                checks = oracle.grade(project,baseline,case)['checks']
                if 'receipt' in CONTRACTS[case]: checks += receipt_checks(project,case,'current-control-receipt')
                return checks
            checks=result({})
            self.assertTrue(all(c['passed'] for c in checks), checks)
            for name, mutation in controls['negative'].items():
                with self.subTest(mutation=name):
                    self.assertTrue(any(not c['passed'] for c in result(mutation)), name)
            # Protected source and instructions cannot be edited to bypass the contract.
            self.assertTrue(any(not c['passed'] for c in result({'README.md':'changed'})))


for case in CONTRACTS:
    def control(self, case=case): self.exercise(case)
    setattr(FixtureControls, 'test_'+case.replace('-','_'),control)


class FixtureBank(unittest.TestCase):
    def test_artifacts_reject_ambiguous_or_nonfinite_json(self):
        with tempfile.TemporaryDirectory() as directory:
            path=pathlib.Path(directory)/'result.json'
            for body in ['{"release":"R16","release":"R17"}', '{"value":NaN}', '{"value":Infinity}']:
                path.write_text(body)
                self.assertIsNone(oracle.read_json(path))
            path.write_text('{"release":"R17"}')
            self.assertEqual(oracle.read_json(path), {'release':'R17'})

    def test_bank_and_controls_cover_every_declared_fixture(self):
        manifest=json.loads((HERE/'benchmark.json').read_text())
        scenarios={}
        for op in manifest['operations']:
            self.assertIn(op['id'],SUITE)
            if op['role']=='scored': scenarios.setdefault(op['scenario'],[]).append(op['id'])
        self.assertTrue(scenarios)
        self.assertTrue(set(CONTRACTS) <= {o['id'] for o in manifest['operations']})
        self.assertEqual(set(CONTROLS),set(CONTRACTS))
        for case in CONTRACTS:
            self.assertTrue(CONTROLS[case]['negative'])
            self.assertTrue(CONTROLS[case]['positive'])

    def test_malformed_bank_is_rejected_before_paid_execution(self):
        import copy
        from preparation import validate_bank
        manifest=json.loads((HERE/'benchmark.json').read_text())
        suite={'cases':list(SUITE.values()), 'task_allowance': 256}
        validate_bank(manifest,suite)
        for value in [None, 0, -1, True, 2.5]:
            with self.subTest(allowance=value), self.assertRaises(ValueError):
                validate_bank(manifest, {**suite, 'task_allowance': value})
        for mutation in ['duplicate','missing','family','worker-policy']:
            altered=copy.deepcopy(manifest)
            if mutation=='duplicate': altered['operations'].append(altered['operations'][0])
            if mutation=='missing': altered['operations'].pop(0)
            if mutation=='worker-policy':
                altered_suite=copy.deepcopy(suite)
                next(c for c in altered_suite['cases'] if c.get('setup',{}).get('policy')=='scripted')['setup']['policy']='live'
                with self.assertRaises(ValueError): validate_bank(altered,altered_suite)
                continue
            if mutation=='family': altered['operations'][0]['family']='wrong'
            with self.subTest(mutation=mutation), self.assertRaises(ValueError): validate_bank(altered,suite)

    def test_baseline_verification_reaches_a_successful_old_revision(self):
        import subprocess
        for case in CONTRACTS:
            spec=SUITE[case]
            if not spec.get('prelude_final'): continue
            with self.subTest(case=case), tempfile.TemporaryDirectory() as tmp:
                project=pathlib.Path(tmp)/'project'
                shutil.copytree(ROOT/'lycaon/test/fixtures/eval'/spec['project'],project)
                result=subprocess.run(['python3','-B','-m','unittest','discover'],cwd=project,capture_output=True,timeout=30)
                self.assertEqual(result.returncode,0,result.stderr.decode())

    def test_worker_overlays_have_their_declared_test_results(self):
        import subprocess
        for case in CONTRACTS:
            spec=SUITE[case]
            for overlay in spec.get('setup',{}).get('overlays',[]):
                with self.subTest(case=case), tempfile.TemporaryDirectory() as tmp:
                    project=pathlib.Path(tmp)/'project'
                    shutil.copytree(ROOT/'lycaon/test/fixtures/eval'/spec['project'],project)
                    for name,body in overlay['files'].items(): (project/name).write_text(body)
                    process=subprocess.run(overlay['verify'].split(),cwd=project,capture_output=True,timeout=30)
                    self.assertEqual(process.returncode==0,overlay['verification_verdict']=='passed',process.stderr.decode())
                    if continuation := overlay.get('continuation'):
                        for name, body in continuation['files'].items(): (project/name).write_text(body)
                        process=subprocess.run(continuation['verify'].split(),cwd=project,capture_output=True,timeout=30)
                        self.assertEqual(process.returncode,0,process.stderr.decode())

    def test_exact_artifacts_are_the_current_user_files(self):
        for case, contract in CONTRACTS.items():
            for path, body in contract.get('artifact_bytes',{}).items():
                self.assertEqual(body,SUITE[case]['setup']['integration_files'][path])
