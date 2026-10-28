"""Invariant controls for the actual shared host-only job settlement implementation."""
import fcntl
import json
import os
from pathlib import Path
import shutil
import sys
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import managed_job_settlement as module


class JobSettlementTests(unittest.TestCase):
    def fixture(self):
        records=[]
        value=module.JobSettlement(checkout=Path('/fixed/source'),arguments=['fixed'],
            save=lambda name,value:records.append((name,value)),command=Mock())
        return value,records

    def test_invocation_without_arguments_is_bound_by_exact_inherited_completion_lock(self):
        value,_=self.fixture()
        with tempfile.TemporaryDirectory() as temporary:
            directory=Path(temporary).resolve();lock=directory/'completion.lock';lock.touch(mode=0o600)
            value.directory=lambda job:directory
            birth={'pid':121,'parent_pid':120,'start_time_ticks':7,'uid':os.getuid(),'owner_identity':'exact-birth'}
            observer=SimpleNamespace(birth=Mock(return_value=birth), files=Mock(
                side_effect=lambda pid, limit:[(lock.stat().st_dev,lock.stat().st_ino)] if pid==121 else []))
            value.observer=observer
            own={'kind':'invocation','pid':121,'owner_identity':'exact-birth','ticket':'1-'+'a'*32}
            foreign={**own,'pid':122,'ticket':'2-'+'b'*32,'source':'/fixed/source'}
            selected=value.invocation({}, {'runs':[foreign,own]})
            self.assertEqual(selected['entry'],own)
            self.assertEqual(selected['completion_lock']['inode'],lock.stat().st_ino)
            self.assertIsNone(value.invocation({}, {'runs':[foreign]}))
            with self.assertRaises(ValueError):
                value.invocation({}, {'runs':[{**own,'owner_identity':'replaced'}]})
            for changed in ({**birth,'owner_identity':None},{**birth,'uid':os.getuid()+1}):
                observer.birth.return_value=changed
                self.assertIsNone(value.invocation({}, {'runs':[own]}))
            observer.birth.side_effect=[birth,{**birth,'start_time_ticks':8}]
            with self.assertRaisesRegex(ValueError,'changed process identity'):
                value.invocation({}, {'runs':[own]})
            observer.birth.side_effect=None;observer.birth.return_value=birth
            observer.files.return_value=[(0,0)]*1025;observer.files.side_effect=None
            with self.assertRaisesRegex(ValueError,'exceeds bound'):
                value.invocation({}, {'runs':[own]})

    def test_actual_repository_namespace_and_repeated_validation_preserve_one_physical_location(self):
        checkout=Path(__file__).resolve().parents[3]
        script="""
from pathlib import Path
import sys
checkout=Path(sys.argv[1]).resolve()
namespace=checkout/'scripts'
assert not (namespace/'__init__.py').exists()
sys.path[:0]=[str(namespace),str(checkout),str(checkout)]
from managed_job_settlement import JobSettlement
import scripts
assert scripts.__file__ is None
value=JobSettlement(checkout=checkout,arguments=[],save=None,command=None)
value.validate_checkout()
value.validate_checkout()
assert sys.path.count(str(namespace))==1
assert sys.path.count(str(checkout))==1
import scripts.artifact_paths
value.validate_checkout()
from scripts.linux_release.installed_campaign import command
import scripts.harness
assert callable(command)
assert scripts.harness.__file__ is None
assert list(scripts.harness.__path__)==[str(namespace/'harness')]
value.validate_checkout({'scripts.linux_release.campaign.guests':'scripts/linux_release/campaign/guests.py',
                         'scripts.harness':'scripts/harness'})
value.validate_checkout()
"""
        result=subprocess.run([sys.executable,'-I','-B','-c',script,str(checkout)],
                              capture_output=True,text=True,timeout=15)
        self.assertEqual(result.returncode,0,result.stdout+result.stderr)

    def test_extra_namespace_or_regular_package_refuses_before_import_or_path_mutation(self):
        value,_=self.fixture();value.checkout=Path(__file__).resolve().parents[3]
        for regular in (False,True):
            with self.subTest(regular=regular),tempfile.TemporaryDirectory() as temporary:
                foreign=Path(temporary)/'scripts';foreign.mkdir()
                if regular:(foreign/'__init__.py').write_text('raise AssertionError("must not import")')
                original=[str(value.checkout),temporary]
                state=SimpleNamespace(modules={},path=list(original))
                with patch.object(module,'sys',state), \
                        patch.object(module.importlib,'import_module') as imported:
                    with self.assertRaises(ValueError):value.validate_checkout()
                    self.assertEqual(state.path,original)
                    imported.assert_not_called()

    def test_cached_namespace_or_concrete_module_origin_cannot_escape_exact_checkout(self):
        value,_=self.fixture();value.checkout=Path(__file__).resolve().parents[3]
        namespace=value.checkout/'scripts'
        native=module.PathFinder.find_spec('scripts',[str(value.checkout)])
        nested=module.PathFinder.find_spec('scripts.harness',[str(namespace)])
        candidates=[{'scripts':SimpleNamespace(__file__=None,__spec__=native,
                                              __path__=[str(namespace),'/foreign/scripts'])},
                    {'scripts':SimpleNamespace(__file__='/foreign/scripts/__init__.py')},
                    {'scripts.harness':SimpleNamespace(__file__=None,__spec__=nested,
                                              __path__=[str(namespace/'harness'),'/foreign/scripts/harness'])},
                    {'scripts.harness':SimpleNamespace(__file__=None,
                        __spec__=SimpleNamespace(origin=None,submodule_search_locations=['/foreign/scripts/harness']),
                        __path__=[str(namespace/'harness')])},
                    {'scripts.artifact_paths':SimpleNamespace(__file__=str(namespace/'verification_resources.py'))}]
        for cached in candidates:
            with self.subTest(cached=list(cached)):
                original=[str(value.checkout)]
                state=SimpleNamespace(modules=cached,path=list(original))
                with patch.object(module,'sys',state), \
                        patch.object(module.importlib,'import_module') as imported:
                    with self.assertRaises(ValueError):value.validate_checkout()
                    self.assertEqual(state.path,original)
                    imported.assert_not_called()

    def test_result_cannot_settle_until_its_inherited_completion_lock_releases(self):
        value,_=self.fixture()
        with tempfile.TemporaryDirectory() as temporary:
            directory=Path(temporary).resolve();lock=directory/'completion.lock';lock.touch(mode=0o664)
            value.directory=lambda job:directory
            job={'job_id':'c'*32,'log':str(directory/'output.log')}
            result={**job,'state':'completed','exit_code':201,'finished_at':1.0}
            (directory/'result.json').write_text(json.dumps(result))
            with lock.open('r+') as held:
                fcntl.flock(held,fcntl.LOCK_EX|fcntl.LOCK_NB)
                self.assertIsNone(value.completed(job))
            self.assertEqual(value.completed(job),result)
            (directory/'result.json').write_text(json.dumps({**result,'job_id':'d'*32}))
            with self.assertRaises(ValueError):value.completed(job)

    def test_callers_validate_their_own_graph_without_importing_or_mutating_paths_on_refusal(self):
        source=Path(__file__).resolve().parents[3]
        with tempfile.TemporaryDirectory() as temporary:
            checkout=Path(temporary).resolve();namespace=checkout/'scripts';namespace.mkdir()
            (namespace/'verification_process').mkdir()
            for relative in ('artifact_paths.py','verification_process/__init__.py'):
                shutil.copyfile(source/'scripts'/relative,namespace/relative)
            caller=namespace/'caller';caller.mkdir()
            (caller/'module.py').write_text('raise AssertionError("validation must not import")')
            other=namespace/'other.py';other.write_text('raise AssertionError("foreign module")')
            value,_=self.fixture();value.checkout=checkout
            declaration={'scripts.caller':'scripts/caller','scripts.caller.module':'scripts/caller/module.py'}
            original=[str(checkout)]
            state=SimpleNamespace(modules={},path=list(original))
            with patch.object(module,'sys',state):
                value.validate_checkout(declaration)
                self.assertEqual(state.path[:2],[str(namespace),str(checkout)])
            for wrong in ([],False,{'module':'../other.py'}, {'scripts.artifact_paths':'scripts/other.py'}):
                state=SimpleNamespace(modules={},path=list(original))
                with self.subTest(wrong=wrong),patch.object(module,'sys',state):
                    with self.assertRaises(ValueError):value.validate_checkout(wrong)
                    self.assertEqual(state.path,original)
            state=SimpleNamespace(modules={'scripts.caller.module':SimpleNamespace(__file__=str(other))},path=list(original))
            with patch.object(module,'sys',state):
                with self.assertRaises(ValueError):value.validate_checkout(declaration)
                self.assertEqual(state.path,original)
            (caller/'module.py').unlink();(caller/'module.py').symlink_to(other)
            state=SimpleNamespace(modules={},path=list(original))
            with patch.object(module,'sys',state):
                with self.assertRaises(ValueError):value.validate_checkout(declaration)
                self.assertEqual(state.path,original)

    def test_completion_races_never_cancel_an_unrelated_invocation(self):
        for when in ('before-status','after-status','during-cancel'):
            value,records=self.fixture()
            terminal={'job_id':'c'*32,'state':'completed','exit_code':201}
            value.completed=Mock(side_effect=[terminal] if when=='before-status' else [None,terminal])
            value.invocation=Mock(return_value={'entry':{'ticket':'1-'+'a'*32}} if when=='during-cancel' else None)
            value.command.side_effect=[SimpleNamespace(returncode=0,stdout=b'{}'),
                SimpleNamespace(returncode=2,stdout=b'not structured; cancellation lost the race')]
            self.assertTrue(value.settle({'job_id':'c'*32}))
            self.assertEqual(value.command.call_count,{'before-status':0,'after-status':1,'during-cancel':2}[when])
            self.assertEqual(records[-1],('owned-cleanup-completion-race.json',terminal))

    def test_cancel_requires_exact_ticket_and_released_ack(self):
        value,_=self.fixture();value.completed=Mock(return_value=None)
        ticket='1-'+'a'*32;value.invocation=Mock(return_value={'entry':{'ticket':ticket}})
        value.command.side_effect=[SimpleNamespace(returncode=0,stdout=b'{}'),
            SimpleNamespace(returncode=0,stdout=json.dumps({'ticket':'foreign','released':True}).encode())]
        with self.assertRaises(ValueError):value.settle({})
        self.assertEqual(value.command.call_args.args[0][3],ticket)

    def test_missing_invocation_and_unreleased_ack_are_not_completion(self):
        value,_=self.fixture();value.completed=Mock(return_value=None);value.invocation=Mock(return_value=None)
        value.command.return_value=SimpleNamespace(returncode=0,stdout=b'{}')
        with self.assertRaises(ValueError):value.settle({})
        self.assertEqual(value.command.call_count,1)
        value,_=self.fixture();value.completed=Mock(return_value=None)
        ticket='1-'+'a'*32;value.invocation=Mock(return_value={'entry':{'ticket':ticket}})
        value.command.side_effect=[SimpleNamespace(returncode=0,stdout=b'{}'),
            *[SimpleNamespace(returncode=0,stdout=json.dumps({'ticket':ticket,'released':False}).encode())]*2]
        with self.assertRaises(ValueError):value.settle({})
        self.assertEqual(value.command.call_count,3)


if __name__=='__main__':unittest.main()
