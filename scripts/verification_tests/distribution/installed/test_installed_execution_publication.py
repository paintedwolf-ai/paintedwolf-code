"""An execution binding becomes visible only as complete private canonical bytes."""
from contextlib import contextmanager
import os
from pathlib import Path
import stat
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release import installed_campaign as campaign
from scripts.linux_release.installed_input import protocol


def binding():
    return {'format_version':1, 'execution_nonce':'a' * 32, 'source_commit':'b' * 40,
        'driver_tree':'c' * 40, 'artifact_id':'deb-amd64', 'environment_id':'noble',
        'variant_id':'floor', 'case_selection':None}


class ExecutionPublicationTests(unittest.TestCase):
    def test_permissive_umask_still_publishes_complete_private_single_link(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / 'guest'
            request = binding()
            previous = os.umask(0o002)
            try:campaign.publish_execution(directory, request)
            finally:os.umask(previous)
            path = directory / 'execution-request.json'
            info = path.stat()
            self.assertEqual(stat.S_IMODE(info.st_mode), 0o600)
            self.assertEqual(info.st_nlink, 1)
            self.assertEqual(path.read_bytes(), protocol.canonical(request))
            self.assertEqual(sorted(p.name for p in directory.iterdir()), ['execution-request.json'])

    def test_partial_write_is_invisible_until_complete_atomic_publication(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / 'guest'
            path = directory / 'execution-request.json'
            request = binding()
            fdopen, replace = os.fdopen, os.replace
            observations = []
            @contextmanager
            def partial_writer(descriptor, mode):
                with fdopen(descriptor, mode) as stream:
                    class Writer:
                        def write(self, raw):
                            middle = len(raw) // 2
                            stream.write(raw[:middle]);stream.flush()
                            observations.append(path.exists())
                            stream.write(raw[middle:])
                            request['execution_nonce'] = 'd' * 32
                            return len(raw)
                        def flush(self):stream.flush()
                        def fileno(self):return stream.fileno()
                    yield Writer()
            expected = protocol.canonical(request)
            def publish(source, destination):
                self.assertFalse(path.exists())
                self.assertEqual(Path(source).read_bytes(), expected)
                replace(source, destination)
                self.assertEqual(path.read_bytes(), expected)
                self.assertEqual(path.stat().st_nlink, 1)
            with patch.object(campaign.os, 'fdopen', side_effect=partial_writer), \
                    patch.object(campaign.os, 'replace', side_effect=publish):
                campaign.publish_execution(directory, request)
            self.assertEqual(observations, [False])

    def test_noncanonical_binding_or_existing_attempt_is_never_published_or_replaced(self):
        for change in ({'format_version':True}, {'source_commit':'A' * 40}, {'extra':1},
                {'case_selection':{'format_version':1, 'cases':[
                    {'capability':'vault', 'case':'service-onboarding'},
                    {'capability':'session', 'case':'lock-during-presence'}]}}):
            with tempfile.TemporaryDirectory() as temporary, self.subTest(change=change):
                directory = Path(temporary) / 'guest'
                with self.assertRaises(ValueError):campaign.publish_execution(directory, {**binding(), **change})
                self.assertFalse(directory.exists())
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / 'guest'
            campaign.publish_execution(directory, binding())
            path = directory / 'execution-request.json'
            original = (path.stat().st_ino, path.read_bytes())
            with self.assertRaises(FileExistsError):campaign.publish_execution(directory, binding())
            self.assertEqual((path.stat().st_ino, path.read_bytes()), original)

    def test_sync_failure_keeps_binding_invisible_and_dispatch_unreachable(self):
        target = {'id':'guest', 'artifact_id':'deb-amd64', 'environment_id':'noble', 'variant_id':'floor',
            'snapshot':{}, 'input_adapter':None, 'case_selection':None, 'final_artifacts':'/selected/final.json',
            'file_root':'/selected/packages', 'install_root':'/selected/app', 'output':'/fresh/leaf',
            'nix_program':None, 'case_inputs':None}
        with tempfile.TemporaryDirectory() as temporary, patch.object(campaign.os, 'fsync', side_effect=OSError('sync refused')), \
                patch.object(campaign, 'run_managed') as managed, patch.object(campaign, 'Bridge') as bridge, \
                patch.object(campaign.subprocess, 'run') as process:
            root = Path(temporary)
            with self.assertRaises(OSError):campaign.run_guest(target, 'b' * 40, 'c' * 40, root)
            self.assertFalse((root / 'guest/execution-request.json').exists())
            self.assertEqual(list((root / 'guest').iterdir()), [])
            managed.assert_not_called();bridge.assert_not_called();process.assert_not_called()


if __name__ == '__main__':unittest.main()
