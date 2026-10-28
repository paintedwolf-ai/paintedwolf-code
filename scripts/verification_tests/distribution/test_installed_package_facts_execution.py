"""Read-only package facts preserve complete snapshots, physical identity and deadlines."""
from contextlib import ExitStack, contextmanager
import hashlib
import json
import os
from pathlib import Path
import subprocess
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import Mock, patch

from linux_release import installed_package_facts as facts


class SharedImageFactsTests(unittest.TestCase):
    def test_real_image_and_process_birth_are_rechecked(self):
        for failure in (None,'birth','replaced','retired','exec-during-capture','exec-during-validation'):
            with self.subTest(failure=failure),tempfile.TemporaryDirectory() as directory:
                base=Path(directory);image=base/'current';image.write_bytes(b'fixed');image.chmod(0o444)
                retained=base/'retained';os.link(image,retained)
                info=image.lstat();process=base/'proc'/'123';process.mkdir(parents=True)
                (process/'exe').symlink_to(retained)
                inventory={'manifest_images':[{'device':info.st_dev,'inode':info.st_ino,'path':str(image)}],
                           'image_sources':{str(image):facts.stable(info)}}
                if failure=='replaced':
                    replacement=base/'new';replacement.write_bytes(b'other');os.replace(replacement,image)
                elif failure=='retired':image.unlink()
                identity={'pid':123,'birth':1,'pgid':123,'sid':123,'uids':[30060]*4}
                kernel=Mock(return_value=identity)
                if failure=='birth':kernel.side_effect=[identity,{**identity,'birth':2}]
                if failure in {'exec-during-capture','exec-during-validation'}:
                    alternate=base/'alternate';alternate.write_bytes(b'unrelated')
                    calls=[]
                    def same_birth_exec(_pid):
                        calls.append(_pid)
                        transition=2 if failure=='exec-during-capture' else 3
                        if len(calls)==transition:
                            (process/'exe').unlink();(process/'exe').symlink_to(alternate)
                        return identity
                    kernel.side_effect=same_birth_exec
                physical=lambda value:base/'proc' if str(value)=='/proc' else Path(value)
                with patch.object(facts,'Path',physical),patch.object(facts,'kernel',kernel), \
                     patch.object(facts.time,'monotonic',return_value=1):
                    if failure:
                        with self.assertRaises((AssertionError,ValueError)):
                            facts.image_processes(inventory,deadline=10)
                    else:
                        rows=facts.image_processes(inventory,deadline=10)
                        self.assertEqual(len(rows),1);self.assertEqual(rows[0]['birth'],1)
                        self.assertEqual(rows[0]['inode'],info.st_ino)
                        self.assertEqual(kernel.call_count,4)


class CompleteSnapshotTests(unittest.TestCase):
    def test_full_snapshot_keeps_sealed_authority_and_package_inventory_distinct(self):
        selected={'generation_root':'/opt/painted-wolf-code/generations/fixed'}
        authority={'generation':'fixed','manifest_sha256':'sealed'}
        sealed={'policy':selected,'manifest_images':[],'generation_inventory':[],
                'custodian_image_ids':set(),'authority':authority}
        package={'arguments':['dpkg-query'],'exit_code':0,'stdout':'package\tversion\n','stderr':''}
        unit={'MainPID':'0','ControlGroup':'','ActiveState':'inactive','SubState':'dead'}
        with patch.object(facts,'image_inventory',return_value=sealed), \
             patch.object(facts,'image_processes',return_value=[]), \
             patch.object(facts,'unit_state',return_value=unit), \
             patch.object(facts,'query',return_value=package), \
             patch.object(facts,'authority',return_value=authority) as current, \
             patch.object(facts,'digest',return_value='digest'), \
             patch.object(facts.os,'statvfs',return_value=SimpleNamespace(f_bavail=10,f_frsize=4096)):
            result=facts.snapshot(deadline=100)
        current.assert_called_once_with(deadline=100)
        self.assertIs(result['package_inventory'],package)
        self.assertIs(result['policy'],selected)
        self.assertEqual(result['qualification_snapshot']['kind'],'package-inventory')
        expected=facts.hashlib.sha256(facts.json.dumps(package,sort_keys=True).encode()).hexdigest()
        self.assertEqual(result['qualification_snapshot']['value'],expected)
        self.assertEqual(result['available_bytes'],40960)
        self.assertEqual(result['live_product_processes'],[])
        self.assertEqual(result['custodian_processes'],[])
        self.assertEqual(set(result['services']),{'paintedwolf-vault-identity.socket',facts.UNIT})


