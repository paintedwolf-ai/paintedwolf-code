import json
import pathlib
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
from release_policy import decision
import run


class ReleasePolicyTests(unittest.TestCase):
    def test_explicit_selection_accepts_patches_prereleases_and_pre_v1(self):
        for version in ['0.8.0','0.8.1','1.0.0-rc.1','1.0.0','2.3.0+build.45']:
            with self.subTest(version=version):
                self.assertEqual(decision(version), {'version':version,'cadence':'selected',
                    'eligible':True,'reason':'selected_target'})

    def test_optional_major_minor_cadence_skips_other_versions(self):
        for version,eligible in [('0.8.0',True),('0.8.1',False),('1.0.0-rc.1',False),('2.0.0',True)]:
            with self.subTest(version=version):
                self.assertEqual(decision(version,'major-minor')['eligible'],eligible)

    def test_invalid_versions_and_cadences_are_rejected(self):
        for version in ['v1.0.0','1.2','01.2.0','1.0.0-01','not-a-version']:
            for cadence in ['selected','major-minor']:
                with self.subTest(version=version,cadence=cadence),self.assertRaises(ValueError):
                    decision(version,cadence)
        with self.assertRaises(ValueError): decision('1.0.0','daily')

    def args(self, root, **changes):
        values=dict(allow_live=True,resume=False,cadence='selected',out=root/'out',source_config=root/'missing',
                    application_ref='v0.9.0',application_worktree=False,prepared=None,repair_from=None,
                    harness_ref=None,harness_worktree=False,reason=None,mode=None,concurrency=None,cloud_concurrency=None,
                    provider_limit=None,models=None,cases=None,repetitions=None,tier=None)
        return SimpleNamespace(**(values|changes))

    def test_cadence_uses_selected_application_before_build_credentials_or_output(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=pathlib.Path(tmp);args=self.args(root,cadence='major-minor')
            with patch.object(run,'select',return_value={'version':'0.9.1'}),patch.object(run,'prepare_target') as prepare:
                run.run(args)
                prepare.assert_not_called()
            self.assertFalse(args.out.exists())

    def test_paid_opt_in_is_required_before_target_selection(self):
        with tempfile.TemporaryDirectory() as tmp:
            with patch.object(run,'select') as select,self.assertRaisesRegex(ValueError,'--allow-live'):
                run.run(self.args(pathlib.Path(tmp),allow_live=False))
            select.assert_not_called()

    def test_prepared_run_uses_its_runner_even_when_only_manifest_or_dependencies_changed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            args = self.args(root, prepared=root/'prepared', application_ref=None)
            selected = {'implementation_sha256':run.IMPLEMENTATION}
            with patch.object(run, 'select', return_value={'prepared':str(args.prepared)}), \
                 patch.object(run, 'verify_prepared', return_value=selected), \
                 patch.object(run.os, 'execv', side_effect=RuntimeError('frozen handoff')) as execute:
                with self.assertRaisesRegex(RuntimeError, 'frozen handoff'):
                    run.run(args)
            command = execute.call_args.args[1]
            self.assertEqual(command[1:3], [str(args.prepared/'source/scripts/coordinator-benchmark/python_runtime.py'),
                                           str(args.prepared/'source/scripts/coordinator-benchmark/run.py')])
            self.assertFalse(args.out.exists())

    def test_resume_does_not_reclassify_or_replace_the_target(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=pathlib.Path(tmp);(root/'plan.json').write_text('{}')
            args=self.args(root,out=root,resume=True,application_ref=None)
            with patch.object(run,'release_decision') as classify,patch.object(run,'verify_execution',side_effect=RuntimeError('fixture resume')):
                with self.assertRaisesRegex(RuntimeError,'fixture resume'): run.run(args)
                classify.assert_not_called()
            args.application_ref='v0.10.0'
            with self.assertRaisesRegex(ValueError,'preserves its target'): run.run(args)

    def test_read_only_cadence_check_needs_no_credentials(self):
        script=pathlib.Path(__file__).with_name('release_policy.py')
        for cadence,eligible in [('selected',True),('major-minor',False)]:
            result=subprocess.run([sys.executable,str(script),'--version','0.9.1','--cadence',cadence],
                                  text=True,capture_output=True,check=True)
            self.assertEqual(json.loads(result.stdout)['eligible'],eligible)


if __name__=='__main__': unittest.main()
