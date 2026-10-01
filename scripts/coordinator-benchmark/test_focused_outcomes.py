import json
import pathlib
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import focused_outcomes as oracle

ROOT = pathlib.Path(__file__).resolve().parents[2] / 'lycaon/test/fixtures/eval/coordinator-focused'


def local_execute(project, program):
    # Unit controls use the same observation program. The live smoke exercises
    # the isolated container separately.
    program = program.replace('/candidate', str(project))
    result = subprocess.run(['python3','-B','-c',program], cwd=project, text=True, capture_output=True,timeout=10)
    return json.loads(result.stdout) if result.returncode == 0 else None


class FocusedOutcomeControls(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = pathlib.Path(self.temp.name)
        patched = patch.object(oracle,'execute',side_effect=local_execute)
        patched.start();self.addCleanup(patched.stop)

    def project(self, name):
        root=self.path/name
        shutil.copytree(ROOT/name,root)
        return root

    def grade(self, project, case):
        return oracle.grade(project,ROOT/project.name,case)

    def assert_passes(self, project, case):
        result=self.grade(project,case)
        self.assertTrue(result['passed'], result['checks'])

    def assert_fails(self, project, case, check):
        result=self.grade(project,case)
        self.assertIn(check,[c['id'] for c in result['checks'] if not c['passed']])


    def test_current_revision_accepts_equivalent_implementations_and_rejects_old_default(self):
        p=self.project('verification')
        self.assert_fails(p,'current-verification','selection-behavior')
        for source in [
            'def select(records, limit=3):\n    return sorted(records,key=lambda r:(-r["score"],r["name"]))[:limit]\n',
            'def select(records, limit=3):\n    ordered=sorted(records,key=lambda r:r["name"])\n    ordered.sort(key=lambda r:r["score"],reverse=True)\n    return ordered[:limit]\n']:
            (p/'report.py').write_text(source)
            self.assert_passes(p,'current-verification')
        (p/'report.py').write_text(source.replace('limit=3','limit=8'))
        self.assert_fails(p,'current-verification','selection-behavior')

    def test_changed_intent_covers_function_and_cli(self):
        p=self.project('changed-intent')
        self.assert_fails(p,'changed-intent','selection-behavior')
        (p/'report.py').write_text('def summarize(records,limit=2):\n    return sorted([r for r in records if r["active"]],key=lambda r:r["name"])[:limit]\n')
        self.assert_passes(p,'changed-intent')
        (p/'app.py').write_text('import json,sys\nprint(json.dumps(sorted(json.load(sys.stdin),key=lambda r:r["name"])[:2]))\n')
        self.assert_fails(p,'changed-intent','cli-latest-intent')

    def test_worker_all_supported_states_and_preserved_user_file(self):
        p=self.project('worker')
        for case in ['worker-complete','worker-partial']:
            (p/'status.py').write_text('def display_status(status):\n    return {"pending":"Waiting","active":"Running","done":"Complete"}[status]\n')
            self.assert_passes(p,case)
            (p/'status.py').write_text('def display_status(status):\n    if status=="pending": return "Waiting"\n    if status=="active": return "Running"\n    return "Complete"\n')
            self.assert_passes(p,case)
            (p/'status.py').write_text('def display_status(status):\n    return {"pending":"Waiting","active":"Running","done":"Done"}[status]\n')
            self.assert_fails(p,case,'status-labels')
        (p/'settings.json').write_text('{"theme":"amber","notifications":false}')
        self.assert_fails(p,'worker-partial','preserved-settings.json')

    def test_merge_composition_and_alternative_implementations(self):
        p=self.project('merge')
        (p/'preferences.json').write_text('{"owner_note":"Preserve this user preference","theme":"violet"}\n')
        prefix='def select_records(records,prefix="",offset=0,limit=None):\n    if type(offset) is not int or offset<0 or (limit is not None and (type(limit) is not int or limit<0)): raise ValueError("invalid pagination")\n'
        solutions=[
            prefix+'    rows=[r for r in records if r["name"].casefold().startswith(prefix.casefold())]\n    return rows[offset:] if limit is None else rows[offset:offset+limit]\n',
            prefix+'    rows=list(filter(lambda r:r["name"].casefold().startswith(prefix.casefold()),records))[offset:]\n    return rows if limit is None else rows[:limit]\n']
        suite=json.loads((ROOT.parent/'coordinator-benchmark.json').read_text())
        scenario=next(c for c in suite['cases'] if c['id']=='workspace-conflict')
        for overlay in scenario['setup']['overlays']:
            (p/'catalog.py').write_text(overlay['files']['catalog.py'])
            self.assert_fails(p,'workspace-conflict','filter-before-pagination')
        for source in solutions:
            (p/'catalog.py').write_text(source)
            self.assert_passes(p,'workspace-conflict')
        (p/'catalog.py').write_text(prefix+'    rows=records[offset:] if limit is None else records[offset:offset+limit]\n    return [r for r in rows if r["name"].casefold().startswith(prefix.casefold())]\n')
        self.assert_fails(p,'workspace-conflict','filter-before-pagination')



    def test_json_predicates_distinguish_booleans_from_numbers(self):
        self.assertFalse(oracle.required_fields({'ready':1},{'ready':True}))
        self.assertFalse(oracle.required_fields({'count':True},{'count':1}))
        self.assertTrue(oracle.required_fields({'count':1,'note':'extra'},{'count':1}))

    def test_candidate_container_does_not_receive_oracle_or_baseline(self):
        with patch('oracle_process.capture') as run:
            run.return_value.returncode=0
            run.return_value.stdout=b'[]'
            self.assertEqual(self.add_container_observation(),[])
            command=run.call_args.args[0]
            mounts=[command[i+1] for i,value in enumerate(command[:-1]) if value=='--mount']
            self.assertEqual(len(mounts),1)
            self.assertIn('dst=/candidate,readonly',mounts[0])

    def add_container_observation(self):
        # Exercise the real container builder despite the local observation adapter.
        import importlib.util
        spec=importlib.util.spec_from_file_location('isolated_oracle',pathlib.Path(oracle.__file__))
        module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
        return module.execute(self.path,'print("[]")')