class PackageObservationFailureTests(unittest.TestCase):
    def test_exhausted_deadline_refuses_before_starting_command(self):
        with patch.object(facts.time,'monotonic',return_value=11),patch.object(facts.subprocess,'run') as run:
            with self.assertRaises(facts.PackageObservationFailure) as failed:
                facts.query(['/usr/bin/systemctl','is-enabled','paintedwolf-vault-identity.socket'],
                            deadline=10,phase='snapshot.service-status')
        run.assert_not_called()
        self.assertEqual(failed.exception.facts['phase'],'snapshot.service-status')
        self.assertEqual(failed.exception.facts['code'],'package_observation_deadline_exhausted')
        self.assertIsNone(failed.exception.facts['exit_code'])
        self.assertFalse(failed.exception.facts['timed_out'])

    def test_short_remaining_command_timeout_is_typed_without_error_prose(self):
        arguments=['/usr/bin/systemctl','is-enabled','paintedwolf-vault-identity.socket']
        error=subprocess.TimeoutExpired(arguments,.008,output=b'private output',stderr=b'private prose')
        with patch.object(facts.time,'monotonic',side_effect=[9.992,10.001]), \
             patch.object(facts.subprocess,'run',side_effect=error) as run:
            with self.assertRaises(facts.PackageObservationFailure) as failed:
                facts.query(arguments,deadline=10,phase='snapshot.service-status')
        self.assertAlmostEqual(run.call_args.kwargs['timeout'],.008)
        self.assertTrue(failed.exception.facts['timed_out'])
        self.assertTrue(failed.exception.facts['deadline_exhausted'])
        self.assertEqual(failed.exception.facts['arguments'],arguments)
        self.assertNotIn('private',json.dumps(failed.exception.facts))


class PhysicalFileFactsTests(unittest.TestCase):
    def test_digest_uses_the_opened_regular_file_and_rejects_replacement_or_symlink(self):
        with tempfile.TemporaryDirectory() as directory:
            path=Path(directory).resolve()/'image';path.write_bytes(b'selected')
            self.assertEqual(facts.digest(path),hashlib.sha256(b'selected').hexdigest())
            alias=path.parent/'alias';alias.symlink_to(path)
            with self.assertRaises(OSError):facts.digest(alias)
            real_hash=hashlib.sha256;changed=[]
            class ReplacingHash:
                def __init__(self):self.value=real_hash()
                def update(self,value):
                    self.value.update(value)
                    if not changed:
                        changed.append(True)
                        replacement=path.parent/'replacement';replacement.write_bytes(b'other')
                        os.replace(replacement,path)
                def hexdigest(self):return self.value.hexdigest()
            with patch.object(facts.hashlib,'sha256',ReplacingHash):
                with self.assertRaises(AssertionError):facts.digest(path)

    def test_kernel_retains_actual_birth_and_uid_fields_and_refuses_malformed_facts(self):
        with tempfile.TemporaryDirectory() as directory:
            proc=Path(directory).resolve()/'proc';selected=proc/'123';selected.mkdir(parents=True)
            fields=['0']*20;fields[0]='R';fields[2]='120';fields[3]='110';fields[19]='456'
            (selected/'stat').write_text('123 (name with spaces) '+' '.join(fields))
            (selected/'status').write_text('Uid:\t30060\t30060\t30060\t30060\n')
            physical=lambda value:proc if str(value)=='/proc' else Path(value)
            with patch.object(facts,'Path',physical):
                self.assertEqual(facts.kernel(123),{'pid':123,'birth':456,'pgid':120,'sid':110,'uids':[30060]*4})
                self.assertIsNone(facts.kernel(124))
                (selected/'status').write_text('Uid:\tunknown\n')
                with self.assertRaises(ValueError):facts.kernel(123)


class PhysicalPackageFactsTests(unittest.TestCase):
    @contextmanager
    def package(self, *, foreign_policy=False):
        with tempfile.TemporaryDirectory() as directory:
            base=Path(directory).resolve();install=base/'package'
            payload=[{'path':'bin/'+name,'kind':'file','mode':0o555}
                     for name in ('painted-wolf-code','pw','pw-vault-identity')]
            raw=json.dumps({'payload':payload},sort_keys=True).encode()
            generation=install/'generations'/hashlib.sha256(raw).hexdigest();generation.mkdir(parents=True)
            manifest=generation/'manifest.json';manifest.write_bytes(raw);manifest.chmod(0o444)
            for root in (install,generation):
                (root/'bin').mkdir()
                for row in payload:
                    image=root/row['path'];image.write_bytes(row['path'].encode());image.chmod(0o555)
            policy=base/'launch.json'
            policy.write_text(json.dumps({'generation_root':str(generation),'manifest_sha256':generation.name}))
            policy.chmod(0o444)
            bootstrap=base/'pw-launch';bootstrap.write_bytes(b'bootstrap');bootstrap.chmod(0o4555)
            mapping={'/etc/paintedwolf/launch.json':policy,
                     '/usr/libexec/painted-wolf-code/pw-launch':bootstrap,
                     '/opt/painted-wolf-code':install,
                     '/opt/painted-wolf-code/generations':install/'generations'}
            physical=lambda value:mapping.get(str(value),Path(value))
            real_lstat,real_fstat=Path.lstat,os.fstat
            def metadata(value,uid=0):
                fields=('st_dev','st_ino','st_mode','st_size','st_mtime_ns','st_ctime_ns')
                return SimpleNamespace(**{name:getattr(value,name) for name in fields},st_uid=uid,st_gid=0)
            # Only root ownership is supplied; bytes, modes, links and physical identities are real.
            with ExitStack() as stack:
                stack.enter_context(patch.object(facts,'INSTALL_ROOT',install))
                stack.enter_context(patch.object(facts,'Path',physical))
                stack.enter_context(patch.object(facts.os,'geteuid',return_value=0))
                stack.enter_context(patch.object(facts.time,'monotonic',return_value=1))
                stack.enter_context(patch.object(Path,'lstat',autospec=True,side_effect=lambda path:
                    metadata(real_lstat(path),uid=99 if foreign_policy and path==policy else 0)))
                stack.enter_context(patch.object(facts.os,'fstat',side_effect=lambda fd:metadata(real_fstat(fd))))
                yield SimpleNamespace(install=install,generation=generation,policy=policy,
                                      bootstrap=bootstrap,manifest=manifest)

    def test_authority_and_inventory_bind_actual_selected_and_flat_images(self):
        with self.package() as package:
            authority=facts.authority(deadline=10)
            inventory=facts.image_inventory(deadline=10)
            self.assertEqual(authority['generation'],package.generation.name)
            self.assertEqual(authority['manifest_sha256'],package.generation.name)
            self.assertEqual(inventory['authority'],authority)
            self.assertEqual(len(inventory['manifest_images']),6)
            self.assertEqual(len(inventory['custodian_image_ids']),2)
            self.assertEqual(inventory['generation_inventory'],[
                {'path':str(package.generation),'manifest_sha256':package.generation.name}])
            images={row['path']:row for row in inventory['manifest_images']}
            flat=images[str(package.install/'bin/pw')];sealed=images[str(package.generation/'bin/pw')]
            self.assertNotEqual(flat['inode'],sealed['inode'])
            facts.unchanged_images(inventory,{(flat['device'],flat['inode']),(sealed['device'],sealed['inode'])})

    def test_package_observation_refuses_foreign_writable_and_symlink_authority(self):
        with patch.object(facts.os,'geteuid',return_value=99),patch.object(facts,'Path') as paths:
            with self.assertRaisesRegex(AssertionError,'root package authority'):
                facts.authority(deadline=10)
            paths.assert_not_called()
        with self.package(foreign_policy=True):
            with self.assertRaises(AssertionError):facts.authority(deadline=10)
        with self.package() as package:
            package.policy.chmod(0o666)
            with self.assertRaises(AssertionError):facts.authority(deadline=10)
            package.policy.chmod(0o444)
            package.bootstrap.unlink();package.bootstrap.symlink_to(package.manifest)
            with self.assertRaises(AssertionError):facts.authority(deadline=10)
        with self.package() as package:
            (package.generation/'bin/pw').chmod(0o777)
            with self.assertRaises(AssertionError):facts.image_inventory(deadline=10)

    def test_inventory_revalidates_selection_after_reading_images(self):
        with self.package() as package:
            actual=facts.authority;calls=[]
            def changed(**kwargs):
                result=actual(**kwargs)
                if not calls:
                    replacement=package.policy.parent/'replacement-policy'
                    selected=json.loads(package.policy.read_bytes())
                    replacement.write_text(json.dumps({**selected,'revision':2}))
                    replacement.chmod(0o444);os.replace(replacement,package.policy)
                calls.append(result)
                return result
            with patch.object(facts,'authority',side_effect=changed):
                with self.assertRaisesRegex(AssertionError,'selection changed'):
                    facts.image_inventory(deadline=10)
            self.assertEqual(len(calls),2)


class ServiceFactsTests(unittest.TestCase):
    def test_unit_state_is_a_closed_property_set_from_the_bounded_query(self):
        fields='MainPID=0\nControlGroup=\nActiveState=inactive\nSubState=dead\n'
        result=SimpleNamespace(returncode=0,stdout=fields,stderr='')
        with patch.object(facts.time,'monotonic',return_value=1), \
             patch.object(facts.subprocess,'run',return_value=result) as run:
            self.assertEqual(facts.unit_state(deadline=3),
                {'MainPID':'0','ControlGroup':'','ActiveState':'inactive','SubState':'dead'})
        self.assertEqual(run.call_args.kwargs['timeout'],2)
        self.assertEqual(run.call_args.kwargs['stdin'],subprocess.DEVNULL)
        self.assertEqual(run.call_args.kwargs['env'],
                         {'PATH':'/run/current-system/sw/bin:/usr/bin:/bin','LC_ALL':'C'})
        for output,code in [(fields+'Unexpected=1\n',0),('MainPID=0\n',0),(fields,1)]:
            with self.subTest(output=output,code=code), \
                 patch.object(facts,'query',return_value={'exit_code':code,'stdout':output}):
                with self.assertRaises(AssertionError):facts.unit_state(deadline=3)
